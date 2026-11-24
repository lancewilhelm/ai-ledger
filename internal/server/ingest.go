package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/lancewilhelm/ai-ledger/internal/archive"
	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/store"
)

// handleIngestStatus reports what the server currently has archived for (device, source, rel_path).
// Query params: source, path
func (s *Server) handleIngestStatus(w http.ResponseWriter, r *http.Request) {
	dev := deviceFromContext(r.Context())
	source := r.URL.Query().Get("source")
	relPath := r.URL.Query().Get("path")
	if source == "" || relPath == "" {
		writeError(w, http.StatusBadRequest, "source and path query parameters are required")
		return
	}

	rec, err := s.store.GetRawFileByKey(r.Context(), dev.ID, source, relPath)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, protocol.IngestStatusResponse{Exists: false, ByteSize: 0})
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, protocol.IngestStatusResponse{
		Exists:       true,
		FileID:       rec.ID,
		ByteSize:     rec.ByteSize,
		SHA256:       rec.SHA256,
		HeaderSHA256: rec.HeaderSHA256,
		ParseStatus:  rec.ParseStatus,
	})
}

// handleIngestChunk appends raw bytes to an append-only file (e.g. JSONL).
// Query params: source, path, offset, header_sha256 (optional), file_kind (optional), sync (optional)
// Request body: raw bytes to append.
func (s *Server) handleIngestChunk(w http.ResponseWriter, r *http.Request) {
	dev := deviceFromContext(r.Context())
	q := r.URL.Query()
	source := q.Get("source")
	relPath := q.Get("path")
	if source == "" || relPath == "" {
		writeError(w, http.StatusBadRequest, "source and path query parameters are required")
		return
	}
	offset, err := strconv.ParseInt(q.Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, "invalid offset")
		return
	}
	fileKind := q.Get("file_kind")
	if fileKind == "" {
		fileKind = protocol.FileKindJSONL
	}
	expectedHeader := q.Get("header_sha256")
	syncParse := s.cfg.SyncParseDefault || q.Get("sync") == "true" || q.Get("sync") == "1"

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	res, err := s.archive.AppendChunk(dev.ID, source, relPath, offset, expectedHeader, r.Body)
	if err != nil {
		if errors.Is(err, archive.ErrOffsetMismatch) || errors.Is(err, archive.ErrHeaderMismatch) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, archive.ErrInvalidPath) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	rec := &protocol.RawFileRecord{
		DeviceID:     dev.ID,
		Source:       source,
		RelPath:      relPath,
		FileKind:     fileKind,
		ArchivePath:  res.RelArchivePath,
		ByteSize:     res.ByteSize,
		SHA256:       res.SHA256,
		HeaderSHA256: res.HeaderSHA256,
		ParseStatus:  "pending",
	}
	if err := s.store.UpsertRawFile(r.Context(), rec); err != nil {
		writeError(w, http.StatusInternalServerError, "save raw file record: "+err.Error())
		return
	}

	if syncParse {
		if err := s.ParseFileNow(r.Context(), rec.ID); err != nil {
			s.log.Warn("sync parse error", "file_id", rec.ID, "err", err)
		}
	} else {
		s.enqueueParse(rec.ID)
	}

	writeJSON(w, http.StatusOK, protocol.IngestChunkResponse{
		FileID:          rec.ID,
		ByteSize:        res.ByteSize,
		SHA256:          res.SHA256,
		HeaderSHA256:    res.HeaderSHA256,
		ParseQueued:     true,
		ArchivedVersion: res.ArchivedVersion,
	})
}

// handleIngestSnapshot uploads a full immutable or snapshot file (.jsonl.zst, SQLite backup, JSON config).
// Query params: source, path, file_kind, sync (optional)
// Request body: full file bytes.
func (s *Server) handleIngestSnapshot(w http.ResponseWriter, r *http.Request) {
	dev := deviceFromContext(r.Context())
	q := r.URL.Query()
	source := q.Get("source")
	relPath := q.Get("path")
	if source == "" || relPath == "" {
		writeError(w, http.StatusBadRequest, "source and path query parameters are required")
		return
	}
	fileKind := q.Get("file_kind")
	if fileKind == "" {
		fileKind = protocol.FileKindOther
	}
	syncParse := s.cfg.SyncParseDefault || q.Get("sync") == "true" || q.Get("sync") == "1"

	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	res, err := s.archive.WriteSnapshot(dev.ID, source, relPath, r.Body)
	if err != nil {
		if errors.Is(err, archive.ErrInvalidPath) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	rec := &protocol.RawFileRecord{
		DeviceID:     dev.ID,
		Source:       source,
		RelPath:      relPath,
		FileKind:     fileKind,
		ArchivePath:  res.RelArchivePath,
		ByteSize:     res.ByteSize,
		SHA256:       res.SHA256,
		HeaderSHA256: res.HeaderSHA256,
		ParseStatus:  "pending",
	}
	if res.Duplicate {
		if existing, err := s.store.GetRawFileByKey(r.Context(), dev.ID, source, relPath); err == nil {
			writeJSON(w, http.StatusOK, protocol.IngestChunkResponse{
				FileID:       existing.ID,
				ByteSize:     existing.ByteSize,
				SHA256:       existing.SHA256,
				HeaderSHA256: existing.HeaderSHA256,
				ParseQueued:  false,
				Duplicate:    true,
			})
			return
		}
	}

	if err := s.store.UpsertRawFile(r.Context(), rec); err != nil {
		writeError(w, http.StatusInternalServerError, "save raw file record: "+err.Error())
		return
	}

	if syncParse {
		if err := s.ParseFileNow(r.Context(), rec.ID); err != nil {
			s.log.Warn("sync parse error", "file_id", rec.ID, "err", err)
		}
	} else {
		s.enqueueParse(rec.ID)
	}

	writeJSON(w, http.StatusOK, protocol.IngestChunkResponse{
		FileID:          rec.ID,
		ByteSize:        res.ByteSize,
		SHA256:          res.SHA256,
		HeaderSHA256:    res.HeaderSHA256,
		ParseQueued:     true,
		Duplicate:       res.Duplicate,
		ArchivedVersion: res.ArchivedVersion,
	})
}
