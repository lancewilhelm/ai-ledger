package parser

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/store"
	_ "modernc.org/sqlite"
)

func (r *Registry) parseHermes(ctx context.Context, in FileInput) (*store.ParsedFileBundle, error) {
	if !isSQLiteFile(in.AbsPath) {
		return &store.ParsedFileBundle{}, nil
	}

	dbPath, cleanup, err := prepareSQLitePath(in.AbsPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	bundle := &store.ParsedFileBundle{}

	sRows, err := db.QueryContext(ctx, `
		SELECT
			id, COALESCE(source, ''), COALESCE(model, ''), COALESCE(parent_session_id, ''),
			started_at, ended_at,
			COALESCE(message_count, 0), COALESCE(tool_call_count, 0), COALESCE(api_call_count, 0),
			COALESCE(input_tokens, 0), COALESCE(output_tokens, 0),
			COALESCE(cache_read_tokens, 0), COALESCE(cache_write_tokens, 0), COALESCE(reasoning_tokens, 0),
			COALESCE(cwd, ''), COALESCE(git_branch, ''), COALESCE(title, ''),
			estimated_cost_usd, actual_cost_usd
		FROM sessions
		ORDER BY started_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query hermes sessions: %w", err)
	}
	defer sRows.Close()

	sessionMap := make(map[string]int) // hermes native id -> index in bundle.Sessions
	defaultTitled := make(map[string]bool)

	for sRows.Next() {
		var (
			hID, src, model, parentID     string
			startedUnix                   float64
			endedUnix                     sql.NullFloat64
			msgCnt, tcCnt, apiCnt         int
			inTok, outTok, crTok, cwTok, rTok int64
			cwd, branch, title            string
			estCost, actCost              sql.NullFloat64
		)
		if err := sRows.Scan(
			&hID, &src, &model, &parentID,
			&startedUnix, &endedUnix,
			&msgCnt, &tcCnt, &apiCnt,
			&inTok, &outTok, &crTok, &cwTok, &rTok,
			&cwd, &branch, &title,
			&estCost, &actCost,
		); err != nil {
			return nil, err
		}

		sid := stableID("sess", in.DeviceID, protocol.SourceHermes, hID)
		startedAt := unixFloatToTime(startedUnix)
		var endedPtr *time.Time
		if endedUnix.Valid && endedUnix.Float64 > 0 {
			t := unixFloatToTime(endedUnix.Float64)
			endedPtr = &t
		}

		var repCost *float64
		if actCost.Valid && actCost.Float64 > 0 {
			v := actCost.Float64
			repCost = &v
		} else if estCost.Valid && estCost.Float64 > 0 {
			v := estCost.Float64
			repCost = &v
		}

		calcCost := r.Pricing.EstimateCost(model, inTok, crTok, cwTok, 0, outTok)
		if !r.Pricing.HasOverride(model) && repCost != nil && *repCost > 0 {
			calcCost = *repCost
		} else if calcCost == 0 && repCost != nil {
			calcCost = *repCost
		}
		if title == "" {
			title = "Hermes " + hID
			defaultTitled[sid] = true
		}

		idx := len(bundle.Sessions)
		sessionMap[hID] = idx
		bundle.Sessions = append(bundle.Sessions, protocol.SessionSummary{
			ID:                 sid,
			NativeID:           hID,
			DeviceID:           in.DeviceID,
			Harness:            protocol.SourceHermes,
			SourceFileID:       in.FileID,
			ProjectPath:        cwd,
			ProjectName:        deriveProjectName(cwd),
			GitBranch:          branch,
			Title:              title,
			ModelPrimary:       model,
			ParentSessionID:    parentID,
			Originator:         src,
			StartedAt:          startedAt,
			EndedAt:            endedPtr,
			MessageCount:       msgCnt,
			ToolCallCount:      tcCnt,
			APICallCount:       apiCnt,
			InputTokens:        inTok,
			CachedInputTokens:  crTok,
			CacheWrite5mTokens: cwTok,
			OutputTokens:       outTok,
			ReasoningTokens:    rTok,
			EstimatedCostUSD:   calcCost,
			ReportedCostUSD:    repCost,
		})
	}
	if err := sRows.Err(); err != nil {
		return nil, err
	}

	// Read messages
	mRows, err := db.QueryContext(ctx, `
		SELECT
			id, session_id, role, COALESCE(content, ''), COALESCE(tool_call_id, ''),
			COALESCE(tool_calls, ''), COALESCE(tool_name, ''), timestamp,
			COALESCE(finish_reason, ''), COALESCE(reasoning, ''), COALESCE(reasoning_content, '')
		FROM messages
		ORDER BY session_id ASC, id ASC
	`)
	if err == nil {
		ordPerSession := make(map[string]int)
		tcOrdPerSession := make(map[string]int)
		tcByCallID := make(map[string]int)
		for mRows.Next() {
			var (
				mID                                  int64
				hSID, role, content, tcID, tcJSON    string
				toolName, finishReason, reas, reasC  string
				tsUnix                               float64
			)
			if mRows.Scan(&mID, &hSID, &role, &content, &tcID, &tcJSON, &toolName, &tsUnix, &finishReason, &reas, &reasC) != nil {
				continue
			}
			sIdx, ok := sessionMap[hSID]
			if !ok {
				continue
			}
			sess := &bundle.Sessions[sIdx]
			ts := unixFloatToTime(tsUnix)
			tsPtr := &ts

			if role == "tool" && tcID != "" {
				if idx, found := tcByCallID[tcID]; found {
					bundle.ToolCalls[idx].OutputText = content
				} else {
					tcOrd := tcOrdPerSession[sess.ID]
					tcOrdPerSession[sess.ID]++
					bundle.ToolCalls = append(bundle.ToolCalls, protocol.ToolCallRecord{
						ID:         stableID("tc", sess.ID, fmt.Sprintf("%d", tcOrd), tcID),
						SessionID:  sess.ID,
						Ordinal:    tcOrd,
						CallID:     tcID,
						ToolName:   toolName,
						OutputText: content,
						Timestamp:  tsPtr,
					})
				}
				continue
			}

			thinking := reas
			if thinking == "" {
				thinking = reasC
			}
			ord := ordPerSession[sess.ID]
			ordPerSession[sess.ID]++
			msgRecID := stableID("msg", sess.ID, fmt.Sprintf("%d", ord), fmt.Sprintf("%d", mID))
			bundle.Messages = append(bundle.Messages, protocol.MessageRecord{
				ID:              msgRecID,
				SessionID:       sess.ID,
				Ordinal:         ord,
				NativeMessageID: fmt.Sprintf("%d", mID),
				Role:            role,
				Model:           sess.ModelPrimary,
				ContentText:     content,
				ThinkingText:    thinking,
				StopReason:      finishReason,
				Timestamp:       tsPtr,
			})
			if ord == 0 && role == "user" && defaultTitled[sess.ID] && strings.TrimSpace(content) != "" {
				sess.Title = truncateTitle(content, 100)
				delete(defaultTitled, sess.ID)
			}

			if tcJSON != "" && tcJSON != "[]" && tcJSON != "null" {
				var rawCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				}
				if json.Unmarshal([]byte(tcJSON), &rawCalls) == nil {
					for j, c := range rawCalls {
						name := c.Function.Name
						if name == "" {
							name = c.Name
						}
						args := c.Function.Arguments
						if args == "" && len(c.Arguments) > 0 {
							args = string(c.Arguments)
						}
						tcOrd := tcOrdPerSession[sess.ID]
						tcOrdPerSession[sess.ID]++
						tcIdx := len(bundle.ToolCalls)
						bundle.ToolCalls = append(bundle.ToolCalls, protocol.ToolCallRecord{
							ID:        stableID("tc", sess.ID, fmt.Sprintf("%d", tcOrd), fmt.Sprintf("%d:%d", mID, j)),
							SessionID: sess.ID,
							MessageID: msgRecID,
							Ordinal:   tcOrd,
							CallID:    c.ID,
							ToolName:  name,
							InputJSON: args,
							Timestamp: tsPtr,
						})
						if c.ID != "" {
							tcByCallID[c.ID] = tcIdx
						}
					}
				}
			}
		}
		mRows.Close()
		for hSID, idx := range sessionMap {
			sid := bundle.Sessions[idx].ID
			if ordPerSession[sid] > 0 {
				bundle.Sessions[idx].MessageCount = ordPerSession[sid]
			}
			if tcOrdPerSession[sid] > 0 {
				bundle.Sessions[idx].ToolCallCount = tcOrdPerSession[sid]
			}
			_ = hSID
		}
	}

	// Read session_model_usage for exact per-model usage breakdown
	sessionsWithModelUsage := make(map[string]bool)
	type usageRollup struct {
		inT, crT, cwT, outT, reasT int64
		cost                       float64
		calls                      int
	}
	usageBySession := make(map[string]*usageRollup)
	uRows, err := db.QueryContext(ctx, `
		SELECT
			session_id, model, COALESCE(task, ''),
			COALESCE(api_call_count, 0),
			COALESCE(input_tokens, 0), COALESCE(output_tokens, 0),
			COALESCE(cache_read_tokens, 0), COALESCE(cache_write_tokens, 0), COALESCE(reasoning_tokens, 0),
			COALESCE(estimated_cost_usd, 0), COALESCE(actual_cost_usd, 0),
			COALESCE(last_seen, first_seen, 0)
		FROM session_model_usage
	`)
	if err == nil {
		idxCounter := 0
		for uRows.Next() {
			var (
				hSID, model, task          string
				calls                      int
				inT, outT, crT, cwT, reasT int64
				estC, actC, seenUnix       float64
			)
			if uRows.Scan(&hSID, &model, &task, &calls, &inT, &outT, &crT, &cwT, &reasT, &estC, &actC, &seenUnix) != nil {
				continue
			}
			sIdx, ok := sessionMap[hSID]
			if !ok {
				continue
			}
			if inT == 0 && outT == 0 && crT == 0 && cwT == 0 {
				continue
			}
			sessionsWithModelUsage[hSID] = true
			sess := bundle.Sessions[sIdx]
			ts := unixFloatToTime(seenUnix)
			if ts.IsZero() {
				ts = sess.StartedAt
			}
			var cost float64
			if r.Pricing.HasOverride(model) {
				cost = r.Pricing.EstimateCost(model, inT, crT, cwT, 0, outT)
			} else {
				cost = actC
				if cost == 0 {
					cost = estC
				}
				if cost == 0 {
					cost = r.Pricing.EstimateCost(model, inT, crT, cwT, 0, outT)
				}
			}
			ru := usageBySession[hSID]
			if ru == nil {
				ru = &usageRollup{}
				usageBySession[hSID] = ru
			}
			ru.inT += inT
			ru.crT += crT
			ru.cwT += cwT
			ru.outT += outT
			ru.reasT += reasT
			ru.cost += cost
			ru.calls += calls

			idxCounter++
			bundle.Usage = append(bundle.Usage, protocol.UsageEventRecord{
				ID:                 stableID("use", sess.ID, model, task, fmt.Sprintf("%d", idxCounter)),
				SessionID:          sess.ID,
				DeviceID:           in.DeviceID,
				Harness:            protocol.SourceHermes,
				Model:              model,
				Timestamp:          ts,
				InputTokens:        inT,
				CachedInputTokens:  crT,
				CacheWrite5mTokens: cwT,
				OutputTokens:       outT,
				ReasoningTokens:    reasT,
				EstimatedCostUSD:   cost,
			})
		}
		uRows.Close()

		// Reconcile session rollups with summed session_model_usage so Sessions, Overview, and Devices agree.
		for hSID, ru := range usageBySession {
			sIdx := sessionMap[hSID]
			sess := &bundle.Sessions[sIdx]
			sess.InputTokens = ru.inT
			sess.CachedInputTokens = ru.crT
			sess.CacheWrite5mTokens = ru.cwT
			sess.OutputTokens = ru.outT
			sess.ReasoningTokens = ru.reasT
			sess.EstimatedCostUSD = ru.cost
			if ru.calls > 0 {
				sess.APICallCount = ru.calls
			}
		}
	}

	// Fallback: if a Hermes session has token totals on sessions table but no session_model_usage rows
	for hSID, sIdx := range sessionMap {
		if sessionsWithModelUsage[hSID] {
			continue
		}
		sess := bundle.Sessions[sIdx]
		if sess.InputTokens == 0 && sess.OutputTokens == 0 && sess.CachedInputTokens == 0 && sess.CacheWrite5mTokens == 0 {
			continue
		}
		bundle.Usage = append(bundle.Usage, protocol.UsageEventRecord{
			ID:                 stableID("use", sess.ID, "summary"),
			SessionID:          sess.ID,
			DeviceID:           in.DeviceID,
			Harness:            protocol.SourceHermes,
			Model:              sess.ModelPrimary,
			Timestamp:          sess.StartedAt,
			InputTokens:        sess.InputTokens,
			CachedInputTokens:  sess.CachedInputTokens,
			CacheWrite5mTokens: sess.CacheWrite5mTokens,
			OutputTokens:       sess.OutputTokens,
			ReasoningTokens:    sess.ReasoningTokens,
			EstimatedCostUSD:   sess.EstimatedCostUSD,
		})
	}

	return bundle, nil
}

func unixFloatToTime(sec float64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	whole := int64(sec)
	nsec := int64((sec - float64(whole)) * 1e9)
	return time.Unix(whole, nsec).UTC()
}
