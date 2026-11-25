package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChecksum(t *testing.T) {
	sums := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  ai-ledger-collector-linux-amd64\n" +
		"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB *ai-ledger-collector-darwin-arm64\n"

	got, err := parseChecksum(sums, "ai-ledger-collector-linux-amd64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != strings.Repeat("a", 64) {
		t.Fatalf("got %q", got)
	}

	got, err = parseChecksum(sums, "ai-ledger-collector-darwin-arm64")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != strings.Repeat("b", 64) {
		t.Fatalf("got %q", got)
	}

	if _, err := parseChecksum(sums, "missing-asset"); err == nil {
		t.Fatalf("expected error for missing asset")
	}
}

func TestSelfUpdateFlow(t *testing.T) {
	newBin := []byte("#!/bin/sh\necho v0.2.0\n")
	sumBytes := sha256.Sum256(newBin)
	goodSum := hex.EncodeToString(sumBytes[:])
	badChecksum := false

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/example/ai-ledger/releases/latest":
			_ = json.NewEncoder(w).Encode(ghRelease{
				TagName: "v0.2.0",
				Assets: []ghAsset{
					{Name: "ai-ledger-collector-linux-amd64", BrowserDownloadURL: srv.URL + "/dl/bin"},
					{Name: "SHA256SUMS.txt", BrowserDownloadURL: srv.URL + "/dl/sums"},
				},
			})
		case "/dl/bin":
			_, _ = w.Write(newBin)
		case "/dl/sums":
			sum := goodSum
			if badChecksum {
				sum = strings.Repeat("0", 64)
			}
			_, _ = w.Write([]byte(sum + "  ai-ledger-collector-linux-amd64\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	exe := filepath.Join(dir, "ai-ledger-collector")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	baseOpts := UpdateOptions{
		Repo:           "example/ai-ledger",
		CurrentVersion: "v0.1.0",
		ExecutablePath: exe,
		APIBaseURL:     srv.URL,
		HTTPClient:     srv.Client(),
		GOOS:           "linux",
		GOARCH:         "amd64",
		NoRestart:      true,
	}

	// 1. CheckOnly reports update without modifying binary
	chkOpts := baseOpts
	chkOpts.CheckOnly = true
	res, err := SelfUpdate(context.Background(), chkOpts)
	if err != nil {
		t.Fatalf("check failed: %v", err)
	}
	if res.Updated || res.AlreadyUpToDate || res.LatestVersion != "v0.2.0" {
		t.Fatalf("unexpected check result: %+v", res)
	}
	raw, _ := os.ReadFile(exe)
	if string(raw) != "old-binary" {
		t.Fatalf("check modified binary")
	}

	// 2. Checksum mismatch rejects update and leaves existing binary intact
	badChecksum = true
	if _, err := SelfUpdate(context.Background(), baseOpts); err == nil {
		t.Fatalf("expected checksum mismatch error")
	}
	raw, _ = os.ReadFile(exe)
	if string(raw) != "old-binary" {
		t.Fatalf("failed checksum overwrote binary")
	}
	badChecksum = false

	// 3. Full update verifies checksum and atomically replaces binary
	res, err = SelfUpdate(context.Background(), baseOpts)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if !res.Updated || !res.ChecksumVerified || res.LatestVersion != "v0.2.0" {
		t.Fatalf("unexpected update result: %+v", res)
	}
	raw, _ = os.ReadFile(exe)
	if string(raw) != string(newBin) {
		t.Fatalf("binary content mismatch: got %q", string(raw))
	}

	// 4. Already up to date is a no-op
	sameOpts := baseOpts
	sameOpts.CurrentVersion = "v0.2.0"
	res, err = SelfUpdate(context.Background(), sameOpts)
	if err != nil {
		t.Fatalf("already-up-to-date failed: %v", err)
	}
	if !res.AlreadyUpToDate || res.Updated {
		t.Fatalf("expected AlreadyUpToDate, got %+v", res)
	}
}
