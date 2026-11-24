package parser

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/store"
	_ "modernc.org/sqlite"
)

// parseT3 extracts T3 Code's unified thread & project metadata from a statev2.sqlite snapshot.
// Rather than double-counting usage already recorded in native Codex/Claude/Antigravity logs,
// it produces T3ThreadLink entries that enrich native sessions with project names and thread titles.
func (r *Registry) parseT3(ctx context.Context, in FileInput) (*store.ParsedFileBundle, error) {
	if !strings.HasSuffix(strings.ToLower(in.AbsPath), ".sqlite") && !strings.HasSuffix(strings.ToLower(in.AbsPath), ".db") {
		return &store.ParsedFileBundle{}, nil
	}

	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", in.AbsPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Load projects map
	type projInfo struct {
		Title         string
		WorkspaceRoot string
	}
	projects := make(map[string]projInfo)
	if rows, err := db.QueryContext(ctx, `SELECT project_id, title, workspace_root FROM projection_projects`); err == nil {
		for rows.Next() {
			var pid, title, root string
			if rows.Scan(&pid, &title, &root) == nil {
				projects[pid] = projInfo{Title: title, WorkspaceRoot: root}
			}
		}
		rows.Close()
	}

	// Map thread_id -> (project_id, title, updated_at)
	type threadMeta struct {
		ProjectID string
		Title     string
		UpdatedAt time.Time
	}
	threads := make(map[string]threadMeta)
	if rows, err := db.QueryContext(ctx, `SELECT thread_id, project_id, title, updated_at FROM orchestration_v2_projection_threads`); err == nil {
		for rows.Next() {
			var tid, pid, title, upd string
			if rows.Scan(&tid, &pid, &title, &upd) == nil {
				threads[tid] = threadMeta{
					ProjectID: pid,
					Title:     title,
					UpdatedAt: parseFlexibleTime(upd),
				}
			}
		}
		rows.Close()
	}

	// Also merge older projection_threads table if present
	if rows, err := db.QueryContext(ctx, `SELECT thread_id, project_id, title, updated_at FROM projection_threads`); err == nil {
		for rows.Next() {
			var tid, pid, title, upd string
			if rows.Scan(&tid, &pid, &title, &upd) == nil {
				if _, ok := threads[tid]; !ok {
					threads[tid] = threadMeta{
						ProjectID: pid,
						Title:     title,
						UpdatedAt: parseFlexibleTime(upd),
					}
				}
			}
		}
		rows.Close()
	}

	var links []protocol.T3ThreadLink
	seen := make(map[string]bool)

	// Extract native session IDs from orchestration_v2_projection_provider_threads
	if rows, err := db.QueryContext(ctx, `
		SELECT provider_thread_id, COALESCE(thread_id, ''), provider, updated_at
		FROM orchestration_v2_projection_provider_threads
	`); err == nil {
		for rows.Next() {
			var ptID, tID, prov, upd string
			if rows.Scan(&ptID, &tID, &prov, &upd) != nil {
				continue
			}
			nativeID := extractNativeSessionFromProviderThreadID(ptID)
			if nativeID == "" || tID == "" {
				continue
			}
			key := tID + "|" + nativeID
			if seen[key] {
				continue
			}
			seen[key] = true
			tm := threads[tID]
			pm := projects[tm.ProjectID]
			uTime := tm.UpdatedAt
			if uTime.IsZero() {
				uTime = parseFlexibleTime(upd)
			}
			if uTime.IsZero() {
				uTime = time.Now().UTC()
			}
			links = append(links, protocol.T3ThreadLink{
				ThreadID:        tID,
				NativeSessionID: nativeID,
				DeviceID:        in.DeviceID,
				Provider:        prov,
				ProjectID:       tm.ProjectID,
				ProjectTitle:    pm.Title,
				WorkspaceRoot:   pm.WorkspaceRoot,
				ThreadTitle:     tm.Title,
				UpdatedAt:       uTime,
			})
		}
		rows.Close()
	}

	// Also inspect active_provider_thread_id directly on orchestration_v2_projection_threads
	if rows, err := db.QueryContext(ctx, `
		SELECT thread_id, project_id, title, default_provider, COALESCE(active_provider_thread_id, ''), updated_at
		FROM orchestration_v2_projection_threads
	`); err == nil {
		for rows.Next() {
			var tID, pid, title, prov, ptID, upd string
			if rows.Scan(&tID, &pid, &title, &prov, &ptID, &upd) != nil {
				continue
			}
			nativeID := extractNativeSessionFromProviderThreadID(ptID)
			if nativeID == "" && strings.HasPrefix(tID, "import:") {
				parts := strings.SplitN(tID, ":", 3)
				if len(parts) == 3 {
					nativeID = parts[2]
				}
			}
			if nativeID == "" {
				continue
			}
			key := tID + "|" + nativeID
			if seen[key] {
				continue
			}
			seen[key] = true
			pm := projects[pid]
			uTime := parseFlexibleTime(upd)
			if uTime.IsZero() {
				uTime = time.Now().UTC()
			}
			links = append(links, protocol.T3ThreadLink{
				ThreadID:        tID,
				NativeSessionID: nativeID,
				DeviceID:        in.DeviceID,
				Provider:        prov,
				ProjectID:       pid,
				ProjectTitle:    pm.Title,
				WorkspaceRoot:   pm.WorkspaceRoot,
				ThreadTitle:     title,
				UpdatedAt:       uTime,
			})
		}
		rows.Close()
	}

	return &store.ParsedFileBundle{
		T3Links: links,
	}, nil
}

func extractNativeSessionFromProviderThreadID(ptID string) string {
	const marker = ":native-thread:"
	idx := strings.Index(ptID, marker)
	if idx < 0 {
		return ""
	}
	rest := ptID[idx+len(marker):]
	if rest == "" || strings.HasPrefix(rest, "pending") {
		return ""
	}
	return rest
}
