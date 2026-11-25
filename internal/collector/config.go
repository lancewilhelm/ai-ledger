package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Config configures the ai-ledger collector running on a user device.
type Config struct {
	ServerURL               string        `json:"server_url"`
	DeviceToken             string        `json:"device_token"`
	StatePath               string        `json:"state_path,omitempty"`
	SnapshotIntervalMinutes int           `json:"snapshot_interval_minutes,omitempty"`
	ChunkSizeBytes          int64         `json:"chunk_size_bytes,omitempty"`
	Sources                 SourcesConfig `json:"sources"`
	ExtraSources            []ExtraSource `json:"extra_sources,omitempty"`
}

// SourcesConfig enables/configures built-in AI harness paths.
type SourcesConfig struct {
	CodexDir        string `json:"codex_dir,omitempty"`
	ClaudeDir       string `json:"claude_dir,omitempty"`
	AntigravityDir  string `json:"antigravity_dir,omitempty"`
	T3Dir           string `json:"t3_dir,omitempty"`
	HermesDir       string `json:"hermes_dir,omitempty"`
	DisableCodex    bool   `json:"disable_codex,omitempty"`
	DisableClaude   bool   `json:"disable_claude,omitempty"`
	DisableAG       bool   `json:"disable_antigravity,omitempty"`
	DisableT3       bool   `json:"disable_t3,omitempty"`
	DisableHermes   bool   `json:"disable_hermes,omitempty"`
	DisableSQLite   bool   `json:"disable_sqlite_snapshots,omitempty"`
	FullT3Snapshot  bool   `json:"full_t3_snapshot,omitempty"` // default false: extract compact T3 metadata/orchestration tables only
}

// ExtraSource lets users archive arbitrary extra directories (e.g., opencode, gemini CLI, cursor).
type ExtraSource struct {
	Name       string   `json:"name"`
	Dir        string   `json:"dir"`
	Extensions []string `json:"extensions,omitempty"` // e.g. [".jsonl", ".json", ".db"]
}

// DefaultConfig returns a ready-to-use default configuration for the current user's home directory.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		ServerURL:               "http://127.0.0.1:8484",
		StatePath:               filepath.Join(home, ".local", "state", "ai-ledger", "collector-state.json"),
		SnapshotIntervalMinutes: 30,
		ChunkSizeBytes:          8 << 20, // 8 MiB
		Sources: SourcesConfig{
			CodexDir:       filepath.Join(home, ".codex"),
			ClaudeDir:      filepath.Join(home, ".claude"),
			AntigravityDir: filepath.Join(home, ".t3", "userdata", "providers", "antigravity"),
			T3Dir:          filepath.Join(home, ".t3", "userdata"),
			HermesDir:      filepath.Join(home, ".hermes"),
		},
	}
}

// DefaultConfigPath returns ~/.config/ai-ledger/collector.json.
func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ai-ledger", "collector.json")
}

// LoadConfig reads a collector config JSON file and fills in defaults.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, err
	}
	cfg.ApplyDefaults()
	return cfg, nil
}

// SaveConfig writes a config JSON file with 0600 permissions (contains bearer token).
func SaveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

func (c *Config) ApplyDefaults() {
	def := DefaultConfig()
	if c.StatePath == "" {
		c.StatePath = def.StatePath
	}
	if c.SnapshotIntervalMinutes <= 0 {
		c.SnapshotIntervalMinutes = def.SnapshotIntervalMinutes
	}
	if c.ChunkSizeBytes <= 0 {
		c.ChunkSizeBytes = def.ChunkSizeBytes
	}
	if c.Sources.CodexDir == "" {
		c.Sources.CodexDir = def.Sources.CodexDir
	}
	if c.Sources.ClaudeDir == "" {
		c.Sources.ClaudeDir = def.Sources.ClaudeDir
	}
	if c.Sources.AntigravityDir == "" {
		c.Sources.AntigravityDir = def.Sources.AntigravityDir
	}
	if c.Sources.T3Dir == "" {
		c.Sources.T3Dir = def.Sources.T3Dir
	}
	if c.Sources.HermesDir == "" {
		c.Sources.HermesDir = def.Sources.HermesDir
	}
	c.StatePath = expandHome(c.StatePath)
	c.Sources.CodexDir = expandHome(c.Sources.CodexDir)
	c.Sources.ClaudeDir = expandHome(c.Sources.ClaudeDir)
	c.Sources.AntigravityDir = expandHome(c.Sources.AntigravityDir)
	c.Sources.T3Dir = expandHome(c.Sources.T3Dir)
	c.Sources.HermesDir = expandHome(c.Sources.HermesDir)
	for i := range c.ExtraSources {
		c.ExtraSources[i].Dir = expandHome(c.ExtraSources[i].Dir)
	}
	c.ServerURL = strings.TrimRight(strings.TrimSpace(c.ServerURL), "/")
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}
