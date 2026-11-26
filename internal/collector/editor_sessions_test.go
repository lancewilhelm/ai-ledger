package collector

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lancewilhelm/ai-ledger/internal/protocol"
)

func TestDiscoverEditorTranscripts(t *testing.T) {
	root := t.TempDir()
	cfg := Config{Sources: SourcesConfig{
		ZedDataDir: filepath.Join(root, "zed"), ZedConfigDir: filepath.Join(root, "config/zed"),
		VSCodeUserDir: filepath.Join(root, "code"), CopilotDir: filepath.Join(root, "copilot"),
	}}
	for _, name := range []string{
		"zed/threads/threads.db",
		"zed/db/0-stable/db.sqlite",
		"config/zed/settings.json",
		"config/zed/conversations/legacy.json",
		"code/workspaceStorage/workspace/chatSessions/saved.json",
		"code/workspaceStorage/workspace/chatSessions/current.json",
		"code/workspaceStorage/workspace/chatSessions/current.jsonl",
		"code/workspaceStorage/workspace/workspace.json",
		"code/globalStorage/emptyWindowChatSessions/empty-window.json",
		"copilot/session-state/session/events.jsonl",
		"copilot/session-state/session/checkpoints/other.jsonl",
		"copilot/auth.db",
		"copilot/logs/process.log",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]struct {
		source, kind string
		appendOnly   bool
	}{
		"threads/threads.db.zst":                                  {protocol.SourceZed, protocol.FileKindSQLiteSnapshot, false},
		"conversations/legacy.json":                               {protocol.SourceZed, protocol.FileKindJSON, false},
		"workspaceStorage/workspace/chatSessions/saved.json":      {protocol.SourceVSCodeChat, protocol.FileKindJSON, false},
		"workspaceStorage/workspace/chatSessions/current.jsonl":   {protocol.SourceVSCodeChat, protocol.FileKindJSONL, false},
		"globalStorage/emptyWindowChatSessions/empty-window.json": {protocol.SourceVSCodeChat, protocol.FileKindJSON, false},
		"session-state/session/events.jsonl":                      {protocol.SourceCopilotCLI, protocol.FileKindJSONL, true},
	}
	files, err := Discover(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(want) {
		t.Fatalf("discovered %d files, want %d", len(files), len(want))
	}
	for _, file := range files {
		expect, ok := want[file.RelPath]
		if !ok || file.Source != expect.source || file.FileKind != expect.kind || file.IsAppend != expect.appendOnly {
			t.Errorf("unexpected file: %+v", file)
		}
		delete(want, file.RelPath)
	}

	cfg.Sources.DisableSQLite = true
	cfg.Sources.DisableCopilot = true
	cfg.Sources.DisableVSCode = true
	files, err = Discover(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].RelPath != "conversations/legacy.json" {
		t.Fatalf("expected just the legacy conversation with SQLite and other sources disabled, got %+v", files)
	}
}
