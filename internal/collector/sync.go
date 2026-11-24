package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/archive"
	"github.com/lancewilhelm/ai-ledger/internal/protocol"
)

// SyncStats summarizes one collector sync pass.
type SyncStats struct {
	Discovered     int   `json:"discovered"`
	Skipped        int   `json:"skipped"`
	UploadedFiles  int   `json:"uploaded_files"`
	UploadedBytes  int64 `json:"uploaded_bytes"`
	Errors         int   `json:"errors"`
}

// Syncer performs incremental uploads from local AI log files to an ai-ledger server.
type Syncer struct {
	cfg      Config
	state    *State
	client   *http.Client
	hostname string
	log      *slog.Logger
	dryRun   bool
}

func NewSyncer(cfg Config, st *State, log *slog.Logger, dryRun bool) *Syncer {
	if log == nil {
		log = slog.Default()
	}
	host, _ := os.Hostname()
	return &Syncer{
		cfg:   cfg,
		state: st,
		client: &http.Client{
			Timeout: 10 * time.Minute, // allow large SQLite snapshot uploads over Tailscale
		},
		hostname: host,
		log:      log,
		dryRun:   dryRun,
	}
}

// SyncOnce discovers all configured log files and incrementally syncs any new data.
func (s *Syncer) SyncOnce(ctx context.Context) (SyncStats, error) {
	var stats SyncStats
	files, err := Discover(s.cfg)
	if err != nil {
		return stats, err
	}
	stats.Discovered = len(files)

	for _, f := range files {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		var uploaded int64
		var didUpload bool
		var syncErr error

		if f.IsAppend {
			uploaded, didUpload, syncErr = s.syncAppendFile(ctx, f)
		} else {
			uploaded, didUpload, syncErr = s.syncSnapshotFile(ctx, f)
		}

		if syncErr != nil {
			stats.Errors++
			s.log.Warn("sync file failed", "source", f.Source, "path", f.RelPath, "err", syncErr)
			continue
		}
		if didUpload {
			stats.UploadedFiles++
			stats.UploadedBytes += uploaded
		} else {
			stats.Skipped++
		}
	}

	if !s.dryRun {
		if err := s.state.Save(); err != nil {
			s.log.Warn("save collector state failed", "err", err)
		}
	}
	return stats, nil
}

func (s *Syncer) syncAppendFile(ctx context.Context, f CandidateFile) (int64, bool, error) {
	cursor, hasCursor := s.state.Get(f.Source, f.RelPath)
	if hasCursor && cursor.Size == f.Size && cursor.MtimeUnix == f.Mtime && cursor.Offset == f.Size {
		return 0, false, nil
	}

	// Determine safe end offset: up to the last newline so we never ship a half-written JSON line
	safeEnd, err := lastNewlineOffset(f.AbsPath, f.Size)
	if err != nil {
		return 0, false, err
	}
	if safeEnd == 0 {
		return 0, false, nil
	}

	startOffset := cursor.Offset
	expectedHeader := cursor.HeaderSHA256

	// Verify prefix up to min(startOffset, HeaderHashBytes) so growing small files (<4 KiB)
	// still match the server's previous header hash cleanly.
	if !hasCursor || safeEnd < startOffset {
		startOffset = 0
		expectedHeader = ""
	} else if expectedHeader != "" && startOffset > 0 {
		curPrefix, herr := archive.HashPrefix(f.AbsPath, startOffset)
		if herr != nil {
			return 0, false, herr
		}
		if curPrefix != expectedHeader {
			startOffset = 0
			expectedHeader = ""
		}
	}
	if startOffset == safeEnd {
		cursor.Size = f.Size
		cursor.MtimeUnix = f.Mtime
		s.state.Set(f.Source, f.RelPath, cursor)
		return 0, false, nil
	}

	if s.dryRun {
		s.log.Info("[dry-run] would append", "source", f.Source, "path", f.RelPath, "from", startOffset, "to", safeEnd, "bytes", safeEnd-startOffset)
		return safeEnd - startOffset, true, nil
	}

	// If we have no local cursor yet, ask server first to avoid re-uploading already-archived files
	if !hasCursor {
		status, err := s.getRemoteStatus(ctx, f.Source, f.RelPath)
		if err == nil && status.Exists && status.ByteSize <= safeEnd {
			curPrefix, herr := archive.HashPrefix(f.AbsPath, status.ByteSize)
			if herr == nil && status.HeaderSHA256 == curPrefix {
				startOffset = status.ByteSize
				expectedHeader = status.HeaderSHA256
				if startOffset == safeEnd {
					s.state.Set(f.Source, f.RelPath, FileCursor{
						Offset:       safeEnd,
						Size:         f.Size,
						MtimeUnix:    f.Mtime,
						SHA256:       status.SHA256,
						HeaderSHA256: status.HeaderSHA256,
						LastSyncAt:   time.Now().UTC(),
					})
					return 0, false, nil
				}
			}
		}
	}

	uploaded, lastResp, err := s.uploadChunks(ctx, f, startOffset, safeEnd, expectedHeader)
	if err != nil {
		// On conflict, reconcile once with server status
		status, serr := s.getRemoteStatus(ctx, f.Source, f.RelPath)
		if serr != nil {
			return 0, false, err
		}
		curPrefix, _ := archive.HashPrefix(f.AbsPath, status.ByteSize)
		if status.Exists && status.ByteSize <= safeEnd && status.HeaderSHA256 == curPrefix {
			startOffset = status.ByteSize
			expectedHeader = status.HeaderSHA256
		} else {
			startOffset = 0
			expectedHeader = ""
		}
		if startOffset == safeEnd {
			s.state.Set(f.Source, f.RelPath, FileCursor{
				Offset:       safeEnd,
				Size:         f.Size,
				MtimeUnix:    f.Mtime,
				SHA256:       status.SHA256,
				HeaderSHA256: status.HeaderSHA256,
				LastSyncAt:   time.Now().UTC(),
			})
			return 0, false, nil
		}
		uploaded, lastResp, err = s.uploadChunks(ctx, f, startOffset, safeEnd, expectedHeader)
		if err != nil {
			return 0, false, err
		}
	}

	s.state.Set(f.Source, f.RelPath, FileCursor{
		Offset:       lastResp.ByteSize,
		Size:         f.Size,
		MtimeUnix:    f.Mtime,
		SHA256:       lastResp.SHA256,
		HeaderSHA256: lastResp.HeaderSHA256,
		LastSyncAt:   time.Now().UTC(),
	})
	return uploaded, uploaded > 0, nil
}

func (s *Syncer) uploadChunks(ctx context.Context, f CandidateFile, start, end int64, expectedHeader string) (int64, *protocol.IngestChunkResponse, error) {
	fh, err := os.Open(f.AbsPath)
	if err != nil {
		return 0, nil, err
	}
	defer fh.Close()

	var totalSent int64
	var lastResp *protocol.IngestChunkResponse
	cur := start
	hdr := expectedHeader
	chunkSize := s.cfg.ChunkSizeBytes
	if chunkSize <= 0 {
		chunkSize = 8 << 20
	}

	for cur < end {
		n := end - cur
		if n > chunkSize {
			n = chunkSize
		}
		if _, err := fh.Seek(cur, io.SeekStart); err != nil {
			return totalSent, lastResp, err
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(fh, buf); err != nil {
			return totalSent, lastResp, err
		}

		q := url.Values{}
		q.Set("source", f.Source)
		q.Set("path", f.RelPath)
		q.Set("offset", fmt.Sprintf("%d", cur))
		q.Set("file_kind", f.FileKind)
		if hdr != "" && cur > 0 {
			q.Set("header_sha256", hdr)
		}

		endpoint := s.cfg.ServerURL + "/api/ingest/chunk?" + q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
		if err != nil {
			return totalSent, lastResp, err
		}
		s.addHeaders(req)
		req.Header.Set("Content-Type", "application/octet-stream")

		resp, err := s.client.Do(req)
		if err != nil {
			return totalSent, lastResp, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return totalSent, lastResp, fmt.Errorf("ingest chunk HTTP %d: %s", resp.StatusCode, string(body))
		}
		var cr protocol.IngestChunkResponse
		if err := json.Unmarshal(body, &cr); err != nil {
			return totalSent, lastResp, err
		}
		lastResp = &cr
		hdr = cr.HeaderSHA256
		cur = cr.ByteSize
		totalSent += n
	}
	return totalSent, lastResp, nil
}

func (s *Syncer) syncSnapshotFile(ctx context.Context, f CandidateFile) (int64, bool, error) {
	cursor, hasCursor := s.state.Get(f.Source, f.RelPath)
	if hasCursor && cursor.MtimeUnix == f.Mtime && cursor.Size == f.Size {
		return 0, false, nil
	}

	// Throttle heavy SQLite snapshots according to SnapshotIntervalMinutes
	if f.FileKind == protocol.FileKindSQLiteSnapshot && hasCursor {
		minInterval := time.Duration(s.cfg.SnapshotIntervalMinutes) * time.Minute
		if time.Since(cursor.LastSyncAt) < minInterval {
			return 0, false, nil
		}
	}

	uploadPath := f.AbsPath
	var cleanupDir string
	if f.FileKind == protocol.FileKindSQLiteSnapshot {
		if s.dryRun {
			s.log.Info("[dry-run] would snapshot SQLite DB", "source", f.Source, "path", f.RelPath, "bytes", f.Size)
			return f.Size, true, nil
		}
		snapPath, err := CreateSQLiteSnapshot(ctx, f.AbsPath)
		if err != nil {
			return 0, false, fmt.Errorf("sqlite snapshot: %w", err)
		}
		uploadPath = snapPath
		cleanupDir = filepath.Dir(snapPath)
		defer os.RemoveAll(cleanupDir)
	}

	size, sha, headerSHA, err := archive.FileHashes(uploadPath)
	if err != nil {
		return 0, false, err
	}
	if hasCursor && cursor.SHA256 == sha {
		cursor.MtimeUnix = f.Mtime
		cursor.Size = f.Size
		cursor.LastSyncAt = time.Now().UTC()
		s.state.Set(f.Source, f.RelPath, cursor)
		return 0, false, nil
	}

	if s.dryRun {
		s.log.Info("[dry-run] would upload snapshot", "source", f.Source, "path", f.RelPath, "bytes", size)
		return size, true, nil
	}

	// Check if server already has this exact SHA-256 (avoids re-uploading after local state reset)
	if status, err := s.getRemoteStatus(ctx, f.Source, f.RelPath); err == nil && status.Exists && status.SHA256 == sha {
		s.state.Set(f.Source, f.RelPath, FileCursor{
			Offset:       size,
			Size:         f.Size,
			MtimeUnix:    f.Mtime,
			SHA256:       sha,
			HeaderSHA256: headerSHA,
			LastSyncAt:   time.Now().UTC(),
		})
		return 0, false, nil
	}

	fh, err := os.Open(uploadPath)
	if err != nil {
		return 0, false, err
	}
	defer fh.Close()

	q := url.Values{}
	q.Set("source", f.Source)
	q.Set("path", f.RelPath)
	q.Set("file_kind", f.FileKind)
	endpoint := s.cfg.ServerURL + "/api/ingest/snapshot?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, fh)
	if err != nil {
		return 0, false, err
	}
	req.ContentLength = size
	s.addHeaders(req)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, false, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("ingest snapshot HTTP %d: %s", resp.StatusCode, string(body))
	}

	s.state.Set(f.Source, f.RelPath, FileCursor{
		Offset:       size,
		Size:         f.Size,
		MtimeUnix:    f.Mtime,
		SHA256:       sha,
		HeaderSHA256: headerSHA,
		LastSyncAt:   time.Now().UTC(),
	})
	return size, true, nil
}

func (s *Syncer) getRemoteStatus(ctx context.Context, source, relPath string) (*protocol.IngestStatusResponse, error) {
	q := url.Values{}
	q.Set("source", source)
	q.Set("path", relPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.ServerURL+"/api/ingest/status?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	s.addHeaders(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status HTTP %d", resp.StatusCode)
	}
	var out protocol.IngestStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *Syncer) addHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+s.cfg.DeviceToken)
	req.Header.Set("X-Device-Hostname", s.hostname)
	req.Header.Set("X-Device-OS", runtime.GOOS)
	req.Header.Set("X-Device-Arch", runtime.GOARCH)
}

// lastNewlineOffset scans backwards from size to find the byte offset immediately after the last '\n'.
// If the file does not end with '\n' and is small/complete (not modified in the last 30s), returns full size.
func lastNewlineOffset(path string, size int64) (int64, error) {
	if size <= 0 {
		return 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if st.Size() < size {
		size = st.Size()
	}

	const probe = 64 * 1024
	start := size - probe
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return 0, err
	}
	if idx := bytes.LastIndexByte(buf, '\n'); idx >= 0 {
		return start + int64(idx) + 1, nil
	}
	// If file hasn't been touched in 30s, allow single-line JSONL without trailing newline
	if time.Since(st.ModTime()) > 30*time.Second {
		return size, nil
	}
	return 0, nil
}
