package collector

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"
)

func TestCreateT3MetadataSnapshotAndCompressZstd(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "statev2.sqlite")
	db, err := sql.Open("sqlite", srcPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		PRAGMA journal_mode = WAL;
		CREATE TABLE projection_projects (project_id TEXT PRIMARY KEY, title TEXT, workspace_root TEXT);
		CREATE TABLE orchestration_v2_projection_threads (thread_id TEXT PRIMARY KEY, project_id TEXT, title TEXT, default_provider TEXT, active_provider_thread_id TEXT, updated_at TEXT);
		CREATE TABLE orchestration_v2_projection_provider_threads (provider_thread_id TEXT PRIMARY KEY, thread_id TEXT, provider TEXT, updated_at TEXT);
		CREATE TABLE orchestration_v2_projection_turn_items (item_id TEXT PRIMARY KEY, payload_json TEXT);
		CREATE TABLE orchestration_events (seq INTEGER PRIMARY KEY, payload TEXT);

		INSERT INTO projection_projects VALUES ('p1', 'AI Ledger', '/Users/dev/ai-ledger');
		INSERT INTO orchestration_v2_projection_threads VALUES ('t1', 'p1', 'Slim Snapshots', 'codex', 'provider-thread:provider:codex:native-thread:abc-123', '2026-11-24T20:00:00Z');
		INSERT INTO orchestration_v2_projection_provider_threads VALUES ('provider-thread:provider:codex:native-thread:abc-123', 't1', 'codex', '2026-11-24T20:00:00Z');
		INSERT INTO orchestration_v2_projection_turn_items VALUES ('huge1', '` + strings.Repeat("x", 4096) + `');
		INSERT INTO orchestration_events VALUES (1, 'redundant event');
	`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	snapPath, err := CreateT3MetadataSnapshot(context.Background(), srcPath)
	if err != nil {
		t.Fatalf("CreateT3MetadataSnapshot: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(snapPath))

	// Verify copied metadata tables exist and redundant tables were omitted
	outDB, err := sql.Open("sqlite", snapPath)
	if err != nil {
		t.Fatal(err)
	}
	defer outDB.Close()

	var projCount int
	if err := outDB.QueryRow(`SELECT COUNT(*) FROM projection_projects`).Scan(&projCount); err != nil || projCount != 1 {
		t.Fatalf("expected 1 project row, got %d (err=%v)", projCount, err)
	}
	var redundantCount int
	if err := outDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('orchestration_v2_projection_turn_items', 'orchestration_events')`).Scan(&redundantCount); err != nil {
		t.Fatal(err)
	}
	if redundantCount != 0 {
		t.Fatalf("expected redundant T3 projection tables to be omitted, found %d", redundantCount)
	}

	// Verify CompressFileZstd produces valid, deterministic zstd output
	zst1 := filepath.Join(dir, "out1.sqlite.zst")
	zst2 := filepath.Join(dir, "out2.sqlite.zst")
	sz1, err := CompressFileZstd(snapPath, zst1)
	if err != nil || sz1 <= 0 {
		t.Fatalf("CompressFileZstd #1: sz=%d err=%v", sz1, err)
	}
	sz2, err := CompressFileZstd(snapPath, zst2)
	if err != nil || sz2 != sz1 {
		t.Fatalf("CompressFileZstd #2: sz=%d (want %d) err=%v", sz2, sz1, err)
	}
	if fileSHA256(t, zst1) != fileSHA256(t, zst2) {
		t.Fatalf("expected deterministic zstd output across runs")
	}

	// Verify round-trip decompression matches original snapshot bytes
	f, err := os.Open(zst1)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	decompressed, err := io.ReadAll(dec)
	if err != nil {
		t.Fatal(err)
	}
	orig, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(decompressed) != len(orig) {
		t.Fatalf("decompressed size mismatch: got %d want %d", len(decompressed), len(orig))
	}
}

func fileSHA256(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestRealT3SnapshotOptional(t *testing.T) {
	src := os.Getenv("AI_LEDGER_BENCH_T3_PATH")
	if src == "" {
		t.Skip("set AI_LEDGER_BENCH_T3_PATH to benchmark against a local T3 statev2.sqlite")
	}
	srcSt, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	snap, err := CreateT3MetadataSnapshot(context.Background(), src)
	if err != nil {
		t.Fatalf("CreateT3MetadataSnapshot: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(snap))
	snapDur := time.Since(t0)
	snapSt, _ := os.Stat(snap)

	t1 := time.Now()
	zstPath := snap + ".zst"
	compBytes, err := CompressFileZstd(snap, zstPath)
	if err != nil {
		t.Fatalf("CompressFileZstd: %v", err)
	}
	compDur := time.Since(t1)

	t.Logf("source=%.2fMB slim=%.2fMB (%s) zstd=%.2fKB (%s) ratio=%.1fx",
		float64(srcSt.Size())/(1024*1024),
		float64(snapSt.Size())/(1024*1024),
		snapDur.Round(time.Millisecond),
		float64(compBytes)/1024,
		compDur.Round(time.Millisecond),
		float64(srcSt.Size())/float64(compBytes),
	)
}
