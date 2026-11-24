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

type claudeLine struct {
	Type               string          `json:"type"`
	UUID               string          `json:"uuid"`
	ParentUUID         string          `json:"parentUuid"`
	SessionID          string          `json:"sessionId"`
	IsSidechain        bool            `json:"isSidechain"`
	Timestamp          string          `json:"timestamp"`
	CWD                string          `json:"cwd"`
	GitBranch          string          `json:"gitBranch"`
	Version            string          `json:"version"`
	Entrypoint         string          `json:"entrypoint"`
	RequestID          string          `json:"requestId"`
	Effort             string          `json:"effort"`
	ThinkingDurationMs int64           `json:"thinkingDurationMs"`
	CustomTitle        string          `json:"customTitle"`
	TotalCostUSD       *float64        `json:"totalCostUSD"`
	Message            json.RawMessage `json:"message"`
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	OutputTokensDetails      struct {
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	CacheCreation struct {
		Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	ServerToolUse struct {
		WebSearchRequests int `json:"web_search_requests"`
		WebFetchRequests  int `json:"web_fetch_requests"`
	} `json:"server_tool_use"`
	ServiceTier string `json:"service_tier"`
}

type claudeAssistantMsg struct {
	ID         string            `json:"id"`
	Model      string            `json:"model"`
	Role       string            `json:"role"`
	StopReason string            `json:"stop_reason"`
	Content    json.RawMessage   `json:"content"`
	Usage      *claudeUsage      `json:"usage"`
}

type claudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (r *Registry) parseClaude(ctx context.Context, in FileInput) (*store.ParsedFileBundle, error) {
	rc, err := openMaybeZstd(in.AbsPath)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	base := strings.TrimSuffix(filepath.Base(in.RelPath), ".jsonl")
	var (
		nativeID     string
		cwd          string
		gitBranch    string
		customTitle  string
		firstTitle   string
		originator   string
		isSidechain  bool
		startedAt    time.Time
		endedAt      time.Time
		reportedCost *float64
	)

	// Claude emits multiple assistant JSONL rows per API call (one per content block: thinking, text, tool_use)
	// sharing the same requestId / message.id. We aggregate them by callKey to avoid double-counting tokens!
	type mergedAssistant struct {
		msgIdx    int
		usageIdx  int
		texts     []string
		thinkings []string
		blocks    []json.RawMessage
	}

	var messages []protocol.MessageRecord
	var toolCalls []protocol.ToolCallRecord
	var usageEvents []protocol.UsageEventRecord
	modelCounts := make(map[string]int)

	asstByKey := make(map[string]*mergedAssistant)
	toolCallIdxByID := make(map[string]int)

	sessionID := ""
	ensureSessionID := func() string {
		if sessionID == "" {
			nid := nativeID
			if nid == "" {
				nid = base
			}
			// Include relPath discriminator for sidechains so subagent files don't collide with parent session
			if isSidechain || strings.Contains(in.RelPath, "subagents") {
				sessionID = stableID("sess", in.DeviceID, protocol.SourceClaude, in.RelPath)
			} else {
				sessionID = stableID("sess", in.DeviceID, protocol.SourceClaude, nid)
			}
		}
		return sessionID
	}

	err = scanJSONL(rc, func(lineNum int, raw []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var cl claudeLine
		if err := json.Unmarshal(raw, &cl); err != nil {
			return nil
		}
		if cl.SessionID != "" && nativeID == "" {
			nativeID = cl.SessionID
		}
		if cl.IsSidechain {
			isSidechain = true
		}
		if cl.CWD != "" && cwd == "" {
			cwd = cl.CWD
		}
		if cl.GitBranch != "" && gitBranch == "" {
			gitBranch = cl.GitBranch
		}
		if cl.Entrypoint != "" && originator == "" {
			originator = cl.Entrypoint
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
		var tsPtr *time.Time
		if !ts.IsZero() {
			tCopy := ts
			tsPtr = &tCopy
		}

		switch cl.Type {
		case "custom-title":
			if cl.CustomTitle != "" {
				customTitle = cl.CustomTitle
			}

		case "cost-state":
			if cl.TotalCostUSD != nil {
				v := *cl.TotalCostUSD
				reportedCost = &v
			}

		case "user":
			var um struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(cl.Message, &um) != nil {
				return nil
			}
			sid := ensureSessionID()
			var userText string
			var asStr string
			if json.Unmarshal(um.Content, &asStr) == nil {
				userText = asStr
			} else {
				var blocks []claudeContentBlock
				if json.Unmarshal(um.Content, &blocks) == nil {
					var parts []string
					for _, b := range blocks {
						switch b.Type {
						case "text":
							if b.Text != "" {
								parts = append(parts, b.Text)
							}
						case "tool_result":
							outStr := extractClaudeToolResult(b.Content)
							if idx, ok := toolCallIdxByID[b.ToolUseID]; ok {
								toolCalls[idx].OutputText = outStr
								toolCalls[idx].IsError = b.IsError
								if b.IsError {
									toolCalls[idx].Status = "error"
								} else if toolCalls[idx].Status == "" {
									toolCalls[idx].Status = "completed"
								}
							}
						}
					}
					userText = strings.Join(parts, "\n")
				}
			}
			if strings.TrimSpace(userText) != "" {
				if firstTitle == "" {
					firstTitle = truncateTitle(userText, 100)
				}
				ord := len(messages)
				messages = append(messages, protocol.MessageRecord{
					ID:              stableID("msg", sid, fmt.Sprintf("%d", ord), cl.UUID),
					SessionID:       sid,
					Ordinal:         ord,
					NativeMessageID: cl.UUID,
					ParentMessageID: cl.ParentUUID,
					Role:            "user",
					ContentText:     userText,
					ContentJSON:     string(um.Content),
					Timestamp:       tsPtr,
				})
			}

		case "assistant":
			var am claudeAssistantMsg
			if json.Unmarshal(cl.Message, &am) != nil {
				return nil
			}
			sid := ensureSessionID()
			callKey := cl.RequestID
			if callKey == "" {
				callKey = am.ID
			}
			if callKey == "" {
				callKey = fmt.Sprintf("line:%d", lineNum)
			}

			ma, exists := asstByKey[callKey]
			if !exists {
				ord := len(messages)
				msgID := stableID("msg", sid, fmt.Sprintf("%d", ord), callKey)
				messages = append(messages, protocol.MessageRecord{
					ID:              msgID,
					SessionID:       sid,
					Ordinal:         ord,
					NativeMessageID: am.ID,
					ParentMessageID: cl.ParentUUID,
					Role:            "assistant",
					Model:           am.Model,
					StopReason:      am.StopReason,
					Timestamp:       tsPtr,
					DurationMs:      cl.ThinkingDurationMs,
				})
				ma = &mergedAssistant{
					msgIdx:   ord,
					usageIdx: -1,
				}
				asstByKey[callKey] = ma
				if am.Model != "" {
					modelCounts[am.Model]++
				}
			} else {
				if am.StopReason != "" {
					messages[ma.msgIdx].StopReason = am.StopReason
				}
				if cl.ThinkingDurationMs > messages[ma.msgIdx].DurationMs {
					messages[ma.msgIdx].DurationMs = cl.ThinkingDurationMs
				}
			}

			// Extract content blocks
			var rawBlocks []json.RawMessage
			if json.Unmarshal(am.Content, &rawBlocks) == nil {
				for _, rb := range rawBlocks {
					ma.blocks = append(ma.blocks, rb)
					var b claudeContentBlock
					if json.Unmarshal(rb, &b) != nil {
						continue
					}
					switch b.Type {
					case "text":
						if b.Text != "" {
							ma.texts = append(ma.texts, b.Text)
						}
					case "thinking":
						if b.Thinking != "" {
							ma.thinkings = append(ma.thinkings, b.Thinking)
						}
					case "tool_use":
						if b.ID != "" {
							if _, dup := toolCallIdxByID[b.ID]; dup {
								continue
							}
						}
						tcOrd := len(toolCalls)
						tc := protocol.ToolCallRecord{
							ID:        stableID("tc", sid, fmt.Sprintf("%d", tcOrd), b.ID),
							SessionID: sid,
							MessageID: messages[ma.msgIdx].ID,
							Ordinal:   tcOrd,
							CallID:    b.ID,
							ToolName:  b.Name,
							InputJSON: string(b.Input),
							Timestamp: tsPtr,
						}
						toolCalls = append(toolCalls, tc)
						if b.ID != "" {
							toolCallIdxByID[b.ID] = tcOrd
						}
					}
				}
			} else {
				var asStr string
				if json.Unmarshal(am.Content, &asStr) == nil && asStr != "" {
					ma.texts = append(ma.texts, asStr)
				}
			}

			messages[ma.msgIdx].ContentText = strings.Join(ma.texts, "\n")
			messages[ma.msgIdx].ThinkingText = strings.Join(ma.thinkings, "\n")
			if len(ma.blocks) > 0 {
				if cj, err := json.Marshal(ma.blocks); err == nil {
					messages[ma.msgIdx].ContentJSON = string(cj)
				}
			}

			// Deduplicate & keep latest/max usage for this API call
			if am.Usage != nil {
				u := am.Usage
				cw5 := u.CacheCreation.Ephemeral5mInputTokens
				cw1 := u.CacheCreation.Ephemeral1hInputTokens
				if cw5 == 0 && cw1 == 0 && u.CacheCreationInputTokens > 0 {
					cw5 = u.CacheCreationInputTokens
				}
				rawUsageBytes, _ := json.Marshal(u)
				cost := r.Pricing.EstimateCost(am.Model, u.InputTokens, u.CacheReadInputTokens, cw5, cw1, u.OutputTokens)
				evTS := ts
				if evTS.IsZero() {
					evTS = startedAt
				}
				ev := protocol.UsageEventRecord{
					ID:                 stableID("use", sid, callKey),
					SessionID:          sid,
					DeviceID:           in.DeviceID,
					Harness:            protocol.SourceClaude,
					Model:              am.Model,
					RequestID:          cl.RequestID,
					Timestamp:          evTS,
					InputTokens:        u.InputTokens,
					CachedInputTokens:  u.CacheReadInputTokens,
					CacheWrite5mTokens: cw5,
					CacheWrite1hTokens: cw1,
					OutputTokens:       u.OutputTokens,
					ReasoningTokens:    u.OutputTokensDetails.ThinkingTokens,
					WebSearchRequests:  u.ServerToolUse.WebSearchRequests,
					WebFetchRequests:   u.ServerToolUse.WebFetchRequests,
					Effort:             cl.Effort,
					ServiceTier:        u.ServiceTier,
					EstimatedCostUSD:   cost,
					RawUsageJSON:       string(rawUsageBytes),
				}
				if ma.usageIdx < 0 {
					ma.usageIdx = len(usageEvents)
					usageEvents = append(usageEvents, ev)
				} else {
					// Update with final block's usage (which has final output_tokens)
					if ev.OutputTokens >= usageEvents[ma.usageIdx].OutputTokens {
						usageEvents[ma.usageIdx] = ev
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(messages) == 0 && len(usageEvents) == 0 && len(toolCalls) == 0 {
		return &store.ParsedFileBundle{}, nil
	}

	if nativeID == "" {
		nativeID = base
	}
	sid := ensureSessionID()
	for i := range messages {
		messages[i].SessionID = sid
	}
	for i := range toolCalls {
		toolCalls[i].SessionID = sid
	}
	for i := range usageEvents {
		usageEvents[i].SessionID = sid
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

	title := customTitle
	if title == "" {
		title = firstTitle
	}
	if title == "" {
		title = "Claude " + nativeID
	}

	projName := deriveProjectName(cwd)
	if projName == "" {
		// Derive from Claude projects folder slug (-Users-foo-projects-bar)
		dir := filepath.Base(filepath.Dir(in.RelPath))
		if dir != "" && dir != "." && dir != "subagents" {
			projName = dir
		}
	}

	var endedPtr *time.Time
	if !endedAt.IsZero() {
		tCopy := endedAt
		endedPtr = &tCopy
	}

	sess := protocol.SessionSummary{
		ID:                 sid,
		NativeID:           nativeID,
		DeviceID:           in.DeviceID,
		Harness:            protocol.SourceClaude,
		SourceFileID:       in.FileID,
		ProjectPath:        cwd,
		ProjectName:        projName,
		GitBranch:          gitBranch,
		Title:              title,
		ModelPrimary:       mostFrequentModel(modelCounts),
		IsSidechain:        isSidechain || strings.Contains(in.RelPath, "subagents"),
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
		ReportedCostUSD:    reportedCost,
	}

	return &store.ParsedFileBundle{
		Sessions:  []protocol.SessionSummary{sess},
		Messages:  messages,
		ToolCalls: toolCalls,
		Usage:     usageEvents,
	}, nil
}

func extractClaudeToolResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
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
