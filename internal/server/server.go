package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/archive"
	"github.com/lancewilhelm/ai-ledger/internal/parser"
	"github.com/lancewilhelm/ai-ledger/internal/pricing"
	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/store"
)

// Config configures the AI Ledger HTTP server.
type Config struct {
	BindAddr         string
	Version          string
	AdminToken       string // optional; if set, protects /api/admin/*
	MaxUploadBytes   int64  // default 2 GiB (large SQLite snapshots!)
	WebFS            fs.FS  // embedded dashboard static files
	Logger           *slog.Logger
	SyncParseDefault bool // useful for deterministic tests
}

// Server serves both the collector ingest API and the dashboard/analytics API.
type Server struct {
	cfg     Config
	store   store.Store
	archive *archive.Store
	parsers *parser.Registry
	pricing *pricing.Calculator
	log     *slog.Logger
	mux     *http.ServeMux

	// Async parse worker with deduplicated pending queue
	parseMu      sync.Mutex
	parsePending map[string]struct{}
	parseCh      chan string
	workerWg     sync.WaitGroup
	stopCh       chan struct{}
	stopOnce     sync.Once
}

// New constructs a Server and starts its background parser worker.
func New(cfg Config, st store.Store, arch *archive.Store, calc *pricing.Calculator) *Server {
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = 2 << 30 // 2 GiB
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{
		cfg:          cfg,
		store:        st,
		archive:      arch,
		parsers:      parser.NewRegistry(calc),
		pricing:      calc,
		log:          cfg.Logger,
		mux:          http.NewServeMux(),
		parsePending: make(map[string]struct{}),
		parseCh:      make(chan string, 4096),
		stopCh:       make(chan struct{}),
	}
	s.registerRoutes()
	s.workerWg.Add(1)
	go s.parseWorker()
	return s
}

// Close gracefully stops the background parser worker.
func (s *Server) Close() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.workerWg.Wait()
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler {
	return s.logMiddleware(s.mux)
}

func (s *Server) registerRoutes() {
	// Health
	s.mux.HandleFunc("GET /api/health", s.handleHealth)

	// Ingest API (device token required)
	s.mux.HandleFunc("GET /api/ingest/status", s.withDeviceAuth(s.handleIngestStatus))
	s.mux.HandleFunc("POST /api/ingest/chunk", s.withDeviceAuth(s.handleIngestChunk))
	s.mux.HandleFunc("POST /api/ingest/snapshot", s.withDeviceAuth(s.handleIngestSnapshot))

	// Dashboard & Analytics API
	s.mux.HandleFunc("GET /api/overview", s.handleOverview)
	s.mux.HandleFunc("GET /api/timeseries", s.handleTimeseries)
	s.mux.HandleFunc("GET /api/breakdown", s.handleBreakdown)
	s.mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	s.mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	s.mux.HandleFunc("GET /api/sessions/{id}/raw", s.handleGetSessionRaw)
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/devices", s.handleListDevices)
	s.mux.HandleFunc("GET /api/files", s.handleListFiles)
	s.mux.HandleFunc("GET /api/filters", s.handleFilters)
	s.mux.HandleFunc("GET /api/export/training", s.handleExportTraining)

	// Admin API
	s.mux.HandleFunc("POST /api/admin/reparse", s.withAdminAuth(s.handleAdminReparse))
	s.mux.HandleFunc("POST /api/admin/devices", s.withAdminAuth(s.handleAdminCreateDevice))
	s.mux.HandleFunc("DELETE /api/admin/devices/{id}", s.withAdminAuth(s.handleAdminRevokeDevice))

	// Embedded Web Dashboard (SPA fallback)
	if s.cfg.WebFS != nil {
		s.mux.Handle("/", s.spaHandler(s.cfg.WebFS))
	}
}

func (s *Server) enqueueParse(fileID string) {
	s.parseMu.Lock()
	if _, exists := s.parsePending[fileID]; exists {
		s.parseMu.Unlock()
		return
	}
	s.parsePending[fileID] = struct{}{}
	s.parseMu.Unlock()

	select {
	case s.parseCh <- fileID:
	default:
		// Channel full: remove pending mark so a later reparse or next sync picks it up
		s.parseMu.Lock()
		delete(s.parsePending, fileID)
		s.parseMu.Unlock()
	}
}

func (s *Server) parseWorker() {
	defer s.workerWg.Done()
	for {
		select {
		case <-s.stopCh:
			return
		case fileID := <-s.parseCh:
			s.parseMu.Lock()
			delete(s.parsePending, fileID)
			s.parseMu.Unlock()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			if err := s.ParseFileNow(ctx, fileID); err != nil {
				s.log.Warn("parse file failed", "file_id", fileID, "err", err)
			}
			cancel()
		}
	}
}

// ParseFileNow parses one archived raw file immediately and persists the normalized output.
func (s *Server) ParseFileNow(ctx context.Context, fileID string) error {
	rec, err := s.store.GetRawFileByID(ctx, fileID)
	if err != nil {
		return err
	}
	absPath := s.archive.AbsPath(rec.ArchivePath)
	bundle, err := s.parsers.Parse(ctx, parser.FileInput{
		FileID:   rec.ID,
		DeviceID: rec.DeviceID,
		Source:   rec.Source,
		RelPath:  rec.RelPath,
		FileKind: rec.FileKind,
		AbsPath:  absPath,
	})
	if err != nil {
		_ = s.store.MarkRawFileParseError(ctx, fileID, err.Error())
		return err
	}
	return s.store.ReplaceFileParsedData(ctx, fileID, bundle)
}

// ReparseAll re-parses every archived file in order (rates & T3 metadata first, then native logs).
func (s *Server) ReparseAll(ctx context.Context) (parsed int, failed int, err error) {
	files, err := s.store.ListAllRawFiles(ctx)
	if err != nil {
		return 0, 0, err
	}
	// Load any archived rates files first so pricing is up-to-date
	for _, f := range files {
		if f.Source == protocol.SourceRates {
			if perr := s.ParseFileNow(ctx, f.ID); perr != nil {
				failed++
			} else {
				parsed++
			}
		}
	}
	for _, f := range files {
		if f.Source == protocol.SourceRates {
			continue
		}
		if ctx.Err() != nil {
			return parsed, failed, ctx.Err()
		}
		if perr := s.ParseFileNow(ctx, f.ID); perr != nil {
			s.log.Warn("reparse error", "file_id", f.ID, "source", f.Source, "rel_path", f.RelPath, "err", perr)
			failed++
		} else {
			parsed++
		}
	}
	return parsed, failed, nil
}

// --- Auth Helpers ---

type ctxKey string

const ctxKeyDevice ctxKey = "device"

// GenerateToken returns a random opaque bearer token and its SHA-256 hex digest.
func GenerateToken() (plaintext string, hashHex string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plaintext = "ail_" + hex.EncodeToString(buf)
	return plaintext, HashToken(plaintext), nil
}

// HashToken returns the SHA-256 hex digest of a bearer token.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tok)))
	return hex.EncodeToString(sum[:])
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func (s *Server) withDeviceAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearerToken(r)
		if tok == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		dev, err := s.store.AuthenticateDevice(r.Context(), HashToken(tok))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusUnauthorized, "invalid or revoked device token")
				return
			}
			writeError(w, http.StatusInternalServerError, "auth check failed")
			return
		}
		// Update device telemetry headers if sent by collector
		hostname := r.Header.Get("X-Device-Hostname")
		osName := r.Header.Get("X-Device-OS")
		arch := r.Header.Get("X-Device-Arch")
		_ = s.store.TouchDevice(r.Context(), dev.ID, hostname, osName, arch)

		ctx := context.WithValue(r.Context(), ctxKeyDevice, dev)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) withAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminToken != "" {
			tok := bearerToken(r)
			if tok != s.cfg.AdminToken {
				writeError(w, http.StatusUnauthorized, "invalid admin token")
				return
			}
		}
		next(w, r)
	}
}

func deviceFromContext(ctx context.Context) *protocol.DeviceInfo {
	v, _ := ctx.Value(ctxKeyDevice).(*protocol.DeviceInfo)
	return v
}

// --- Response & Middleware Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rw.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) spaHandler(webFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(webFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "api endpoint not found")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if f, err := webFS.Open(path); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA client-side route fallback -> index.html
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}
