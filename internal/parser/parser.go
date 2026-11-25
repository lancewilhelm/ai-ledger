package parser

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/lancewilhelm/ai-ledger/internal/pricing"
	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/store"
)

// FileInput describes an archived file to parse.
type FileInput struct {
	FileID   string
	DeviceID string
	Source   string
	RelPath  string
	FileKind string
	AbsPath  string
}

// Registry dispatches raw files to the appropriate harness parser.
type Registry struct {
	Pricing *pricing.Calculator
}

func NewRegistry(calc *pricing.Calculator) *Registry {
	return &Registry{Pricing: calc}
}

// Parse dispatches a raw archived file to its matching parser.
func (r *Registry) Parse(ctx context.Context, in FileInput) (*store.ParsedFileBundle, error) {
	switch in.Source {
	case protocol.SourceCodex:
		return r.parseCodex(ctx, in)
	case protocol.SourceClaude:
		return r.parseClaude(ctx, in)
	case protocol.SourceAntigravity:
		return r.parseAntigravity(ctx, in)
	case protocol.SourceT3:
		return r.parseT3(ctx, in)
	case protocol.SourceHermes:
		return r.parseHermes(ctx, in)
	case protocol.SourceRates:
		// Hot-load pricing rates into the live calculator; no session rows produced.
		if _, err := r.Pricing.LoadT3RatesFile(in.AbsPath); err != nil {
			return nil, fmt.Errorf("load rates file: %w", err)
		}
		return &store.ParsedFileBundle{}, nil
	default:
		// Unknown source: raw file is still archived losslessly!
		return &store.ParsedFileBundle{}, nil
	}
}

// openMaybeZstd opens a file and transparently wraps it in a zstd decoder if it ends in .zst.
func openMaybeZstd(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(strings.ToLower(path), ".zst") {
		dec, err := zstd.NewReader(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		return &zstdReadCloser{dec: dec, f: f}, nil
	}
	return f, nil
}

type zstdReadCloser struct {
	dec *zstd.Decoder
	f   *os.File
}

func (z *zstdReadCloser) Read(p []byte) (int, error) { return z.dec.Read(p) }
func (z *zstdReadCloser) Close() error {
	z.dec.Close()
	return z.f.Close()
}

// isSQLiteFile reports whether a filename looks like a raw or zstd-compressed SQLite file.
func isSQLiteFile(path string) bool {
	lower := strings.ToLower(path)
	lower = strings.TrimSuffix(lower, ".zst")
	return strings.HasSuffix(lower, ".sqlite") || strings.HasSuffix(lower, ".db")
}

// prepareSQLitePath returns a readable SQLite file path. If absPath is zstd-compressed
// (.zst), it decompresses into a temporary file and returns a cleanup function.
func prepareSQLitePath(absPath string) (string, func(), error) {
	if !strings.HasSuffix(strings.ToLower(absPath), ".zst") {
		return absPath, func() {}, nil
	}
	rc, err := openMaybeZstd(absPath)
	if err != nil {
		return "", nil, err
	}
	defer rc.Close()

	tmpFile, err := os.CreateTemp("", "ai-ledger-parse-*.sqlite")
	if err != nil {
		return "", nil, err
	}
	tmpPath := tmpFile.Name()
	if _, err := io.Copy(tmpFile, rc); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return "", nil, fmt.Errorf("decompress sqlite %s: %w", filepath.Base(absPath), err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return "", nil, err
	}
	return tmpPath, func() { _ = os.Remove(tmpPath) }, nil
}

// scanJSONL streams lines of arbitrary length (up to 64 MiB per line) without hitting bufio.Scanner's 64 KiB cap.
func scanJSONL(r io.Reader, fn func(lineNum int, line []byte) error) error {
	br := bufio.NewReaderSize(r, 256*1024)
	lineNum := 0
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			lineNum++
			trimmed := strings.TrimSpace(string(line))
			if trimmed != "" {
				if cbErr := fn(lineNum, []byte(trimmed)); cbErr != nil {
					return cbErr
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// stableID creates a deterministic hex ID from parts so re-parsing produces stable IDs.
func stableID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return prefix + "_" + hex.EncodeToString(h.Sum(nil))[:24]
}

// deriveProjectName turns a cwd or T3 worktree path into a clean project name.
func deriveProjectName(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	cwd = strings.ReplaceAll(cwd, "\\", "/")
	cwd = strings.TrimSuffix(cwd, "/")

	// Handle T3 worktree paths like /Users/foo/.t3/worktrees/<project>/<branch-hash>
	if idx := strings.Index(cwd, "/.t3/worktrees/"); idx >= 0 {
		rest := cwd[idx+len("/.t3/worktrees/"):]
		parts := strings.Split(rest, "/")
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}
	// Handle T3 scratch paths like /Users/foo/.t3/scratch/<slug>
	if strings.Contains(cwd, "/.t3/scratch/") {
		return "scratch"
	}
	return filepath.Base(cwd)
}

func parseFlexibleTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func truncateTitle(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	// Strip T3/internal XML wrappers if they lead the prompt
	if idx := strings.Index(s, "<user_request>"); idx >= 0 {
		s = s[idx+len("<user_request>"):]
	}
	if idx := strings.Index(s, "</user_request>"); idx >= 0 {
		s = s[:idx]
	}
	if idx := strings.Index(s, "User message:\n"); idx >= 0 {
		s = s[idx+len("User message:\n"):]
	}
	s = strings.TrimSpace(s)
	if nl := strings.IndexAny(s, "\r\n"); nl >= 0 {
		s = strings.TrimSpace(s[:nl])
	}
	if len(s) > maxLen {
		return strings.TrimSpace(s[:maxLen-1]) + "…"
	}
	return s
}

func mostFrequentModel(counts map[string]int) string {
	best := ""
	bestN := 0
	for m, n := range counts {
		if m != "" && (n > bestN || (n == bestN && m < best)) {
			best = m
			bestN = n
		}
	}
	return best
}
