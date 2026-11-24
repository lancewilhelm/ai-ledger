package collector

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// CreateSQLiteSnapshot creates a consistent, WAL-safe snapshot of a live SQLite database
// in a temporary file. Caller is responsible for removing the returned file path.
func CreateSQLiteSnapshot(ctx context.Context, srcDB string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "ai-ledger-snap-*")
	if err != nil {
		return "", err
	}
	dstPath := filepath.Join(tmpDir, filepath.Base(srcDB))

	// Prefer native sqlite3 CLI .backup when available (fastest & lowest memory for 900 MB DBs)
	if sqlite3Bin, err := exec.LookPath("sqlite3"); err == nil {
		cmd := exec.CommandContext(ctx, sqlite3Bin, srcDB, fmt.Sprintf(".backup '%s'", strings.ReplaceAll(dstPath, "'", "''")))
		if out, err := cmd.CombinedOutput(); err == nil {
			if st, serr := os.Stat(dstPath); serr == nil && st.Size() > 0 {
				return dstPath, nil
			}
		} else {
			_ = out
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
