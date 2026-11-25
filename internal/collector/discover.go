package collector

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lancewilhelm/ai-ledger/internal/protocol"
)

// CandidateFile is a local file discovered for archiving and parsing.
type CandidateFile struct {
	Source   string
	RelPath  string
	AbsPath  string
	FileKind string
	Size     int64
	Mtime    int64
	IsAppend bool // true for append-only JSONL, false for full snapshot upload
}

// Discover scans all configured local AI log directories and returns files to sync.
func Discover(cfg Config) ([]CandidateFile, error) {
	var out []CandidateFile

	// 1. Codex: ~/.codex/sessions/**/*.jsonl(.zst) plus top-level history/session_index jsonl
	if !cfg.Sources.DisableCodex && dirExists(cfg.Sources.CodexDir) {
		sessionsDir := filepath.Join(cfg.Sources.CodexDir, "sessions")
		if dirExists(sessionsDir) {
			_ = filepath.WalkDir(sessionsDir, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				lower := strings.ToLower(d.Name())
				rel, rerr := filepath.Rel(cfg.Sources.CodexDir, path)
				if rerr != nil {
					return nil
				}
				rel = filepath.ToSlash(rel)
				info, ierr := d.Info()
				if ierr != nil || info.Size() == 0 {
					return nil
				}
				if strings.HasSuffix(lower, ".jsonl") {
					out = append(out, CandidateFile{
						Source:   protocol.SourceCodex,
						RelPath:  rel,
						AbsPath:  path,
						FileKind: protocol.FileKindJSONL,
						Size:     info.Size(),
						Mtime:    info.ModTime().Unix(),
						IsAppend: true,
					})
				} else if strings.HasSuffix(lower, ".jsonl.zst") {
					out = append(out, CandidateFile{
						Source:   protocol.SourceCodex,
						RelPath:  rel,
						AbsPath:  path,
						FileKind: protocol.FileKindJSONLZst,
						Size:     info.Size(),
						Mtime:    info.ModTime().Unix(),
						IsAppend: false,
					})
				}
				return nil
			})
		}
		// Also archive Codex top-level history/index files as raw archive extras
		for _, name := range []string{"history.jsonl", "session_index.jsonl"} {
			p := filepath.Join(cfg.Sources.CodexDir, name)
			if st, err := os.Stat(p); err == nil && st.Size() > 0 {
				out = append(out, CandidateFile{
					Source:   "codex_meta",
					RelPath:  name,
					AbsPath:  p,
					FileKind: protocol.FileKindJSONL,
					Size:     st.Size(),
					Mtime:    st.ModTime().Unix(),
					IsAppend: true,
				})
			}
		}
	}

	// 2. Claude Code: ~/.claude/projects/**/*.jsonl (including subagents!) + history.jsonl
	if !cfg.Sources.DisableClaude && dirExists(cfg.Sources.ClaudeDir) {
		projectsDir := filepath.Join(cfg.Sources.ClaudeDir, "projects")
		if dirExists(projectsDir) {
			_ = filepath.WalkDir(projectsDir, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if !strings.HasSuffix(strings.ToLower(d.Name()), ".jsonl") {
					return nil
				}
				info, ierr := d.Info()
				if ierr != nil || info.Size() == 0 {
					return nil
				}
				rel, rerr := filepath.Rel(cfg.Sources.ClaudeDir, path)
				if rerr != nil {
					return nil
				}
				out = append(out, CandidateFile{
					Source:   protocol.SourceClaude,
					RelPath:  filepath.ToSlash(rel),
					AbsPath:  path,
					FileKind: protocol.FileKindJSONL,
					Size:     info.Size(),
					Mtime:    info.ModTime().Unix(),
					IsAppend: true,
				})
				return nil
			})
		}
		if st, err := os.Stat(filepath.Join(cfg.Sources.ClaudeDir, "history.jsonl")); err == nil && st.Size() > 0 {
			out = append(out, CandidateFile{
				Source:   "claude_meta",
				RelPath:  "history.jsonl",
				AbsPath:  filepath.Join(cfg.Sources.ClaudeDir, "history.jsonl"),
				FileKind: protocol.FileKindJSONL,
				Size:     st.Size(),
				Mtime:    st.ModTime().Unix(),
				IsAppend: true,
			})
		}
	}

	// 3. Antigravity (via T3): ~/.t3/userdata/providers/antigravity/**/logs/transcript_full.jsonl (& transcript.jsonl)
	if !cfg.Sources.DisableAG && dirExists(cfg.Sources.AntigravityDir) {
		_ = filepath.WalkDir(cfg.Sources.AntigravityDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			name := d.Name()
			if name != "transcript_full.jsonl" && name != "transcript.jsonl" {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil || info.Size() == 0 {
				return nil
			}
			rel, rerr := filepath.Rel(cfg.Sources.AntigravityDir, path)
			if rerr != nil {
				return nil
			}
			out = append(out, CandidateFile{
				Source:   protocol.SourceAntigravity,
				RelPath:  filepath.ToSlash(rel),
				AbsPath:  path,
				FileKind: protocol.FileKindJSONL,
				Size:     info.Size(),
				Mtime:    info.ModTime().Unix(),
				IsAppend: true,
			})
			return nil
		})
	}

	// 4. T3 Code: usage-model-rates.json + statev2.sqlite online snapshot
	if !cfg.Sources.DisableT3 && dirExists(cfg.Sources.T3Dir) {
		ratesPath := filepath.Join(cfg.Sources.T3Dir, "usage-model-rates.json")
		if st, err := os.Stat(ratesPath); err == nil && st.Size() > 0 {
			out = append(out, CandidateFile{
				Source:   protocol.SourceRates,
				RelPath:  "usage-model-rates.json",
				AbsPath:  ratesPath,
				FileKind: protocol.FileKindJSON,
				Size:     st.Size(),
				Mtime:    st.ModTime().Unix(),
				IsAppend: false,
			})
		}
		if !cfg.Sources.DisableSQLite {
			t3DB := filepath.Join(cfg.Sources.T3Dir, "statev2.sqlite")
			if st, err := os.Stat(t3DB); err == nil && st.Size() > 0 {
				mtime := maxSQLiteMtime(t3DB, st.ModTime().Unix())
				rel := "statev2-meta.sqlite.zst"
				if cfg.Sources.FullT3Snapshot {
					rel = "statev2.sqlite.zst"
				}
				out = append(out, CandidateFile{
					Source:   protocol.SourceT3,
					RelPath:  rel,
					AbsPath:  t3DB,
					FileKind: protocol.FileKindSQLiteSnapshot,
					Size:     st.Size(),
					Mtime:    mtime,
					IsAppend: false,
				})
			}
		}
	}

	// 5. Hermes: ~/.hermes/state.db online snapshot (zstd-compressed)
	if !cfg.Sources.DisableHermes && !cfg.Sources.DisableSQLite && dirExists(cfg.Sources.HermesDir) {
		hDB := filepath.Join(cfg.Sources.HermesDir, "state.db")
		if st, err := os.Stat(hDB); err == nil && st.Size() > 0 {
			mtime := maxSQLiteMtime(hDB, st.ModTime().Unix())
			out = append(out, CandidateFile{
				Source:   protocol.SourceHermes,
				RelPath:  "state.db.zst",
				AbsPath:  hDB,
				FileKind: protocol.FileKindSQLiteSnapshot,
				Size:     st.Size(),
				Mtime:    mtime,
				IsAppend: false,
			})
		}
	}

	// 6. User-configured extra sources
	for _, ex := range cfg.ExtraSources {
		if ex.Name == "" || !dirExists(ex.Dir) {
			continue
		}
		extSet := make(map[string]bool)
		for _, e := range ex.Extensions {
			extSet[strings.ToLower(e)] = true
		}
		_ = filepath.WalkDir(ex.Dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if len(extSet) > 0 && !extSet[ext] {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil || info.Size() == 0 {
				return nil
			}
			rel, rerr := filepath.Rel(ex.Dir, path)
			if rerr != nil {
				return nil
			}
			isAppend := ext == ".jsonl"
			fk := protocol.FileKindOther
			if ext == ".jsonl" {
				fk = protocol.FileKindJSONL
			} else if ext == ".json" {
				fk = protocol.FileKindJSON
			} else if ext == ".db" || ext == ".sqlite" {
				fk = protocol.FileKindSQLiteSnapshot
			}
			relPath := filepath.ToSlash(rel)
			if fk == protocol.FileKindSQLiteSnapshot && !strings.HasSuffix(strings.ToLower(relPath), ".zst") {
				relPath += ".zst"
			}
			out = append(out, CandidateFile{
				Source:   ex.Name,
				RelPath:  relPath,
				AbsPath:  path,
				FileKind: fk,
				Size:     info.Size(),
				Mtime:    info.ModTime().Unix(),
				IsAppend: isAppend,
			})
			return nil
		})
	}

	// Order: rates & T3 metadata first so sessions get enriched cleanly, then everything else deterministically
	sort.Slice(out, func(i, j int) bool {
		pi, pj := sourcePriority(out[i].Source), sourcePriority(out[j].Source)
		if pi != pj {
			return pi < pj
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].RelPath < out[j].RelPath
	})

	return out, nil
}

func sourcePriority(src string) int {
	switch src {
	case protocol.SourceRates:
		return 0
	case protocol.SourceT3:
		return 1
	default:
		return 2
	}
}

func dirExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func maxSQLiteMtime(dbPath string, baseMtime int64) int64 {
	best := baseMtime
	for _, suf := range []string{"-wal", "-journal"} {
		if st, err := os.Stat(dbPath + suf); err == nil {
			if m := st.ModTime().Unix(); m > best {
				best = m
			}
		}
	}
	return best
}
