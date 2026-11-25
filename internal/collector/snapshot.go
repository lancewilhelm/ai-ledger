package collector

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"
)

// t3MetadataTables lists T3 Code's unique metadata and orchestration tables.
// Redundant UI timeline projections (turn_items, nodes, events, messages, FTS)
// are omitted because full conversations, tool calls, and token usage are
// already captured losslessly from the native Claude/Codex/Antigravity logs.
var t3MetadataTables = []string{
	"projection_projects",
	"projection_threads",
	"orchestration_v2_projection_threads",
	"orchestration_v2_projection_provider_threads",
	"orchestration_v2_projection_provider_sessions",
	"orchestration_v2_projection_provider_session_bindings",
	"orchestration_v2_projection_provider_turns",
	"orchestration_v2_projection_runs",
	"orchestration_v2_projection_run_attempts",
	"orchestration_v2_projection_subagents",
	"orchestration_v2_projection_context_transfers",
	"orchestration_v2_projection_context_handoffs",
	"orchestration_v2_projection_plans",
	"orchestration_v2_projection_checkpoints",
	"orchestration_v2_projection_checkpoint_scopes",
	"orchestration_v2_projection_runtime_requests",
	"orchestration_v2_projection_metadata",
	"orchestration_v2_projection_thread_item_counts",
	"orchestration_v2_projection_turn_item_counts",
	"projection_thread_pull_requests",
	"pull_request_review_guides",
	"scheduled_tasks",
	"workflow_runs",
	"workflow_steps",
}

// CreateSQLiteSnapshot creates a consistent, WAL-safe snapshot of a live SQLite database
// in a temporary directory. Caller is responsible for removing filepath.Dir(returnedPath).
func CreateSQLiteSnapshot(ctx context.Context, srcDB string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "ai-ledger-snap-*")
	if err != nil {
		return "", err
	}
	dstPath := filepath.Join(tmpDir, filepath.Base(srcDB))

	// Prefer native sqlite3 CLI .backup when available (fastest & lowest memory for large DBs)
	if sqlite3Bin, err := exec.LookPath("sqlite3"); err == nil {
		cmd := exec.CommandContext(ctx, sqlite3Bin, srcDB, fmt.Sprintf(".backup '%s'", strings.ReplaceAll(dstPath, "'", "''")))
		if _, err := cmd.CombinedOutput(); err == nil {
			if st, serr := os.Stat(dstPath); serr == nil && st.Size() > 0 {
				return dstPath, nil
			}
		} else {
			_ = os.Remove(dstPath)
		}
	}

	// Fallback: pure-Go modernc.org/sqlite VACUUM INTO (WAL-safe, zero C deps)
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(10000)", srcDB)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", err
	}
	defer db.Close()

	escaped := strings.ReplaceAll(dstPath, "'", "''")
	if _, err := db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", escaped)); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("vacuum into snapshot: %w", err)
	}
	return dstPath, nil
}

// CreateT3MetadataSnapshot extracts only T3 Code's unique metadata and orchestration
// tables into a compact SQLite file (~1 MB vs ~1 GB for full statev2.sqlite).
// Caller is responsible for removing filepath.Dir(returnedPath).
func CreateT3MetadataSnapshot(ctx context.Context, srcDB string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "ai-ledger-t3meta-*")
	if err != nil {
		return "", err
	}
	dstPath := filepath.Join(tmpDir, "statev2-meta.sqlite")

	// Open the new destination DB directly, then ATTACH the live source read-only.
	dst, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(OFF)&_pragma=synchronous(OFF)", dstPath))
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", err
	}
	defer dst.Close()
	dst.SetMaxOpenConns(1)

	srcURI := fmt.Sprintf("file:%s?mode=ro", strings.ReplaceAll(srcDB, "'", "''"))
	if _, err := dst.ExecContext(ctx, fmt.Sprintf("ATTACH DATABASE '%s' AS src", srcURI)); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("attach source t3 db: %w", err)
	}
	defer func() {
		_, _ = dst.Exec("DETACH DATABASE src")
	}()

	existing := make(map[string]string)
	rows, err := dst.QueryContext(ctx, `SELECT name, sql FROM src.sqlite_master WHERE type = 'table' AND sql IS NOT NULL`)
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("list source t3 tables: %w", err)
	}
	for rows.Next() {
		var name, createSQL string
		if rows.Scan(&name, &createSQL) == nil {
			existing[name] = createSQL
		}
	}
	rows.Close()

	copied := 0
	for _, tbl := range t3MetadataTables {
		createSQL, ok := existing[tbl]
		if !ok {
			continue
		}
		if _, err := dst.ExecContext(ctx, createSQL); err != nil {
			os.RemoveAll(tmpDir)
			return "", fmt.Errorf("create table %s: %w", tbl, err)
		}
		quoted := `"` + strings.ReplaceAll(tbl, `"`, `""`) + `"`
		if _, err := dst.ExecContext(ctx, fmt.Sprintf("INSERT INTO main.%s SELECT * FROM src.%s", quoted, quoted)); err != nil {
			os.RemoveAll(tmpDir)
			return "", fmt.Errorf("copy table %s: %w", tbl, err)
		}
		copied++
	}
	if copied == 0 {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("no recognized T3 metadata tables found in %s", srcDB)
	}

	if err := dst.Close(); err != nil {
		os.RemoveAll(tmpDir)
		return "", fmt.Errorf("close t3 metadata snapshot: %w", err)
	}
	return dstPath, nil
}

// CompressFileZstd compresses srcPath to dstPath using deterministic zstd compression
// and returns the compressed byte size.
func CompressFileZstd(srcPath, dstPath string) (int64, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return 0, err
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}

	enc, err := zstd.NewWriter(dst, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	if err != nil {
		dst.Close()
		os.Remove(dstPath)
		return 0, err
	}

	if _, err := io.Copy(enc, src); err != nil {
		enc.Close()
		dst.Close()
		os.Remove(dstPath)
		return 0, err
	}
	if err := enc.Close(); err != nil {
		dst.Close()
		os.Remove(dstPath)
		return 0, err
	}
	if err := dst.Close(); err != nil {
		os.Remove(dstPath)
		return 0, err
	}

	st, err := os.Stat(dstPath)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}
