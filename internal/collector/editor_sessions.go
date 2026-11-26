package collector

import (
	"os"
	"path/filepath"

	"github.com/lancewilhelm/ai-ledger/internal/protocol"
)

func discoverEditorSessions(out []CandidateFile, cfg SourcesConfig) []CandidateFile {
	add := func(root, rel, source, kind string, appendOnly bool) {
		path := filepath.Join(root, rel)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return
		}
		mtime := info.ModTime().Unix()
		if kind == protocol.FileKindSQLiteSnapshot {
			mtime = maxSQLiteMtime(path, mtime)
			rel += ".zst"
		}
		out = append(out, CandidateFile{
			Source: source, RelPath: filepath.ToSlash(rel), AbsPath: path,
			FileKind: kind, Size: info.Size(), Mtime: mtime, IsAppend: appendOnly,
		})
	}

	if !cfg.DisableZed {
		if !cfg.DisableSQLite && dirExists(cfg.ZedDataDir) {
			add(cfg.ZedDataDir, filepath.Join("threads", "threads.db"), protocol.SourceZed, protocol.FileKindSQLiteSnapshot, false)
		}
		if dirExists(cfg.ZedConfigDir) {
			entries, _ := os.ReadDir(filepath.Join(cfg.ZedConfigDir, "conversations"))
			for _, entry := range entries {
				if filepath.Ext(entry.Name()) == ".json" {
					add(cfg.ZedConfigDir, filepath.Join("conversations", entry.Name()), protocol.SourceZed, protocol.FileKindJSON, false)
				}
			}
		}
	}

	if !cfg.DisableVSCode && dirExists(cfg.VSCodeUserDir) {
		addChats := func(dir string) {
			entries, _ := os.ReadDir(filepath.Join(cfg.VSCodeUserDir, dir))
			for _, entry := range entries {
				name := entry.Name()
				switch filepath.Ext(name) {
				case ".jsonl":
					add(cfg.VSCodeUserDir, filepath.Join(dir, name), protocol.SourceVSCodeChat, protocol.FileKindJSONL, false)
				case ".json":
					logPath := filepath.Join(cfg.VSCodeUserDir, dir, name+"l")
					if info, err := os.Lstat(logPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
						continue
					}
					add(cfg.VSCodeUserDir, filepath.Join(dir, name), protocol.SourceVSCodeChat, protocol.FileKindJSON, false)
				}
			}
		}
		workspaces, _ := os.ReadDir(filepath.Join(cfg.VSCodeUserDir, "workspaceStorage"))
		for _, workspace := range workspaces {
			if workspace.IsDir() {
				addChats(filepath.Join("workspaceStorage", workspace.Name(), "chatSessions"))
			}
		}
		addChats(filepath.Join("globalStorage", "emptyWindowChatSessions"))
	}

	if !cfg.DisableCopilot && dirExists(cfg.CopilotDir) {
		sessions, _ := os.ReadDir(filepath.Join(cfg.CopilotDir, "session-state"))
		for _, session := range sessions {
			if session.IsDir() {
				add(cfg.CopilotDir, filepath.Join("session-state", session.Name(), "events.jsonl"), protocol.SourceCopilotCLI, protocol.FileKindJSONL, true)
			}
		}
	}

	return out
}
