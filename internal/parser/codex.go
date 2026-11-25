package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/store"
)

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Ordinal   int             `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexTokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

func (r *Registry) parseCodex(ctx context.Context, in FileInput) (*store.ParsedFileBundle, error) {
	rc, err := openMaybeZstd(in.AbsPath)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var (
		nativeID        string
		parentSessionID string
		isSidechain     bool
		cwd             string
		gitBranch       string
		originator      string
		currentModel    string
		currentEff      string
		startedAt       time.Time
		endedAt         time.Time
		firstTitle      string
		contextWin      int64
	)

	modelCounts := make(map[string]int)
	var messages []protocol.MessageRecord
	var toolCalls []protocol.ToolCallRecord
	toolCallIdxByCallID := make(map[string]int)

	// Separate storage for token_usage_record vs fallback event_msg:token_count
	// so we never double-count when both are emitted in the same rollout file.
	var primaryUsage []protocol.UsageEventRecord
	var fallbackUsage []protocol.UsageEventRecord
	seenRespIDs := make(map[string]bool)
	var prevTotalIn, prevTotalOut int64

	var rateLimits []protocol.RateLimitSnapshot

	// Fallback native ID from filename if session_meta is absent
	base := filepath.Base(in.RelPath)
	base = strings.TrimSuffix(base, ".zst")
	base = strings.TrimSuffix(base, ".jsonl")
	sessionID := ""
	ensureSessionID := func() string {
		if sessionID == "" {
			nid := nativeID
			if nid == "" {
				nid = base
			}
			sessionID = stableID("sess", in.DeviceID, protocol.SourceCodex, nid)
		}
		return sessionID
	}

	err = scanJSONL(rc, func(lineNum int, raw []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var cl codexLine
		if err := json.Unmarshal(raw, &cl); err != nil {
			return nil // tolerate partial trailing line
		}
		ts := parseFlexibleTime(cl.Timestamp)
		if !ts.IsZero() {
			if startedAt.IsZero() || ts.Before(startedAt) {
				startedAt = ts
			}
			if ts.After(endedAt) {
				endedAt = ts
			}
		}

		switch cl.Type {
		case "session_meta":
			var meta struct {
				ID             string          `json:"id"`
				SessionID      string          `json:"session_id"`
				ParentThreadID string          `json:"parent_thread_id"`
				Source         json.RawMessage `json:"source"`
				Timestamp      string          `json:"timestamp"`
				CWD            string          `json:"cwd"`
				Originator     string          `json:"originator"`
				Git            struct {
					Branch string `json:"branch"`
				} `json:"git"`
			}
			if json.Unmarshal(cl.Payload, &meta) == nil {
				// Prefer meta.ID (this rollout thread's own unique ID).
				// For Codex subagents, meta.SessionID holds the parent thread's ID.
				if meta.ID != "" {
					nativeID = meta.ID
				} else if meta.SessionID != "" {
					nativeID = meta.SessionID
				}
				if meta.ParentThreadID != "" {
					parentSessionID = meta.ParentThreadID
					isSidechain = true
				}
				if len(meta.Source) > 0 {
					var srcObj struct {
						Subagent *struct {
							ThreadSpawn *struct {
								ParentThreadID string `json:"parent_thread_id"`
							} `json:"thread_spawn"`
						} `json:"subagent"`
					}
					if json.Unmarshal(meta.Source, &srcObj) == nil && srcObj.Subagent != nil {
						isSidechain = true
						if srcObj.Subagent.ThreadSpawn != nil && srcObj.Subagent.ThreadSpawn.ParentThreadID != "" {
							parentSessionID = srcObj.Subagent.ThreadSpawn.ParentThreadID
						}
					}
				}
				if parentSessionID == "" && meta.SessionID != "" && meta.ID != "" && meta.SessionID != meta.ID {
					parentSessionID = meta.SessionID
					isSidechain = true
				}
				if meta.CWD != "" {
					cwd = meta.CWD
				}
				if meta.Originator != "" {
					originator = meta.Originator
				}
				if meta.Git.Branch != "" {
					gitBranch = meta.Git.Branch
				}
				if mt := parseFlexibleTime(meta.Timestamp); !mt.IsZero() && (startedAt.IsZero() || mt.Before(startedAt)) {
					startedAt = mt
				}
			}

		case "turn_context":
			var tc struct {
				CWD        string `json:"cwd"`
				Model      string `json:"model"`
				Effort     string `json:"effort"`
				Reasoning  struct {
					Effort string `json:"effort"`
				} `json:"reasoning"`
			}
			if json.Unmarshal(cl.Payload, &tc) == nil {
				if tc.CWD != "" && cwd == "" {
					cwd = tc.CWD
				}
				if tc.Model != "" {
					currentModel = tc.Model
				}
				if tc.Effort != "" {
					currentEff = tc.Effort
				} else if tc.Reasoning.Effort != "" {
					currentEff = tc.Reasoning.Effort
				}
			}

		case "token_usage_record":
			var rec struct {
				SessionID  string          `json:"session_id"`
				ResponseID string          `json:"response_id"`
				TurnID     string          `json:"turn_id"`
				Model      string          `json:"model"`
				Usage      codexTokenUsage `json:"usage"`
			}
			if json.Unmarshal(cl.Payload, &rec) == nil {
				if rec.SessionID != "" && nativeID == "" {
					nativeID = rec.SessionID
				}
				if rec.ResponseID != "" && seenRespIDs[rec.ResponseID] {
					return nil
				}
				if rec.ResponseID != "" {
					seenRespIDs[rec.ResponseID] = true
				}
				model := rec.Model
				if model == "" {
					model = currentModel
				}
				modelCounts[model]++
				uncachedIn := rec.Usage.InputTokens - rec.Usage.CachedInputTokens
				if uncachedIn < 0 {
					uncachedIn = 0
				}
				cost := r.Pricing.EstimateCost(model, uncachedIn, rec.Usage.CachedInputTokens, rec.Usage.CacheWriteInputTokens, 0, rec.Usage.OutputTokens)
				sid := ensureSessionID()
				evTS := ts
				if evTS.IsZero() {
					evTS = startedAt
				}
				primaryUsage = append(primaryUsage, protocol.UsageEventRecord{
					ID:                 stableID("use", sid, "tur", fmt.Sprintf("%d", lineNum), rec.ResponseID),
					SessionID:          sid,
					DeviceID:           in.DeviceID,
					Harness:            protocol.SourceCodex,
					Model:              model,
					RequestID:          rec.ResponseID,
					Timestamp:          evTS,
					InputTokens:        uncachedIn,
					CachedInputTokens:  rec.Usage.CachedInputTokens,
					CacheWrite5mTokens: rec.Usage.CacheWriteInputTokens,
					OutputTokens:       rec.Usage.OutputTokens,
					ReasoningTokens:    rec.Usage.ReasoningOutputTokens,
					ContextWindow:      contextWin,
					Effort:             currentEff,
					EstimatedCostUSD:   cost,
					RawUsageJSON:       string(cl.Payload),
				})
			}

		case "event_msg":
			var ev struct {
				Type               string `json:"type"`
				ModelContextWindow int64  `json:"model_context_window"`
				Info               *struct {
					TotalTokenUsage    codexTokenUsage `json:"total_token_usage"`
					LastTokenUsage     codexTokenUsage `json:"last_token_usage"`
					ModelContextWindow int64           `json:"model_context_window"`
				} `json:"info"`
				RateLimits json.RawMessage `json:"rate_limits"`
			}
			if json.Unmarshal(cl.Payload, &ev) != nil {
				return nil
			}
			if ev.ModelContextWindow > 0 {
				contextWin = ev.ModelContextWindow
			}
			if ev.Type == "token_count" {
				if ev.Info != nil {
					if ev.Info.ModelContextWindow > 0 {
						contextWin = ev.Info.ModelContextWindow
					}
					tot := ev.Info.TotalTokenUsage
					last := ev.Info.LastTokenUsage
					// Deduplicate repeated token_count events with unchanged totals
					if tot.InputTokens != prevTotalIn || tot.OutputTokens != prevTotalOut {
						prevTotalIn = tot.InputTokens
						prevTotalOut = tot.OutputTokens
						model := currentModel
						if model != "" {
							modelCounts[model]++
						}
						uncachedIn := last.InputTokens - last.CachedInputTokens
						if uncachedIn < 0 {
							uncachedIn = 0
						}
						cost := r.Pricing.EstimateCost(model, uncachedIn, last.CachedInputTokens, last.CacheWriteInputTokens, 0, last.OutputTokens)
						sid := ensureSessionID()
						evTS := ts
						if evTS.IsZero() {
							evTS = startedAt
						}
						fallbackUsage = append(fallbackUsage, protocol.UsageEventRecord{
							ID:                 stableID("use", sid, "tc", fmt.Sprintf("%d", lineNum)),
							SessionID:          sid,
							DeviceID:           in.DeviceID,
							Harness:            protocol.SourceCodex,
							Model:              model,
							Timestamp:          evTS,
							InputTokens:        uncachedIn,
							CachedInputTokens:  last.CachedInputTokens,
							CacheWrite5mTokens: last.CacheWriteInputTokens,
							OutputTokens:       last.OutputTokens,
							ReasoningTokens:    last.ReasoningOutputTokens,
							ContextWindow:      contextWin,
							Effort:             currentEff,
							EstimatedCostUSD:   cost,
							RawUsageJSON:       string(cl.Payload),
						})
					}
				}
				if len(ev.RateLimits) > 2 && string(ev.RateLimits) != "null" {
					var rl struct {
						LimitID  string `json:"limit_id"`
						PlanType string `json:"plan_type"`
						Primary  *struct {
							UsedPercent float64 `json:"used_percent"`
						} `json:"primary"`
						Secondary *struct {
							UsedPercent float64 `json:"used_percent"`
						} `json:"secondary"`
					}
					if json.Unmarshal(ev.RateLimits, &rl) == nil {
						sid := ensureSessionID()
						snap := protocol.RateLimitSnapshot{
							ID:        stableID("rl", sid, fmt.Sprintf("%d", lineNum)),
							DeviceID:  in.DeviceID,
							Harness:   protocol.SourceCodex,
							SessionID: sid,
							Timestamp: ts,
							LimitID:   rl.LimitID,
							PlanType:  rl.PlanType,
							RawJSON:   string(ev.RateLimits),
						}
						if rl.Primary != nil {
							v := rl.Primary.UsedPercent
							snap.PrimaryUsedPct = &v
						}
						if rl.Secondary != nil {
							v := rl.Secondary.UsedPercent
							snap.SecondaryUsedPct = &v
						}
						rateLimits = append(rateLimits, snap)
					}
				}
			}

		case "response_item":
			var item struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Role      string          `json:"role"`
				Status    string          `json:"status"`
				Name      string          `json:"name"`
				CallID    string          `json:"call_id"`
				Arguments string          `json:"arguments"`
				Input     json.RawMessage `json:"input"`
				Output    json.RawMessage `json:"output"`
				Content   json.RawMessage `json:"content"`
				Summary   []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"summary"`
			}
			if json.Unmarshal(cl.Payload, &item) != nil {
				return nil
			}
			sid := ensureSessionID()
			var tsPtr *time.Time
			if !ts.IsZero() {
				tCopy := ts
				tsPtr = &tCopy
			}

			switch item.Type {
			case "message":
				text := extractCodexContentText(item.Content)
				role := item.Role
				if role == "" {
					role = "assistant"
				}
				// Skip developer/system preamble when picking session title
				if firstTitle == "" && role == "user" && strings.TrimSpace(text) != "" && !strings.HasPrefix(strings.TrimSpace(text), "# AGENTS.md") && !strings.HasPrefix(strings.TrimSpace(text), "<environment_context>") {
					firstTitle = truncateTitle(text, 100)
				}
				ord := len(messages)
				messages = append(messages, protocol.MessageRecord{
					ID:              stableID("msg", sid, fmt.Sprintf("%d", ord), item.ID),
					SessionID:       sid,
					Ordinal:         ord,
					NativeMessageID: item.ID,
					Role:            role,
					Model:           currentModel,
					ContentText:     text,
					ContentJSON:     string(item.Content),
					Timestamp:       tsPtr,
				})

			case "reasoning":
				var parts []string
				for _, sm := range item.Summary {
					if sm.Text != "" {
						parts = append(parts, sm.Text)
					}
				}
				if len(parts) > 0 {
					ord := len(messages)
					messages = append(messages, protocol.MessageRecord{
						ID:              stableID("msg", sid, fmt.Sprintf("%d", ord), item.ID),
						SessionID:       sid,
						Ordinal:         ord,
						NativeMessageID: item.ID,
						Role:            "assistant",
						Model:           currentModel,
						ThinkingText:    strings.Join(parts, "\n"),
						Timestamp:       tsPtr,
					})
				}

			case "function_call", "custom_tool_call":
				inputStr := item.Arguments
				if inputStr == "" && len(item.Input) > 0 {
					inputStr = string(item.Input)
				}
				ord := len(toolCalls)
				tc := protocol.ToolCallRecord{
					ID:        stableID("tc", sid, fmt.Sprintf("%d", ord), item.CallID),
					SessionID: sid,
					Ordinal:   ord,
					CallID:    item.CallID,
					ToolName:  item.Name,
					InputJSON: inputStr,
					Status:    item.Status,
					Timestamp: tsPtr,
				}
				toolCalls = append(toolCalls, tc)
				if item.CallID != "" {
					toolCallIdxByCallID[item.CallID] = ord
				}

			case "function_call_output", "custom_tool_call_output":
				outText := extractRawStringOrJSON(item.Output)
				if idx, ok := toolCallIdxByCallID[item.CallID]; ok {
					toolCalls[idx].OutputText = outText
				} else {
					ord := len(toolCalls)
					toolCalls = append(toolCalls, protocol.ToolCallRecord{
						ID:         stableID("tc", sid, fmt.Sprintf("%d", ord), item.CallID),
						SessionID:  sid,
						Ordinal:    ord,
						CallID:     item.CallID,
						ToolName:   "tool_output",
						OutputText: outText,
						Timestamp:  tsPtr,
					})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Pick canonical usage events (prefer token_usage_record if present)
	usageEvents := primaryUsage
	if len(usageEvents) == 0 {
		usageEvents = fallbackUsage
	}

	if len(messages) == 0 && len(usageEvents) == 0 && len(toolCalls) == 0 {
		return &store.ParsedFileBundle{}, nil
	}

	if nativeID == "" {
		nativeID = base
	}
	sid := ensureSessionID()
	// Ensure all child records carry final sessionID
	for i := range messages {
		messages[i].SessionID = sid
	}
	for i := range toolCalls {
		toolCalls[i].SessionID = sid
	}
	for i := range usageEvents {
		usageEvents[i].SessionID = sid
	}
	for i := range rateLimits {
		rateLimits[i].SessionID = sid
	}

	var inTok, cachedTok, cw5Tok, cw1Tok, outTok, reasTok int64
	var estCost float64
	for _, u := range usageEvents {
		inTok += u.InputTokens
		cachedTok += u.CachedInputTokens
		cw5Tok += u.CacheWrite5mTokens
		cw1Tok += u.CacheWrite1hTokens
		outTok += u.OutputTokens
		reasTok += u.ReasoningTokens
		estCost += u.EstimatedCostUSD
	}

	var endedPtr *time.Time
	if !endedAt.IsZero() {
		tCopy := endedAt
		endedPtr = &tCopy
	}
	title := firstTitle
	if title == "" {
		title = "Codex " + nativeID
	}

	sess := protocol.SessionSummary{
		ID:                 sid,
		NativeID:           nativeID,
		DeviceID:           in.DeviceID,
		Harness:            protocol.SourceCodex,
		SourceFileID:       in.FileID,
		ProjectPath:        cwd,
		ProjectName:        deriveProjectName(cwd),
		GitBranch:          gitBranch,
		Title:              title,
		ModelPrimary:       mostFrequentModel(modelCounts),
		ParentSessionID:    parentSessionID,
		IsSidechain:        isSidechain,
		Originator:         originator,
		StartedAt:          startedAt,
		EndedAt:            endedPtr,
		MessageCount:       len(messages),
		ToolCallCount:      len(toolCalls),
		APICallCount:       len(usageEvents),
		InputTokens:        inTok,
		CachedInputTokens:  cachedTok,
		CacheWrite5mTokens: cw5Tok,
		CacheWrite1hTokens: cw1Tok,
		OutputTokens:       outTok,
		ReasoningTokens:    reasTok,
		EstimatedCostUSD:   estCost,
	}

	return &store.ParsedFileBundle{
		Sessions:   []protocol.SessionSummary{sess},
		Messages:   messages,
		ToolCalls:  toolCalls,
		Usage:      usageEvents,
		RateLimits: rateLimits,
	}, nil
}

func extractCodexContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asStr string
	if json.Unmarshal(raw, &asStr) == nil {
		return asStr
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

func extractRawStringOrJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}
