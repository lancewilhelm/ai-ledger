package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestOpenSQLiteAppliesVersionedMigrations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ai-ledger.sqlite")

	s, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite fresh: %v", err)
	}
	res := s.MigrationResult()
	if res.Applied != 2 || res.CurrentVersion != 2 {
		t.Fatalf("expected 2 applied migrations at v2, got %+v", res)
	}
	if res.BackupPath != "" {
		t.Fatalf("expected no backup on fresh db, got %q", res.BackupPath)
	}
	_ = s.Close()

	// Re-opening should be a no-op.
	s2, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite reopen: %v", err)
	}
	defer s2.Close()
	if got := s2.MigrationResult(); got.Applied != 0 || got.CurrentVersion != 2 {
		t.Fatalf("expected 0 applied on reopen, got %+v", got)
	}
}

func TestMigrateUpgradesExistingDBWithBackupAndRollback(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ai-ledger.sqlite")
	s, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer s.Close()

	// Insert a device row so we can verify data survives upgrades and rollbacks.
	if _, err := s.CreateDevice(context.Background(), "macbook", "macbook.local", "darwin", "arm64", "hash-1"); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	v1SQL, err := embeddedMigrationsFS.ReadFile("migrations/0001_initial_schema.sql")
	if err != nil {
		t.Fatalf("read v1 migration: %v", err)
	}
	v2SQL, err := embeddedMigrationsFS.ReadFile("migrations/0002_pricing_overrides.sql")
	if err != nil {
		t.Fatalf("read v2 migration: %v", err)
	}

	// Simulate adding a v3 migration.
	v3FS := fstest.MapFS{
		"0001_initial_schema.sql":    &fstest.MapFile{Data: v1SQL},
		"0002_pricing_overrides.sql": &fstest.MapFile{Data: v2SQL},
		"0003_add_device_notes.sql": &fstest.MapFile{
			Data: []byte(`ALTER TABLE devices ADD COLUMN notes TEXT NOT NULL DEFAULT '';`),
		},
	}

	res, err := runMigrations(context.Background(), s.db, dbPath, v3FS)
	if err != nil {
		t.Fatalf("runMigrations v3: %v", err)
	}
	if res.Applied != 1 || res.CurrentVersion != 3 {
		t.Fatalf("expected 1 applied at v3, got %+v", res)
	}
	if res.BackupPath == "" {
		t.Fatalf("expected pre-migration backup file on upgrade")
	}
	if info, err := os.Stat(res.BackupPath); err != nil || info.Size() == 0 {
		t.Fatalf("expected non-empty backup at %s: %v", res.BackupPath, err)
	}

	// Verify existing row survived and new column exists.
	var name, notes string
	if err := s.db.QueryRow(`SELECT name, notes FROM devices WHERE id IS NOT NULL`).Scan(&name, &notes); err != nil {
		t.Fatalf("query upgraded table: %v", err)
	}
	if name != "macbook" || notes != "" {
		t.Fatalf("unexpected row after migration: name=%q notes=%q", name, notes)
	}

	// Simulate a broken v4 migration and confirm it rolls back cleanly without advancing version.
	v4BrokenFS := fstest.MapFS{
		"0001_initial_schema.sql":    v3FS["0001_initial_schema.sql"],
		"0002_pricing_overrides.sql": v3FS["0002_pricing_overrides.sql"],
		"0003_add_device_notes.sql":  v3FS["0003_add_device_notes.sql"],
		"0004_broken.sql": &fstest.MapFile{
			Data: []byte(`CREATE TABLE temp_ok (id TEXT); SELECT * FROM nonexistent_table;`),
		},
	}
	if _, err := runMigrations(context.Background(), s.db, dbPath, v4BrokenFS); err == nil {
		t.Fatalf("expected broken v4 migration to fail")
	}
	exists, err := sqliteTableExists(context.Background(), s.db, "temp_ok")
	if err != nil {
		t.Fatalf("check rollback table: %v", err)
	}
	if exists {
		t.Fatalf("expected temp_ok to be rolled back on failed migration")
	}
}

func TestMigrateBootstrapsPreVersionedDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.sqlite")
	s, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if _, err := s.CreateDevice(context.Background(), "legacy-box", "box", "linux", "amd64", "hash-legacy"); err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	// Drop schema_migrations to mimic a database created before versioned migrations existed.
	if _, err := s.db.Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatalf("drop schema_migrations: %v", err)
	}
	_ = s.Close()

	s2, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen legacy db: %v", err)
	}
	defer s2.Close()

	if got := s2.MigrationResult(); got.Applied != 2 || got.CurrentVersion != 2 {
		t.Fatalf("expected v2 bootstrap, got %+v", got)
	}
	devs, err := s2.ListDevices(context.Background())
	if err != nil || len(devs) != 1 || devs[0].Name != "legacy-box" {
		t.Fatalf("expected legacy device preserved, got %+v (err=%v)", devs, err)
	}
}
