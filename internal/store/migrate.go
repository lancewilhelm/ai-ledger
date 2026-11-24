package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var embeddedMigrationsFS embed.FS

// MigrationResult describes the outcome of running schema migrations.
type MigrationResult struct {
	CurrentVersion int
	Applied        int
	BackupPath     string
}

type sqlMigration struct {
	Version int
	Name    string
	SHA256  string
	SQL     string
}

// MigrationResult returns the last migration outcome recorded when opening the store.
func (s *SQLiteStore) MigrationResult() MigrationResult {
	return s.migrationResult
}

func (s *SQLiteStore) migrate(ctx context.Context, dbPath string) (MigrationResult, error) {
	sub, err := fs.Sub(embeddedMigrationsFS, "migrations")
	if err != nil {
		return MigrationResult{}, fmt.Errorf("load embedded migrations: %w", err)
	}
	return runMigrations(ctx, s.db, dbPath, sub)
}

func runMigrations(ctx context.Context, db *sql.DB, dbPath string, migrationsFS fs.FS) (MigrationResult, error) {
	migrations, err := loadSQLMigrations(migrationsFS)
	if err != nil {
		return MigrationResult{}, err
	}

	hasDevicesTable, err := sqliteTableExists(ctx, db, "devices")
	if err != nil {
		return MigrationResult{}, err
	}

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			sha256 TEXT NOT NULL,
			applied_at TEXT NOT NULL
		)
	`); err != nil {
		return MigrationResult{}, fmt.Errorf("create schema_migrations table: %w", err)
	}

	applied := make(map[int]string)
	rows, err := db.QueryContext(ctx, `SELECT version, sha256 FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		return MigrationResult{}, fmt.Errorf("read schema_migrations: %w", err)
	}
	var currentVersion int
	for rows.Next() {
		var ver int
		var hash string
		if err := rows.Scan(&ver, &hash); err != nil {
			rows.Close()
			return MigrationResult{}, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[ver] = hash
		if ver > currentVersion {
			currentVersion = ver
		}
	}
	if err := rows.Close(); err != nil {
		return MigrationResult{}, err
	}

	var pending []sqlMigration
	for _, m := range migrations {
		if existingHash, ok := applied[m.Version]; ok {
			if existingHash != m.SHA256 {
				return MigrationResult{}, fmt.Errorf("migration %04d_%s checksum mismatch (recorded %s, embedded %s)", m.Version, m.Name, existingHash, m.SHA256)
			}
			continue
		}
		pending = append(pending, m)
	}

	result := MigrationResult{CurrentVersion: currentVersion}
	if len(pending) == 0 {
		return result, nil
	}

	// Determine whether we are upgrading an existing database with real DDL changes.
	// Skip the backup for in-memory DBs, brand-new empty DBs, or a one-time bootstrap
	// where only v1 (CREATE TABLE IF NOT EXISTS) is recorded on an already-initialized DB.
	isOnlyV1Bootstrap := hasDevicesTable && len(applied) == 0 && len(pending) == 1 && pending[0].Version == 1
	shouldBackup := dbPath != "" && dbPath != ":memory:" && hasDevicesTable && !isOnlyV1Bootstrap && !skipMigrationBackup()
	if shouldBackup {
		backupPath, err := createPreMigrationBackup(ctx, db, dbPath)
		if err != nil {
			return MigrationResult{}, fmt.Errorf("create pre-migration backup: %w", err)
		}
		result.BackupPath = backupPath
		slog.Info("created pre-migration sqlite backup", "backup", backupPath, "pending_migrations", len(pending))
	}

	for _, m := range pending {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return result, fmt.Errorf("begin migration %04d_%s: %w", m.Version, m.Name, err)
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			_ = tx.Rollback()
			return result, fmt.Errorf("execute migration %04d_%s: %w", m.Version, m.Name, err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO schema_migrations (version, name, sha256, applied_at) VALUES (?, ?, ?, ?)`,
			m.Version,
			m.Name,
			m.SHA256,
			formatTime(time.Now()),
		); err != nil {
			_ = tx.Rollback()
			return result, fmt.Errorf("record migration %04d_%s: %w", m.Version, m.Name, err)
		}
		if err := tx.Commit(); err != nil {
			return result, fmt.Errorf("commit migration %04d_%s: %w", m.Version, m.Name, err)
		}
		result.Applied++
		if m.Version > result.CurrentVersion {
			result.CurrentVersion = m.Version
		}
	}

	return result, nil
}

func loadSQLMigrations(migrationsFS fs.FS) ([]sqlMigration, error) {
	entries, err := fs.ReadDir(migrationsFS, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations directory: %w", err)
	}
	var list []sqlMigration
	seen := make(map[int]string)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		ver, name, err := parseMigrationFilename(e.Name())
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[ver]; dup {
			return nil, fmt.Errorf("duplicate migration version %d (%s and %s)", ver, prev, e.Name())
		}
		seen[ver] = e.Name()

		raw, err := fs.ReadFile(migrationsFS, path.Clean(e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(raw)
		list = append(list, sqlMigration{
			Version: ver,
			Name:    name,
			SHA256:  hex.EncodeToString(sum[:]),
			SQL:     string(raw),
		})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Version < list[j].Version
	})
	return list, nil
}

func parseMigrationFilename(filename string) (int, string, error) {
	base := strings.TrimSuffix(filename, ".sql")
	parts := strings.SplitN(base, "_", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, "", fmt.Errorf("invalid migration filename %q (expected NNNN_description.sql)", filename)
	}
	ver, err := strconv.Atoi(parts[0])
	if err != nil || ver <= 0 {
		return 0, "", fmt.Errorf("invalid migration version in %q", filename)
	}
	return ver, parts[1], nil
}

func sqliteTableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var found string
	err := db.QueryRowContext(
		ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name,
	).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check table %q: %w", name, err)
	}
	return found == name, nil
}

func skipMigrationBackup() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("AI_LEDGER_SKIP_MIGRATION_BACKUP")))
	return v == "1" || v == "true" || v == "yes"
}

func createPreMigrationBackup(ctx context.Context, db *sql.DB, dbFilePath string) (string, error) {
	ts := time.Now().UTC().Format("20060102T150405.000Z")
	backupPath := fmt.Sprintf("%s.pre-migration-%s.bak", dbFilePath, ts)
	escaped := strings.ReplaceAll(backupPath, "'", "''")
	if _, err := db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", escaped)); err != nil {
		return "", err
	}
	return backupPath, nil
}
