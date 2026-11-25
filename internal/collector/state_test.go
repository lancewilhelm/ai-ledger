package collector

import (
	"path/filepath"
	"testing"
)

func TestStateResetIfServerMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Set("codex", "a.jsonl", FileCursor{Offset: 100, Size: 100})

	// Legacy state file without server_url resets when bound to a server
	if !st.ResetIfServerMismatch("http://server-a:8484") {
		t.Fatal("expected reset for legacy unscoped state with files")
	}
	if len(st.Files) != 0 || st.ServerURL != "http://server-a:8484" {
		t.Fatalf("unexpected state after reset: %+v", st)
	}

	st.Set("codex", "a.jsonl", FileCursor{Offset: 100, Size: 100})
	if st.ResetIfServerMismatch("http://server-a:8484") {
		t.Fatal("did not expect reset when server URL matches")
	}
	if len(st.Files) != 1 {
		t.Fatal("expected cursors preserved for same server")
	}

	if !st.ResetIfServerMismatch("http://server-b:8484") {
		t.Fatal("expected reset when switching to a different server")
	}
	if len(st.Files) != 0 || st.ServerURL != "http://server-b:8484" {
		t.Fatalf("unexpected state after server switch: %+v", st)
	}
}
