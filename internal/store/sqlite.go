package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

// SQLiteStore implements Store backed by a single SQLite database in WAL mode.
type SQLiteStore struct {
	db              *sql.DB
	migrationResult MigrationResult
}

// OpenSQLite opens (and migrates) the SQLite database at dbPath.
func OpenSQLite(dbPath string) (*SQLiteStore, error) {
	if dbPath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := dbPath
	if dbPath != ":memory:" {
		dsn = fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(ON)", dbPath)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Single connection eliminates write contention in SQLite WAL mode.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if dbPath == ":memory:" {
		_, _ = db.Exec(`PRAGMA foreign_keys = ON;`)
	}
	s := &SQLiteStore{db: db}
	res, err := s.migrate(context.Background(), dbPath)
	if err != nil {
		db.Close()
		return nil, err
	}
	s.migrationResult = res
	return s, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func formatTimePtr(t *time.Time) sql.NullString {
	if t == nil || t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(*t), Valid: true}
}

func parseTimeStr(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	return time.Time{}
}

func parseNullTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := parseTimeStr(ns.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- Devices ---

func (s *SQLiteStore) CreateDevice(ctx context.Context, name, hostname, osName, arch, tokenHash string) (*protocol.DeviceInfo, error) {
	now := time.Now().UTC()
	id := uuid.NewString()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO devices (id, name, hostname, os, arch, token_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, id, name, hostname, osName, arch, tokenHash, formatTime(now))
	if err != nil {
		return nil, err
	}
	return &protocol.DeviceInfo{
		ID:        id,
		Name:      name,
		Hostname:  hostname,
		OS:        osName,
		Arch:      arch,
		CreatedAt: now,
	}, nil
}

func (s *SQLiteStore) AuthenticateDevice(ctx context.Context, tokenHash string) (*protocol.DeviceInfo, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, hostname, os, arch, created_at, last_seen_at, revoked_at
		FROM devices WHERE token_hash = ?
	`, tokenHash)
	var d protocol.DeviceInfo
	var createdAt string
	var lastSeen, revoked sql.NullString
	if err := row.Scan(&d.ID, &d.Name, &d.Hostname, &d.OS, &d.Arch, &createdAt, &lastSeen, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if revoked.Valid && revoked.String != "" {
		return nil, ErrNotFound
	}
	d.CreatedAt = parseTimeStr(createdAt)
	d.LastSeenAt = parseNullTime(lastSeen)
	return &d, nil
}

func (s *SQLiteStore) TouchDevice(ctx context.Context, deviceID, hostname, osName, arch string) error {
	now := formatTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `
		UPDATE devices
		SET last_seen_at = ?,
			hostname = CASE WHEN ? != '' THEN ? ELSE hostname END,
			os = CASE WHEN ? != '' THEN ? ELSE os END,
			arch = CASE WHEN ? != '' THEN ? ELSE arch END
		WHERE id = ?
	`, now, hostname, hostname, osName, osName, arch, arch, deviceID)
	return err
}

func (s *SQLiteStore) RevokeDevice(ctx context.Context, deviceID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET revoked_at = ? WHERE id = ? OR name = ?`, formatTime(time.Now().UTC()), deviceID, deviceID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) ListDevices(ctx context.Context) ([]protocol.DeviceInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			d.id, d.name, d.hostname, d.os, d.arch, d.created_at, d.last_seen_at, d.revoked_at,
			COALESCE((SELECT COUNT(*) FROM raw_files rf WHERE rf.device_id = d.id), 0) AS file_count,
			COALESCE((SELECT SUM(byte_size) FROM raw_files rf WHERE rf.device_id = d.id), 0) AS archive_bytes,
			COALESCE((SELECT COUNT(*) FROM sessions se WHERE se.device_id = d.id), 0) AS session_count,
			COALESCE((SELECT SUM(u.input_tokens + u.cached_input_tokens + u.cache_write_5m_tokens + u.cache_write_1h_tokens + u.output_tokens) FROM usage_events u WHERE u.device_id = d.id), 0) AS total_tokens,
			COALESCE((SELECT SUM(u.estimated_cost_usd) FROM usage_events u WHERE u.device_id = d.id), 0) AS estimated_cost
		FROM devices d
		ORDER BY d.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.DeviceInfo
	for rows.Next() {
		var d protocol.DeviceInfo
		var createdAt string
		var lastSeen, revoked sql.NullString
		if err := rows.Scan(
			&d.ID, &d.Name, &d.Hostname, &d.OS, &d.Arch, &createdAt, &lastSeen, &revoked,
			&d.FileCount, &d.ArchiveBytes, &d.SessionCount, &d.TotalTokens, &d.EstimatedCost,
		); err != nil {
			return nil, err
		}
		d.CreatedAt = parseTimeStr(createdAt)
		d.LastSeenAt = parseNullTime(lastSeen)
		d.RevokedAt = parseNullTime(revoked)
		out = append(out, d)
	}
	return out, rows.Err()
}

// --- Raw Files ---

func (s *SQLiteStore) GetRawFileByKey(ctx context.Context, deviceID, source, relPath string) (*protocol.RawFileRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, device_id, source, rel_path, file_kind, archive_path, byte_size, sha256, header_sha256,
		       parse_status, parse_error, parsed_at, created_at, updated_at
		FROM raw_files WHERE device_id = ? AND source = ? AND rel_path = ?
	`, deviceID, source, relPath)
	return scanRawFile(row)
}

func (s *SQLiteStore) GetRawFileByID(ctx context.Context, fileID string) (*protocol.RawFileRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, device_id, source, rel_path, file_kind, archive_path, byte_size, sha256, header_sha256,
		       parse_status, parse_error, parsed_at, created_at, updated_at
		FROM raw_files WHERE id = ?
	`, fileID)
	return scanRawFile(row)
}

func scanRawFile(row *sql.Row) (*protocol.RawFileRecord, error) {
	var r protocol.RawFileRecord
	var parsedAt sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&r.ID, &r.DeviceID, &r.Source, &r.RelPath, &r.FileKind, &r.ArchivePath,
		&r.ByteSize, &r.SHA256, &r.HeaderSHA256, &r.ParseStatus, &r.ParseError,
		&parsedAt, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.ParsedAt = parseNullTime(parsedAt)
	r.CreatedAt = parseTimeStr(createdAt)
	r.UpdatedAt = parseTimeStr(updatedAt)
	return &r, nil
}

func (s *SQLiteStore) UpsertRawFile(ctx context.Context, rec *protocol.RawFileRecord) error {
	now := time.Now().UTC()
	if rec.ID == "" {
		rec.ID = uuid.NewString()
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	if rec.ParseStatus == "" {
		rec.ParseStatus = "pending"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO raw_files (id, device_id, source, rel_path, file_kind, archive_path, byte_size, sha256, header_sha256, parse_status, parse_error, parsed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id, source, rel_path) DO UPDATE SET
			file_kind = excluded.file_kind,
			archive_path = excluded.archive_path,
			byte_size = excluded.byte_size,
			sha256 = excluded.sha256,
			header_sha256 = excluded.header_sha256,
			parse_status = excluded.parse_status,
			parse_error = excluded.parse_error,
			updated_at = excluded.updated_at
	`, rec.ID, rec.DeviceID, rec.Source, rec.RelPath, rec.FileKind, rec.ArchivePath,
		rec.ByteSize, rec.SHA256, rec.HeaderSHA256, rec.ParseStatus, rec.ParseError,
		formatTimePtr(rec.ParsedAt), formatTime(rec.CreatedAt), formatTime(rec.UpdatedAt))
	if err != nil {
		return err
	}
	// Ensure rec.ID matches the existing row's ID on conflict
	existing, err := s.GetRawFileByKey(ctx, rec.DeviceID, rec.Source, rec.RelPath)
	if err == nil {
		rec.ID = existing.ID
	}
	return nil
}

func (s *SQLiteStore) MarkRawFileParseError(ctx context.Context, fileID, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE raw_files SET parse_status = 'error', parse_error = ?, updated_at = ? WHERE id = ?
	`, errMsg, formatTime(time.Now().UTC()), fileID)
	return err
}

func (s *SQLiteStore) ListRawFiles(ctx context.Context, deviceID, source, status string, limit, offset int) ([]protocol.RawFileRecord, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var where []string
	var args []any
	if deviceID != "" {
		where = append(where, "(rf.device_id = ? OR d.name = ?)")
		args = append(args, deviceID, deviceID)
	}
	if source != "" {
		where = append(where, "rf.source = ?")
		args = append(args, source)
	}
	if status != "" {
		where = append(where, "rf.parse_status = ?")
		args = append(args, status)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM raw_files rf JOIN devices d ON d.id = rf.device_id`+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	qArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.QueryContext(ctx, `
		SELECT rf.id, rf.device_id, d.name, rf.source, rf.rel_path, rf.file_kind, rf.archive_path,
		       rf.byte_size, rf.sha256, rf.header_sha256, rf.parse_status, rf.parse_error,
		       rf.parsed_at, rf.created_at, rf.updated_at
		FROM raw_files rf
		JOIN devices d ON d.id = rf.device_id`+whereSQL+`
		ORDER BY rf.updated_at DESC
		LIMIT ? OFFSET ?
	`, qArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []protocol.RawFileRecord
	for rows.Next() {
		var r protocol.RawFileRecord
		var parsedAt sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(
			&r.ID, &r.DeviceID, &r.DeviceName, &r.Source, &r.RelPath, &r.FileKind, &r.ArchivePath,
			&r.ByteSize, &r.SHA256, &r.HeaderSHA256, &r.ParseStatus, &r.ParseError,
			&parsedAt, &createdAt, &updatedAt,
		); err != nil {
			return nil, 0, err
		}
		r.ParsedAt = parseNullTime(parsedAt)
		r.CreatedAt = parseTimeStr(createdAt)
		r.UpdatedAt = parseTimeStr(updatedAt)
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func (s *SQLiteStore) ListAllRawFiles(ctx context.Context) ([]protocol.RawFileRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, device_id, source, rel_path, file_kind, archive_path, byte_size, sha256, header_sha256,
		       parse_status, parse_error, parsed_at, created_at, updated_at
		FROM raw_files
		ORDER BY CASE WHEN source = 't3' THEN 0 ELSE 1 END, updated_at ASC, created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.RawFileRecord
	for rows.Next() {
		var r protocol.RawFileRecord
		var parsedAt sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(
			&r.ID, &r.DeviceID, &r.Source, &r.RelPath, &r.FileKind, &r.ArchivePath,
			&r.ByteSize, &r.SHA256, &r.HeaderSHA256, &r.ParseStatus, &r.ParseError,
			&parsedAt, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		r.ParsedAt = parseNullTime(parsedAt)
		r.CreatedAt = parseTimeStr(createdAt)
		r.UpdatedAt = parseTimeStr(updatedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- Replace Parsed Data ---

func (s *SQLiteStore) ReplaceFileParsedData(ctx context.Context, fileID string, bundle *ParsedFileBundle) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	nowStr := formatTime(now)

	// Existing sessions can come from this file or have the same IDs in another
	// file (for example, after a log is compressed). Batch both sets so FTS does
	// one scan even when a database snapshot contains hundreds of sessions.
	sessionIDs := make([]string, 0, len(bundle.Sessions))
	for _, sess := range bundle.Sessions {
		sessionIDs = append(sessionIDs, sess.ID)
	}
	sessionIDsJSON, err := json.Marshal(sessionIDs)
	if err != nil {
		return fmt.Errorf("encode replacement session IDs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM messages_fts WHERE session_id IN (
			SELECT id FROM sessions WHERE source_file_id = ?
			UNION ALL
			SELECT value FROM json_each(?)
		)
	`, fileID, string(sessionIDsJSON)); err != nil {
		return fmt.Errorf("clear fts: %w", err)
	}

	// Cascading delete removes old messages, tool_calls, usage_events, rate_limit_snapshots
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM sessions WHERE source_file_id = ? OR id IN (SELECT value FROM json_each(?))
	`, fileID, string(sessionIDsJSON)); err != nil {
		return fmt.Errorf("clear sessions: %w", err)
	}

	// Upsert T3 thread links if any
	if len(bundle.T3Links) > 0 {
		stmtLink, err := tx.PrepareContext(ctx, `
			INSERT INTO t3_thread_links (thread_id, native_session_id, device_id, provider, project_id, project_title, workspace_root, thread_title, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(device_id, thread_id, native_session_id) DO UPDATE SET
				provider = excluded.provider,
				project_id = excluded.project_id,
				project_title = excluded.project_title,
				workspace_root = excluded.workspace_root,
				thread_title = excluded.thread_title,
				updated_at = excluded.updated_at
		`)
		if err != nil {
			return err
		}
		for _, l := range bundle.T3Links {
			if _, err := stmtLink.ExecContext(ctx, l.ThreadID, l.NativeSessionID, l.DeviceID, l.Provider, l.ProjectID, l.ProjectTitle, l.WorkspaceRoot, l.ThreadTitle, formatTime(l.UpdatedAt)); err != nil {
				stmtLink.Close()
				return fmt.Errorf("upsert t3 link: %w", err)
			}
		}
		stmtLink.Close()

		// Backfill already-ingested native sessions with richer T3 titles & projects
		if _, err := tx.ExecContext(ctx, `
			UPDATE sessions
			SET
				title = COALESCE(NULLIF((SELECT tl.thread_title FROM t3_thread_links tl WHERE tl.native_session_id = sessions.native_id AND tl.device_id = sessions.device_id LIMIT 1), ''), title),
				project_name = COALESCE(NULLIF((SELECT tl.project_title FROM t3_thread_links tl WHERE tl.native_session_id = sessions.native_id AND tl.device_id = sessions.device_id LIMIT 1), ''), project_name),
				project_path = CASE WHEN project_path = '' THEN COALESCE((SELECT tl.workspace_root FROM t3_thread_links tl WHERE tl.native_session_id = sessions.native_id AND tl.device_id = sessions.device_id LIMIT 1), '') ELSE project_path END
			WHERE EXISTS (SELECT 1 FROM t3_thread_links tl WHERE tl.native_session_id = sessions.native_id AND tl.device_id = sessions.device_id)
		`); err != nil {
			return fmt.Errorf("enrich sessions from t3 links: %w", err)
		}
	}

	if len(bundle.Sessions) > 0 {
		stmtSess, err := tx.PrepareContext(ctx, `
			INSERT INTO sessions (
				id, native_id, device_id, harness, source_file_id,
				project_path, project_name, git_branch, title, model_primary,
				parent_session_id, is_sidechain, originator, started_at, ended_at,
				message_count, tool_call_count, api_call_count,
				input_tokens, cached_input_tokens, cache_write_5m_tokens, cache_write_1h_tokens,
				output_tokens, reasoning_tokens, estimated_cost_usd, reported_cost_usd,
				metadata_json, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				source_file_id = excluded.source_file_id,
				project_path = excluded.project_path,
				project_name = excluded.project_name,
				git_branch = excluded.git_branch,
				title = excluded.title,
				model_primary = excluded.model_primary,
				parent_session_id = excluded.parent_session_id,
				is_sidechain = excluded.is_sidechain,
				originator = excluded.originator,
				started_at = excluded.started_at,
				ended_at = excluded.ended_at,
				message_count = excluded.message_count,
				tool_call_count = excluded.tool_call_count,
				api_call_count = excluded.api_call_count,
				input_tokens = excluded.input_tokens,
				cached_input_tokens = excluded.cached_input_tokens,
				cache_write_5m_tokens = excluded.cache_write_5m_tokens,
				cache_write_1h_tokens = excluded.cache_write_1h_tokens,
				output_tokens = excluded.output_tokens,
				reasoning_tokens = excluded.reasoning_tokens,
				estimated_cost_usd = excluded.estimated_cost_usd,
				reported_cost_usd = excluded.reported_cost_usd,
				metadata_json = excluded.metadata_json,
				updated_at = excluded.updated_at
		`)
		if err != nil {
			return err
		}
		for _, sess := range bundle.Sessions {
			// Check T3 enrichment for this native session
			var t3Title, t3Proj, t3Root string
			_ = tx.QueryRowContext(ctx, `
				SELECT thread_title, project_title, workspace_root
				FROM t3_thread_links
				WHERE native_session_id = ? AND device_id = ?
				LIMIT 1
			`, sess.NativeID, sess.DeviceID).Scan(&t3Title, &t3Proj, &t3Root)
			if t3Title != "" && (sess.Title == "" || len(sess.Title) > 120) {
				sess.Title = t3Title
			}
			if t3Proj != "" {
				sess.ProjectName = t3Proj
			}
			if t3Root != "" && sess.ProjectPath == "" {
				sess.ProjectPath = t3Root
			}

			var repCost sql.NullFloat64
			if sess.ReportedCostUSD != nil {
				repCost = sql.NullFloat64{Float64: *sess.ReportedCostUSD, Valid: true}
			}
			started := sess.StartedAt
			if started.IsZero() {
				started = now
			}
			if _, err := stmtSess.ExecContext(ctx,
				sess.ID, sess.NativeID, sess.DeviceID, sess.Harness, fileID,
				sess.ProjectPath, sess.ProjectName, sess.GitBranch, sess.Title, sess.ModelPrimary,
				sess.ParentSessionID, boolToInt(sess.IsSidechain), sess.Originator,
				formatTime(started), formatTimePtr(sess.EndedAt),
				sess.MessageCount, sess.ToolCallCount, sess.APICallCount,
				sess.InputTokens, sess.CachedInputTokens, sess.CacheWrite5mTokens, sess.CacheWrite1hTokens,
				sess.OutputTokens, sess.ReasoningTokens, sess.EstimatedCostUSD, repCost,
				sess.MetadataJSON, nowStr,
			); err != nil {
				stmtSess.Close()
				return fmt.Errorf("insert session %s: %w", sess.ID, err)
			}
		}
		stmtSess.Close()
	}

	if len(bundle.Messages) > 0 {
		stmtMsg, err := tx.PrepareContext(ctx, `
			INSERT OR REPLACE INTO messages (
				id, session_id, ordinal, native_message_id, parent_message_id,
				role, model, content_text, thinking_text, content_json,
				stop_reason, timestamp, duration_ms, metadata_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return err
		}
		stmtFTS, err := tx.PrepareContext(ctx, `
			INSERT INTO messages_fts (message_id, session_id, content_text, thinking_text)
			VALUES (?, ?, ?, ?)
		`)
		if err != nil {
			stmtMsg.Close()
			return err
		}
		for _, m := range bundle.Messages {
			if _, err := stmtMsg.ExecContext(ctx,
				m.ID, m.SessionID, m.Ordinal, m.NativeMessageID, m.ParentMessageID,
				m.Role, m.Model, m.ContentText, m.ThinkingText, m.ContentJSON,
				m.StopReason, formatTimePtr(m.Timestamp), m.DurationMs, m.MetadataJSON,
			); err != nil {
				stmtMsg.Close()
				stmtFTS.Close()
				return fmt.Errorf("insert message %s: %w", m.ID, err)
			}
			if m.ContentText != "" || m.ThinkingText != "" {
				if _, err := stmtFTS.ExecContext(ctx, m.ID, m.SessionID, m.ContentText, m.ThinkingText); err != nil {
					stmtMsg.Close()
					stmtFTS.Close()
					return fmt.Errorf("insert fts %s: %w", m.ID, err)
				}
			}
		}
		stmtMsg.Close()
		stmtFTS.Close()
	}

	if len(bundle.ToolCalls) > 0 {
		stmtTC, err := tx.PrepareContext(ctx, `
			INSERT OR REPLACE INTO tool_calls (
				id, session_id, message_id, ordinal, call_id, tool_name,
				input_json, output_text, status, is_error, timestamp, duration_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return err
		}
		for _, tc := range bundle.ToolCalls {
			if _, err := stmtTC.ExecContext(ctx,
				tc.ID, tc.SessionID, tc.MessageID, tc.Ordinal, tc.CallID, tc.ToolName,
				tc.InputJSON, tc.OutputText, tc.Status, boolToInt(tc.IsError),
				formatTimePtr(tc.Timestamp), tc.DurationMs,
			); err != nil {
				stmtTC.Close()
				return fmt.Errorf("insert tool call %s: %w", tc.ID, err)
			}
		}
		stmtTC.Close()
	}

	if len(bundle.Usage) > 0 {
		stmtUsage, err := tx.PrepareContext(ctx, `
			INSERT OR REPLACE INTO usage_events (
				id, session_id, device_id, harness, model, request_id, timestamp,
				input_tokens, cached_input_tokens, cache_write_5m_tokens, cache_write_1h_tokens,
				output_tokens, reasoning_tokens, web_search_requests, web_fetch_requests,
				context_window, effort, service_tier, estimated_cost_usd, raw_usage_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return err
		}
		for _, u := range bundle.Usage {
			ts := u.Timestamp
			if ts.IsZero() {
				ts = now
			}
			if _, err := stmtUsage.ExecContext(ctx,
				u.ID, u.SessionID, u.DeviceID, u.Harness, u.Model, u.RequestID, formatTime(ts),
				u.InputTokens, u.CachedInputTokens, u.CacheWrite5mTokens, u.CacheWrite1hTokens,
				u.OutputTokens, u.ReasoningTokens, u.WebSearchRequests, u.WebFetchRequests,
				u.ContextWindow, u.Effort, u.ServiceTier, u.EstimatedCostUSD, u.RawUsageJSON,
			); err != nil {
				stmtUsage.Close()
				return fmt.Errorf("insert usage %s: %w", u.ID, err)
			}
		}
		stmtUsage.Close()
	}

	if len(bundle.RateLimits) > 0 {
		stmtRL, err := tx.PrepareContext(ctx, `
			INSERT OR REPLACE INTO rate_limit_snapshots (
				id, device_id, harness, session_id, timestamp, limit_id, plan_type,
				primary_used_pct, secondary_used_pct, raw_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return err
		}
		for _, rl := range bundle.RateLimits {
			var p1, p2 sql.NullFloat64
			if rl.PrimaryUsedPct != nil {
				p1 = sql.NullFloat64{Float64: *rl.PrimaryUsedPct, Valid: true}
			}
			if rl.SecondaryUsedPct != nil {
				p2 = sql.NullFloat64{Float64: *rl.SecondaryUsedPct, Valid: true}
			}
			if _, err := stmtRL.ExecContext(ctx,
				rl.ID, rl.DeviceID, rl.Harness, rl.SessionID, formatTime(rl.Timestamp),
				rl.LimitID, rl.PlanType, p1, p2, rl.RawJSON,
			); err != nil {
				stmtRL.Close()
				return fmt.Errorf("insert rate limit %s: %w", rl.ID, err)
			}
		}
		stmtRL.Close()
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE raw_files SET parse_status = 'parsed', parse_error = '', parsed_at = ?, updated_at = ?
		WHERE id = ?
	`, nowStr, nowStr, fileID); err != nil {
		return err
	}

	return tx.Commit()
}

// --- Analytics Queries ---

func buildUsageWhere(f QueryFilter) (string, []any) {
	var conds []string
	var args []any
	if f.From != nil {
		conds = append(conds, "u.timestamp >= ?")
		args = append(args, formatTime(*f.From))
	}
	if f.To != nil {
		conds = append(conds, "u.timestamp <= ?")
		args = append(args, formatTime(*f.To))
	}
	if f.Device != "" {
		conds = append(conds, "(u.device_id = ? OR d.name = ?)")
		args = append(args, f.Device, f.Device)
	}
	if f.Harness != "" {
		conds = append(conds, "u.harness = ?")
		args = append(args, f.Harness)
	}
	if f.Model != "" {
		conds = append(conds, "u.model = ?")
		args = append(args, f.Model)
	}
	if f.Project != "" {
		conds = append(conds, "s.project_name = ?")
		args = append(args, f.Project)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func buildSessionWhere(f QueryFilter) (string, []any) {
	var conds []string
	var args []any
	if f.From != nil {
		// Match sessions active at or after f.From (based on last activity time, falling back to started_at).
		conds = append(conds, "COALESCE(s.ended_at, s.started_at) >= ?")
		args = append(args, formatTime(*f.From))
	}
	if f.To != nil {
		conds = append(conds, "s.started_at <= ?")
		args = append(args, formatTime(*f.To))
	}
	if f.Device != "" {
		conds = append(conds, "(s.device_id = ? OR d.name = ?)")
		args = append(args, f.Device, f.Device)
	}
	if f.Harness != "" {
		conds = append(conds, "s.harness = ?")
		args = append(args, f.Harness)
	}
	if f.Model != "" {
		conds = append(conds, "s.model_primary = ?")
		args = append(args, f.Model)
	}
	if f.Project != "" {
		conds = append(conds, "s.project_name = ?")
		args = append(args, f.Project)
	}
	if f.Query != "" {
		like := "%" + f.Query + "%"
		conds = append(conds, "(s.title LIKE ? OR s.project_name LIKE ? OR s.project_path LIKE ? OR s.native_id LIKE ? OR s.model_primary LIKE ?)")
		args = append(args, like, like, like, like, like)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func calcCacheHitRate(input, cached, cw5, cw1 int64) float64 {
	totalIn := input + cached + cw5 + cw1
	if totalIn <= 0 {
		return 0
	}
	return float64(cached) / float64(totalIn)
}

// Zed stores one usage total per thread, with no recorded API call count.
const usageCallSource = "NULLIF(u.harness, '" + protocol.SourceZed + "')"
const usageCallCount = "COUNT(" + usageCallSource + ")"

func (s *SQLiteStore) GetOverview(ctx context.Context, f QueryFilter) (*protocol.OverviewResponse, error) {
	whereU, argsU := buildUsageWhere(f)
	var agg protocol.UsageAggregate

	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(DISTINCT u.session_id),
			`+usageCallCount+`,
			COALESCE(SUM(u.input_tokens), 0),
			COALESCE(SUM(u.cached_input_tokens), 0),
			COALESCE(SUM(u.cache_write_5m_tokens), 0),
			COALESCE(SUM(u.cache_write_1h_tokens), 0),
			COALESCE(SUM(u.output_tokens), 0),
			COALESCE(SUM(u.reasoning_tokens), 0),
			COALESCE(SUM(u.estimated_cost_usd), 0)
		FROM usage_events u
		JOIN sessions s ON s.id = u.session_id
		JOIN devices d ON d.id = u.device_id
	`+whereU, argsU...).Scan(
		&agg.SessionCount, &agg.APICallCount,
		&agg.InputTokens, &agg.CachedInputTokens, &agg.CacheWrite5mTokens, &agg.CacheWrite1hTokens,
		&agg.OutputTokens, &agg.ReasoningTokens, &agg.EstimatedCostUSD,
	)
	if err != nil {
		return nil, err
	}

	whereS, argsS := buildSessionWhere(f)
	var sessCountAll int64
	err = s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(s.message_count), 0),
			COALESCE(SUM(s.tool_call_count), 0),
			COALESCE(SUM(COALESCE(s.reported_cost_usd, s.estimated_cost_usd)), 0)
		FROM sessions s
		JOIN devices d ON d.id = s.device_id
	`+whereS, argsS...).Scan(&sessCountAll, &agg.MessageCount, &agg.ToolCallCount, &agg.ReportedCostUSD)
	if err != nil {
		return nil, err
	}
	if sessCountAll > agg.SessionCount {
		agg.SessionCount = sessCountAll
	}
	agg.TotalTokens = agg.InputTokens + agg.CachedInputTokens + agg.CacheWrite5mTokens + agg.CacheWrite1hTokens + agg.OutputTokens
	agg.CacheHitRate = calcCacheHitRate(agg.InputTokens, agg.CachedInputTokens, agg.CacheWrite5mTokens, agg.CacheWrite1hTokens)

	var arch protocol.ArchiveSummary
	var lastFileUpd sql.NullString
	_ = s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(byte_size), 0),
			COALESCE(SUM(CASE WHEN parse_status = 'parsed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN parse_status = 'error' THEN 1 ELSE 0 END), 0),
			MAX(updated_at)
		FROM raw_files
	`).Scan(&arch.TotalFiles, &arch.TotalBytes, &arch.ParsedFiles, &arch.ErrorFiles, &lastFileUpd)

	cutoff := formatTime(time.Now().UTC().Add(-24 * time.Hour))
	var lastDevSeen sql.NullString
	_ = s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN last_seen_at >= ? AND revoked_at IS NULL THEN 1 ELSE 0 END), 0),
			MAX(last_seen_at)
		FROM devices
	`, cutoff).Scan(&arch.TotalDevices, &arch.ActiveDevices, &lastDevSeen)
	arch.LastUpdatedAt = latestTime(parseNullTime(lastFileUpd), parseNullTime(lastDevSeen))

	byHarness, err := s.GetBreakdown(ctx, f, "harness")
	if err != nil {
		return nil, err
	}
	byModel, err := s.GetBreakdown(ctx, f, "model")
	if err != nil {
		return nil, err
	}
	byDevice, err := s.GetBreakdown(ctx, f, "device")
	if err != nil {
		return nil, err
	}
	byProject, err := s.GetBreakdown(ctx, f, "project")
	if err != nil {
		return nil, err
	}
	if len(byModel) > 12 {
		byModel = byModel[:12]
	}
	if len(byProject) > 12 {
		byProject = byProject[:12]
	}

	return &protocol.OverviewResponse{
		Totals:    agg,
		Archive:   arch,
		ByHarness: byHarness,
		ByModel:   byModel,
		ByDevice:  byDevice,
		ByProject: byProject,
	}, nil
}

func (s *SQLiteStore) GetBreakdown(ctx context.Context, f QueryFilter, by string) ([]protocol.BreakdownRow, error) {
	var keyExpr, labelExpr string
	var includeSessions bool
	switch by {
	case "model":
		keyExpr, labelExpr = "COALESCE(NULLIF(u.model, ''), 'unknown')", "COALESCE(NULLIF(u.model, ''), 'unknown')"
	case "device":
		keyExpr, labelExpr = "u.device_id", "d.name"
	case "project":
		keyExpr, labelExpr = "COALESCE(NULLIF(s.project_name, ''), '(no project)')", "COALESCE(NULLIF(s.project_name, ''), '(no project)')"
	default:
		keyExpr, labelExpr = "u.harness", "u.harness"
		includeSessions = true
	}

	whereU, argsU := buildUsageWhere(f)
	records := fmt.Sprintf(`
		SELECT %s AS k, %s AS lbl, u.session_id, %s AS call_source,
		       u.input_tokens, u.cached_input_tokens, u.cache_write_5m_tokens,
		       u.cache_write_1h_tokens, u.output_tokens, u.reasoning_tokens, u.estimated_cost_usd
		FROM usage_events u
		JOIN sessions s ON s.id = u.session_id
		JOIN devices d ON d.id = u.device_id
		%s
	`, keyExpr, labelExpr, usageCallSource, whereU)
	args := argsU
	if includeSessions {
		// Count conversations even when their logs have no token usage records.
		// DISTINCT session IDs below avoid counting a session twice.
		whereS, argsS := buildSessionWhere(f)
		records += `
			UNION ALL
			SELECT s.harness, s.harness, s.id, NULL, 0, 0, 0, 0, 0, 0, 0
			FROM sessions s
			JOIN devices d ON d.id = s.device_id
		` + whereS
		args = append(args, argsS...)
	}
	q := `
		SELECT
			k, lbl,
			COUNT(DISTINCT session_id) AS session_count,
			COUNT(call_source) AS api_call_count,
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(cached_input_tokens), 0),
			COALESCE(SUM(cache_write_5m_tokens), 0),
			COALESCE(SUM(cache_write_1h_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(reasoning_tokens), 0),
			COALESCE(SUM(estimated_cost_usd), 0) AS est_cost
		FROM (` + records + `)
		GROUP BY k, lbl
		ORDER BY est_cost DESC, (COALESCE(SUM(input_tokens), 0) + COALESCE(SUM(cached_input_tokens), 0) + COALESCE(SUM(output_tokens), 0)) DESC
	`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.BreakdownRow
	var totalCost float64
	for rows.Next() {
		var r protocol.BreakdownRow
		if err := rows.Scan(
			&r.Key, &r.Label, &r.SessionCount, &r.APICallCount,
			&r.InputTokens, &r.CachedInputTokens, &r.CacheWrite5mTokens, &r.CacheWrite1hTokens,
			&r.OutputTokens, &r.ReasoningTokens, &r.EstimatedCostUSD,
		); err != nil {
			return nil, err
		}
		r.TotalTokens = r.InputTokens + r.CachedInputTokens + r.CacheWrite5mTokens + r.CacheWrite1hTokens + r.OutputTokens
		r.CacheHitRate = calcCacheHitRate(r.InputTokens, r.CachedInputTokens, r.CacheWrite5mTokens, r.CacheWrite1hTokens)
		totalCost += r.EstimatedCostUSD
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if totalCost > 0 {
		for i := range out {
			out[i].CostShare = out[i].EstimatedCostUSD / totalCost
		}
	}
	return out, nil
}

func (s *SQLiteStore) GetTimeseries(ctx context.Context, f QueryFilter, bucket, groupBy string, tzOffsetMin int) ([]protocol.TimeseriesPoint, error) {
	// Clamp to real-world offsets (UTC-12:00 .. UTC+14:00).
	if tzOffsetMin < -12*60 || tzOffsetMin > 14*60 {
		tzOffsetMin = 0
	}
	// The offset is an integer we formatted ourselves, so it is safe to inline.
	local := fmt.Sprintf("u.timestamp, '%+d minutes'", tzOffsetMin)
	var bucketExpr string
	switch bucket {
	case "hour":
		// Hour buckets are absolute instants; the client renders them in local time.
		bucketExpr = "strftime('%Y-%m-%dT%H:00:00Z', u.timestamp)"
	case "week":
		// Key each week by the local date of its Monday ('weekday 0' moves to the
		// next Sunday, or stays on a Sunday; minus 6 days lands on Monday).
		bucketExpr = "date(" + local + ", 'weekday 0', '-6 days')"
	case "month":
		bucketExpr = "strftime('%Y-%m', " + local + ")"
	default:
		bucketExpr = "date(" + local + ")"
	}

	var groupExpr string
	switch groupBy {
	case "harness":
		groupExpr = "u.harness"
	case "model":
		groupExpr = "COALESCE(NULLIF(u.model, ''), 'unknown')"
	case "device":
		groupExpr = "d.name"
	case "project":
		groupExpr = "COALESCE(NULLIF(s.project_name, ''), '(no project)')"
	default:
		groupExpr = "''"
	}

	whereU, argsU := buildUsageWhere(f)
	q := fmt.Sprintf(`
		SELECT
			%s AS b,
			%s AS g,
			%s AS api_call_count,
			COUNT(DISTINCT u.session_id) AS session_count,
			COALESCE(SUM(u.input_tokens), 0),
			COALESCE(SUM(u.cached_input_tokens), 0),
			COALESCE(SUM(u.cache_write_5m_tokens), 0),
			COALESCE(SUM(u.cache_write_1h_tokens), 0),
			COALESCE(SUM(u.output_tokens), 0),
			COALESCE(SUM(u.reasoning_tokens), 0),
			COALESCE(SUM(u.estimated_cost_usd), 0)
		FROM usage_events u
		JOIN sessions s ON s.id = u.session_id
		JOIN devices d ON d.id = u.device_id
		%s
		GROUP BY b, g
		ORDER BY b ASC, g ASC
	`, bucketExpr, groupExpr, usageCallCount, whereU)

	rows, err := s.db.QueryContext(ctx, q, argsU...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.TimeseriesPoint
	for rows.Next() {
		var p protocol.TimeseriesPoint
		if err := rows.Scan(
			&p.Bucket, &p.Group, &p.APICallCount, &p.SessionCount,
			&p.InputTokens, &p.CachedInputTokens, &p.CacheWrite5mTokens, &p.CacheWrite1hTokens,
			&p.OutputTokens, &p.ReasoningTokens, &p.EstimatedCostUSD,
		); err != nil {
			return nil, err
		}
		p.TotalTokens = p.InputTokens + p.CachedInputTokens + p.CacheWrite5mTokens + p.CacheWrite1hTokens + p.OutputTokens
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListSessions(ctx context.Context, params SessionListParams) ([]protocol.SessionSummary, int64, error) {
	limit := params.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := params.Offset
	if offset < 0 {
		offset = 0
	}

	whereS, argsS := buildSessionWhere(params.Filter)

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions s JOIN devices d ON d.id = s.device_id`+whereS, argsS...).Scan(&total); err != nil {
		return nil, 0, err
	}

	orderCol := "COALESCE(s.ended_at, s.started_at)"
	switch params.Sort {
	case "started_at":
		orderCol = "s.started_at"
	case "ended_at", "updated_at", "last_active":
		orderCol = "COALESCE(s.ended_at, s.started_at)"
	case "cost":
		orderCol = "s.estimated_cost_usd"
	case "tokens":
		orderCol = "(s.input_tokens + s.cached_input_tokens + s.cache_write_5m_tokens + s.cache_write_1h_tokens + s.output_tokens)"
	case "messages":
		orderCol = "s.message_count"
	}
	dir := "DESC"
	if strings.EqualFold(params.Order, "asc") {
		dir = "ASC"
	}

	qArgs := append(append([]any{}, argsS...), limit, offset)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT
			s.id, s.native_id, s.device_id, d.name, s.harness, s.source_file_id,
			s.project_path, s.project_name, s.git_branch, s.title, s.model_primary,
			s.parent_session_id, s.is_sidechain, s.originator, s.started_at, s.ended_at,
			s.message_count, s.tool_call_count, s.api_call_count,
			s.input_tokens, s.cached_input_tokens, s.cache_write_5m_tokens, s.cache_write_1h_tokens,
			s.output_tokens, s.reasoning_tokens, s.estimated_cost_usd, s.reported_cost_usd,
			s.metadata_json, s.updated_at
		FROM sessions s
		JOIN devices d ON d.id = s.device_id
		%s
		ORDER BY %s %s
		LIMIT ? OFFSET ?
	`, whereS, orderCol, dir), qArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []protocol.SessionSummary
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *sess)
	}
	return out, total, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(row rowScanner) (*protocol.SessionSummary, error) {
	var s protocol.SessionSummary
	var isSidechain int
	var startedAt, updatedAt string
	var endedAt sql.NullString
	var repCost sql.NullFloat64
	if err := row.Scan(
		&s.ID, &s.NativeID, &s.DeviceID, &s.DeviceName, &s.Harness, &s.SourceFileID,
		&s.ProjectPath, &s.ProjectName, &s.GitBranch, &s.Title, &s.ModelPrimary,
		&s.ParentSessionID, &isSidechain, &s.Originator, &startedAt, &endedAt,
		&s.MessageCount, &s.ToolCallCount, &s.APICallCount,
		&s.InputTokens, &s.CachedInputTokens, &s.CacheWrite5mTokens, &s.CacheWrite1hTokens,
		&s.OutputTokens, &s.ReasoningTokens, &s.EstimatedCostUSD, &repCost,
		&s.MetadataJSON, &updatedAt,
	); err != nil {
		return nil, err
	}
	s.IsSidechain = isSidechain != 0
	s.StartedAt = parseTimeStr(startedAt)
	s.EndedAt = parseNullTime(endedAt)
	s.UpdatedAt = parseTimeStr(updatedAt)
	s.TotalTokens = s.InputTokens + s.CachedInputTokens + s.CacheWrite5mTokens + s.CacheWrite1hTokens + s.OutputTokens
	if repCost.Valid {
		v := repCost.Float64
		s.ReportedCostUSD = &v
	}
	return &s, nil
}

func (s *SQLiteStore) GetSessionDetail(ctx context.Context, sessionID string) (*protocol.SessionDetail, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT
			s.id, s.native_id, s.device_id, d.name, s.harness, s.source_file_id,
			s.project_path, s.project_name, s.git_branch, s.title, s.model_primary,
			s.parent_session_id, s.is_sidechain, s.originator, s.started_at, s.ended_at,
			s.message_count, s.tool_call_count, s.api_call_count,
			s.input_tokens, s.cached_input_tokens, s.cache_write_5m_tokens, s.cache_write_1h_tokens,
			s.output_tokens, s.reasoning_tokens, s.estimated_cost_usd, s.reported_cost_usd,
			s.metadata_json, s.updated_at
		FROM sessions s
		JOIN devices d ON d.id = s.device_id
		WHERE s.id = ?
	`, sessionID)
	sess, err := scanSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	detail := &protocol.SessionDetail{
		Session:   *sess,
		Messages:  []protocol.MessageRecord{},
		ToolCalls: []protocol.ToolCallRecord{},
		Usage:     []protocol.UsageEventRecord{},
	}

	mRows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, ordinal, native_message_id, parent_message_id,
		       role, model, content_text, thinking_text, content_json,
		       stop_reason, timestamp, duration_ms, metadata_json
		FROM messages WHERE session_id = ? ORDER BY ordinal ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer mRows.Close()
	for mRows.Next() {
		var m protocol.MessageRecord
		var ts sql.NullString
		if err := mRows.Scan(
			&m.ID, &m.SessionID, &m.Ordinal, &m.NativeMessageID, &m.ParentMessageID,
			&m.Role, &m.Model, &m.ContentText, &m.ThinkingText, &m.ContentJSON,
			&m.StopReason, &ts, &m.DurationMs, &m.MetadataJSON,
		); err != nil {
			return nil, err
		}
		m.Timestamp = parseNullTime(ts)
		detail.Messages = append(detail.Messages, m)
	}

	tcRows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, message_id, ordinal, call_id, tool_name,
		       input_json, output_text, status, is_error, timestamp, duration_ms
		FROM tool_calls WHERE session_id = ? ORDER BY ordinal ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer tcRows.Close()
	for tcRows.Next() {
		var tc protocol.ToolCallRecord
		var isErr int
		var ts sql.NullString
		if err := tcRows.Scan(
			&tc.ID, &tc.SessionID, &tc.MessageID, &tc.Ordinal, &tc.CallID, &tc.ToolName,
			&tc.InputJSON, &tc.OutputText, &tc.Status, &isErr, &ts, &tc.DurationMs,
		); err != nil {
			return nil, err
		}
		tc.IsError = isErr != 0
		tc.Timestamp = parseNullTime(ts)
		detail.ToolCalls = append(detail.ToolCalls, tc)
	}

	uRows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, device_id, harness, model, request_id, timestamp,
		       input_tokens, cached_input_tokens, cache_write_5m_tokens, cache_write_1h_tokens,
		       output_tokens, reasoning_tokens, web_search_requests, web_fetch_requests,
		       context_window, effort, service_tier, estimated_cost_usd, raw_usage_json
		FROM usage_events WHERE session_id = ? ORDER BY timestamp ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer uRows.Close()
	for uRows.Next() {
		var u protocol.UsageEventRecord
		var ts string
		if err := uRows.Scan(
			&u.ID, &u.SessionID, &u.DeviceID, &u.Harness, &u.Model, &u.RequestID, &ts,
			&u.InputTokens, &u.CachedInputTokens, &u.CacheWrite5mTokens, &u.CacheWrite1hTokens,
			&u.OutputTokens, &u.ReasoningTokens, &u.WebSearchRequests, &u.WebFetchRequests,
			&u.ContextWindow, &u.Effort, &u.ServiceTier, &u.EstimatedCostUSD, &u.RawUsageJSON,
		); err != nil {
			return nil, err
		}
		u.Timestamp = parseTimeStr(ts)
		detail.Usage = append(detail.Usage, u)
	}

	rlRows, err := s.db.QueryContext(ctx, `
		SELECT id, device_id, harness, session_id, timestamp, limit_id, plan_type,
		       primary_used_pct, secondary_used_pct, raw_json
		FROM rate_limit_snapshots WHERE session_id = ? ORDER BY timestamp ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rlRows.Close()
	for rlRows.Next() {
		var rl protocol.RateLimitSnapshot
		var ts string
		var p1, p2 sql.NullFloat64
		if err := rlRows.Scan(
			&rl.ID, &rl.DeviceID, &rl.Harness, &rl.SessionID, &ts, &rl.LimitID, &rl.PlanType,
			&p1, &p2, &rl.RawJSON,
		); err != nil {
			return nil, err
		}
		rl.Timestamp = parseTimeStr(ts)
		if p1.Valid {
			v := p1.Float64
			rl.PrimaryUsedPct = &v
		}
		if p2.Valid {
			v := p2.Float64
			rl.SecondaryUsedPct = &v
		}
		detail.RateLimits = append(detail.RateLimits, rl)
	}

	return detail, nil
}

func (s *SQLiteStore) SearchMessages(ctx context.Context, f QueryFilter, limit, offset int) ([]protocol.SearchHit, error) {
	if strings.TrimSpace(f.Query) == "" {
		return []protocol.SearchHit{}, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	// Quote user query safely for FTS5 if it contains syntax chars
	ftsQuery := sanitizeFTSQuery(f.Query)
	var conds []string
	var args []any
	conds = append(conds, "messages_fts MATCH ?")
	args = append(args, ftsQuery)

	if f.From != nil {
		conds = append(conds, "COALESCE(m.timestamp, s.started_at) >= ?")
		args = append(args, formatTime(*f.From))
	}
	if f.To != nil {
		conds = append(conds, "COALESCE(m.timestamp, s.started_at) <= ?")
		args = append(args, formatTime(*f.To))
	}
	if f.Harness != "" {
		conds = append(conds, "s.harness = ?")
		args = append(args, f.Harness)
	}
	if f.Model != "" {
		conds = append(conds, "COALESCE(NULLIF(m.model, ''), s.model_primary) = ?")
		args = append(args, f.Model)
	}
	if f.Device != "" {
		conds = append(conds, "(s.device_id = ? OR d.name = ?)")
		args = append(args, f.Device, f.Device)
	}
	if f.Project != "" {
		conds = append(conds, "s.project_name = ?")
		args = append(args, f.Project)
	}
	args = append(args, limit, offset)

	q := `
		SELECT
			m.id, m.session_id, s.title, s.harness, s.project_name, d.name,
			COALESCE(NULLIF(m.model, ''), s.model_primary), m.role,
			snippet(messages_fts, -1, '<mark>', '</mark>', '…', 24),
			m.timestamp
		FROM messages_fts
		JOIN messages m ON m.id = messages_fts.message_id
		JOIN sessions s ON s.id = m.session_id
		JOIN devices d ON d.id = s.device_id
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY rank
		LIMIT ? OFFSET ?
	`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.SearchHit
	for rows.Next() {
		var h protocol.SearchHit
		var ts sql.NullString
		if err := rows.Scan(
			&h.MessageID, &h.SessionID, &h.SessionTitle, &h.Harness, &h.ProjectName,
			&h.DeviceName, &h.Model, &h.Role, &h.Snippet, &ts,
		); err != nil {
			return nil, err
		}
		h.Timestamp = parseNullTime(ts)
		out = append(out, h)
	}
	return out, rows.Err()
}

func sanitizeFTSQuery(q string) string {
	// Wrap each whitespace-separated term in double quotes so punctuation like '-', ':', '/' doesn't break FTS5 syntax.
	parts := strings.Fields(q)
	for i, p := range parts {
		p = strings.ReplaceAll(p, `"`, `""`)
		parts[i] = `"` + p + `"`
	}
	return strings.Join(parts, " ")
}

func (s *SQLiteStore) GetFilterOptions(ctx context.Context) (*protocol.FilterOptions, error) {
	opts := &protocol.FilterOptions{
		Harnesses: []string{},
		Models:    []string{},
		Devices:   []protocol.FilterDeviceOpt{},
		Projects:  []string{},
	}

	if rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT harness FROM sessions WHERE harness != '' ORDER BY harness`); err == nil {
		for rows.Next() {
			var v string
			if rows.Scan(&v) == nil {
				opts.Harnesses = append(opts.Harnesses, v)
			}
		}
		rows.Close()
	}
	if rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT model FROM usage_events WHERE model != '' ORDER BY model`); err == nil {
		for rows.Next() {
			var v string
			if rows.Scan(&v) == nil {
				opts.Models = append(opts.Models, v)
			}
		}
		rows.Close()
	}
	if rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM devices WHERE revoked_at IS NULL ORDER BY name`); err == nil {
		for rows.Next() {
			var d protocol.FilterDeviceOpt
			if rows.Scan(&d.ID, &d.Name) == nil {
				opts.Devices = append(opts.Devices, d)
			}
		}
		rows.Close()
	}
	if rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT project_name FROM sessions WHERE project_name != '' ORDER BY project_name`); err == nil {
		for rows.Next() {
			var v string
			if rows.Scan(&v) == nil {
				opts.Projects = append(opts.Projects, v)
			}
		}
		rows.Close()
	}
	var lastFileUpd, lastDevSeen sql.NullString
	_ = s.db.QueryRowContext(ctx, `SELECT MAX(updated_at) FROM raw_files`).Scan(&lastFileUpd)
	_ = s.db.QueryRowContext(ctx, `SELECT MAX(last_seen_at) FROM devices`).Scan(&lastDevSeen)
	opts.LastUpdatedAt = latestTime(parseNullTime(lastFileUpd), parseNullTime(lastDevSeen))
	return opts, nil
}

func latestTime(a, b *time.Time) *time.Time {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if b.After(*a) {
		return b
	}
	return a
}

// --- Pricing Overrides & Cost Recalculation ---

func (s *SQLiteStore) ListPricingOverrides(ctx context.Context) ([]protocol.PricingOverrideRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT model, input_per_m, cached_input_per_m, cache_write_5m_per_m, cache_write_1h_per_m, output_per_m, notes, updated_at
		FROM pricing_overrides
		ORDER BY model ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.PricingOverrideRecord
	for rows.Next() {
		var r protocol.PricingOverrideRecord
		var upd string
		if err := rows.Scan(
			&r.Model, &r.InputPerM, &r.CachedInputPerM, &r.CacheWrite5mPerM, &r.CacheWrite1hPerM,
			&r.OutputPerM, &r.Notes, &upd,
		); err != nil {
			return nil, err
		}
		r.UpdatedAt = parseTimeStr(upd)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpsertPricingOverride(ctx context.Context, rec protocol.PricingOverrideRecord) (*protocol.PricingOverrideRecord, error) {
	model := strings.ToLower(strings.TrimSpace(rec.Model))
	if model == "" {
		return nil, fmt.Errorf("model is required")
	}
	now := time.Now().UTC()
	rec.Model = model
	rec.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pricing_overrides (
			model, input_per_m, cached_input_per_m, cache_write_5m_per_m, cache_write_1h_per_m,
			output_per_m, notes, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET
			input_per_m = excluded.input_per_m,
			cached_input_per_m = excluded.cached_input_per_m,
			cache_write_5m_per_m = excluded.cache_write_5m_per_m,
			cache_write_1h_per_m = excluded.cache_write_1h_per_m,
			output_per_m = excluded.output_per_m,
			notes = excluded.notes,
			updated_at = excluded.updated_at
	`, rec.Model, rec.InputPerM, rec.CachedInputPerM, rec.CacheWrite5mPerM, rec.CacheWrite1hPerM,
		rec.OutputPerM, strings.TrimSpace(rec.Notes), formatTime(now))
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *SQLiteStore) DeletePricingOverride(ctx context.Context, model string) error {
	model = strings.ToLower(strings.TrimSpace(model))
	res, err := s.db.ExecContext(ctx, `DELETE FROM pricing_overrides WHERE model = ?`, model)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) ListObservedModels(ctx context.Context) ([]protocol.ObservedModelUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			model,
			GROUP_CONCAT(DISTINCT harness) AS harnesses,
			`+usageCallCount+` AS api_calls,
			COALESCE(SUM(input_tokens + cached_input_tokens + cache_write_5m_tokens + cache_write_1h_tokens + output_tokens), 0) AS total_tokens,
			COALESCE(SUM(estimated_cost_usd), 0) AS cost
		FROM usage_events u
		WHERE model != ''
		GROUP BY model
		ORDER BY cost DESC, total_tokens DESC, model ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.ObservedModelUsage
	for rows.Next() {
		var u protocol.ObservedModelUsage
		var harnessesCSV sql.NullString
		if err := rows.Scan(&u.Model, &harnessesCSV, &u.APICallCount, &u.TotalTokens, &u.EstimatedCostUSD); err != nil {
			return nil, err
		}
		if harnessesCSV.Valid && harnessesCSV.String != "" {
			for _, h := range strings.Split(harnessesCSV.String, ",") {
				h = strings.TrimSpace(h)
				if h != "" {
					u.Harnesses = append(u.Harnesses, h)
				}
			}
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) RecalculateCosts(
	ctx context.Context,
	estimateFn func(harness, model string, input, cachedRead, cacheWrite5m, cacheWrite1h, output int64, currentCost float64) (float64, bool),
) (int64, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	type pendingUpdate struct {
		id      string
		newCost float64
	}
	var updates []pendingUpdate

	rows, err := tx.QueryContext(ctx, `
		SELECT id, harness, model, input_tokens, cached_input_tokens, cache_write_5m_tokens, cache_write_1h_tokens, output_tokens, estimated_cost_usd
		FROM usage_events
	`)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var (
			id, harness, model             string
			inT, crT, cw5T, cw1T, outT     int64
			curCost                        float64
		)
		if err := rows.Scan(&id, &harness, &model, &inT, &crT, &cw5T, &cw1T, &outT, &curCost); err != nil {
			rows.Close()
			return 0, 0, err
		}
		newCost, shouldUpdate := estimateFn(harness, model, inT, crT, cw5T, cw1T, outT, curCost)
		if shouldUpdate && newCost != curCost {
			updates = append(updates, pendingUpdate{id: id, newCost: newCost})
		}
	}
	if err := rows.Close(); err != nil {
		return 0, 0, err
	}

	var updatedEvents int64
	if len(updates) > 0 {
		stmt, err := tx.PrepareContext(ctx, `UPDATE usage_events SET estimated_cost_usd = ? WHERE id = ?`)
		if err != nil {
			return 0, 0, err
		}
		for _, u := range updates {
			if _, err := stmt.ExecContext(ctx, u.newCost, u.id); err != nil {
				stmt.Close()
				return 0, 0, err
			}
			updatedEvents++
		}
		stmt.Close()
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE sessions
		SET estimated_cost_usd = COALESCE(
			(SELECT SUM(u.estimated_cost_usd) FROM usage_events u WHERE u.session_id = sessions.id),
			estimated_cost_usd
		)
		WHERE id IN (SELECT DISTINCT session_id FROM usage_events)
	`)
	if err != nil {
		return 0, 0, err
	}
	updatedSessions, _ := res.RowsAffected()

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return updatedEvents, updatedSessions, nil
}
