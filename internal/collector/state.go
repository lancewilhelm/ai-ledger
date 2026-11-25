package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileCursor remembers how much of a local file has been acknowledged by the server.
type FileCursor struct {
	Offset       int64     `json:"offset"`
	Size         int64     `json:"size"`
	MtimeUnix    int64     `json:"mtime_unix"`
	SHA256       string    `json:"sha256,omitempty"`
	HeaderSHA256 string    `json:"header_sha256,omitempty"`
	LastSyncAt   time.Time `json:"last_sync_at"`
}

// State persists local cursors across collector runs.
type State struct {
	mu        sync.Mutex
	path      string
	ServerURL string                `json:"server_url,omitempty"`
	Files     map[string]FileCursor `json:"files"`
}

// ResetIfServerMismatch clears cached cursors when switching to a different server URL
// (or migrating from an older state file that did not record server_url).
func (s *State) ResetIfServerMismatch(serverURL string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ServerURL == serverURL {
		return false
	}
	hadFiles := len(s.Files) > 0
	s.ServerURL = serverURL
	if hadFiles {
		s.Files = make(map[string]FileCursor)
		return true
	}
	return false
}

// LoadState reads the local state file or initializes an empty one.
func LoadState(path string) (*State, error) {
	st := &State{
		path:  path,
		Files: make(map[string]FileCursor),
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, st); err != nil {
			// Corrupted state file: start fresh; server /api/ingest/status reconciles safely.
			st.Files = make(map[string]FileCursor)
		}
	}
	if st.Files == nil {
		st.Files = make(map[string]FileCursor)
	}
	return st, nil
}

func StateKey(source, relPath string) string {
	return source + ":" + relPath
}

func (s *State) Get(source, relPath string) (FileCursor, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.Files[StateKey(source, relPath)]
	return c, ok
}

func (s *State) Set(source, relPath string, c FileCursor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Files[StateKey(source, relPath)] = c
}

// Save atomically persists the state JSON to disk.
func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
