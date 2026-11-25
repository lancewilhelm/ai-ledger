package collector

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const DefaultReleaseRepo = "lancewilhelm/ai-ledger"

// UpdateOptions configures a self-update of ai-ledger-collector from GitHub Releases.
type UpdateOptions struct {
	Repo           string // e.g. "lancewilhelm/ai-ledger"
	CurrentVersion string // build-time version string
	TargetVersion  string // empty or "latest" for latest release, otherwise tag like "v0.2.0"
	CheckOnly      bool   // report availability without modifying disk
	Force          bool   // reinstall even if current version matches target
	NoRestart      bool   // skip restarting systemd user service on Linux
	ExecutablePath string // optional override (defaults to current os.Executable)
	APIBaseURL     string // optional override for testing (defaults to https://api.github.com)
	HTTPClient     *http.Client
	GOOS           string // optional override for testing
	GOARCH         string // optional override for testing
}

// UpdateResult summarizes the outcome of SelfUpdate.
type UpdateResult struct {
	CurrentVersion   string
	LatestVersion    string
	Updated          bool
	AlreadyUpToDate  bool
	ExecutablePath   string
	ChecksumVerified bool
	ServiceRestarted bool
	ServiceNote      string
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// SelfUpdate checks GitHub Releases for a newer ai-ledger-collector binary,
// verifies its SHA-256 against SHA256SUMS.txt when present, atomically replaces
// the installed executable, and restarts the background service when appropriate.
func SelfUpdate(ctx context.Context, opts UpdateOptions) (*UpdateResult, error) {
	repo := strings.TrimSpace(opts.Repo)
	if repo == "" {
		repo = DefaultReleaseRepo
	}
	goos := opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := opts.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	apiBase := strings.TrimRight(opts.APIBaseURL, "/")
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}

	exePath := opts.ExecutablePath
	if exePath == "" {
		var err error
		exePath, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate current binary: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
			exePath = resolved
		}
	}

	rel, err := fetchRelease(ctx, client, apiBase, repo, opts.TargetVersion, opts.CurrentVersion)
	if err != nil {
		return nil, err
	}

	res := &UpdateResult{
		CurrentVersion: opts.CurrentVersion,
		LatestVersion:  rel.TagName,
		ExecutablePath: exePath,
	}

	if sameVersion(opts.CurrentVersion, rel.TagName) && !opts.Force {
		res.AlreadyUpToDate = true
		return res, nil
	}

	assetName := fmt.Sprintf("ai-ledger-collector-%s-%s", goos, goarch)
	var binAsset, sumAsset *ghAsset
	for i := range rel.Assets {
		a := &rel.Assets[i]
		switch a.Name {
		case assetName:
			binAsset = a
		case "SHA256SUMS.txt":
			sumAsset = a
		}
	}
	if binAsset == nil {
		return nil, fmt.Errorf("release %s does not include asset %s", rel.TagName, assetName)
	}

	if opts.CheckOnly {
		return res, nil
	}

	var expectedSum string
	if sumAsset != nil {
		sumsBody, err := downloadBytes(ctx, client, sumAsset.BrowserDownloadURL, opts.CurrentVersion)
		if err != nil {
			return nil, fmt.Errorf("download SHA256SUMS.txt: %w", err)
		}
		expectedSum, err = parseChecksum(string(sumsBody), assetName)
		if err != nil {
			return nil, err
		}
	}

	dir := filepath.Dir(exePath)
	tmpFile, err := os.CreateTemp(dir, ".ai-ledger-collector-update-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("permission denied writing to %s (try: sudo %s update)", dir, filepath.Base(exePath))
		}
		return nil, fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	actualSum, err := downloadToFile(ctx, client, binAsset.BrowserDownloadURL, opts.CurrentVersion, tmpFile)
	closeErr := tmpFile.Close()
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", assetName, err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close temp file: %w", closeErr)
	}

	if expectedSum != "" {
		if !strings.EqualFold(actualSum, expectedSum) {
			return nil, fmt.Errorf("checksum mismatch for %s: expected %s, got %s", assetName, expectedSum, actualSum)
		}
		res.ChecksumVerified = true
	}

	mode := os.FileMode(0o755)
	if st, err := os.Stat(exePath); err == nil {
		mode = st.Mode().Perm() | 0o111
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return nil, fmt.Errorf("chmod temp binary: %w", err)
	}

	// Atomic rename avoids ETXTBSY ("text file busy") on Linux even while the daemon is running.
	if err := os.Rename(tmpPath, exePath); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("permission denied replacing %s (try: sudo %s update)", exePath, filepath.Base(exePath))
		}
		return nil, fmt.Errorf("replace binary %s: %w", exePath, err)
	}
	cleanupTmp = false
	res.Updated = true

	if !opts.NoRestart && opts.ExecutablePath == "" {
		restarted, note := maybeRestartBackgroundService(ctx, goos)
		res.ServiceRestarted = restarted
		res.ServiceNote = note
	}

	return res, nil
}

func sameVersion(cur, tag string) bool {
	c := strings.TrimPrefix(strings.TrimSpace(cur), "v")
	t := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	return c != "" && t != "" && c == t
}

func fetchRelease(ctx context.Context, client *http.Client, apiBase, repo, targetVersion, currentVersion string) (*ghRelease, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", apiBase, repo)
	if v := strings.TrimSpace(targetVersion); v != "" && !strings.EqualFold(v, "latest") {
		endpoint = fmt.Sprintf("%s/repos/%s/releases/tags/%s", apiBase, repo, v)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "ai-ledger-collector/"+currentVersion)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query GitHub releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("release not found at %s (no published GitHub Release yet)", endpoint)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode GitHub release metadata: %w", err)
	}
	if rel.TagName == "" {
		return nil, errors.New("GitHub release metadata missing tag_name")
	}
	return &rel, nil
}

func downloadBytes(ctx context.Context, client *http.Client, url, currentVersion string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ai-ledger-collector/"+currentVersion)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func downloadToFile(ctx context.Context, client *http.Client, url, currentVersion string, dst *os.File) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ai-ledger-collector/"+currentVersion)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hasher), resp.Body)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", errors.New("downloaded binary is empty")
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func parseChecksum(sumsText, filename string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(sumsText))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		name = filepath.Base(name)
		if name == filename {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found in SHA256SUMS.txt", filename)
}

func maybeRestartBackgroundService(ctx context.Context, goos string) (bool, string) {
	switch goos {
	case "linux":
		if _, err := exec.LookPath("systemctl"); err != nil {
			return false, ""
		}
		check := exec.CommandContext(ctx, "systemctl", "--user", "is-active", "--quiet", "ai-ledger-collector.service")
		if err := check.Run(); err != nil {
			return false, ""
		}
		restart := exec.CommandContext(ctx, "systemctl", "--user", "restart", "ai-ledger-collector.service")
		if out, err := restart.CombinedOutput(); err != nil {
			return false, fmt.Sprintf("could not restart systemd user service (%v: %s) — run: systemctl --user restart ai-ledger-collector.service", err, strings.TrimSpace(string(out)))
		}
		return true, "Restarted systemd user service (ai-ledger-collector.service)"
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return false, ""
		}
		plist := filepath.Join(home, "Library", "LaunchAgents", "com.ai-ledger.collector.plist")
		if _, err := os.Stat(plist); err == nil {
			return false, "macOS LaunchAgent is installed and will use the new binary automatically on its next scheduled run"
		}
	}
	return false, ""
}
