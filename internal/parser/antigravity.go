package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/store"
)

type antigravityStep struct {
	StepIndex       int             `json:"step_index"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Status          string          `json:"status"`
	CreatedAt       string          `json:"created_at"`
	Model           string          `json:"model"`
	InputTokens     int64           `json:"input_tokens"`
	CacheReadTokens int64           `json:"cache_read_tokens"`
	OutputTokens    int64           `json:"output_tokens"`
	Thinking        string          `json:"thinking"`
	Content         string          `json:"content"`
	ToolCalls       json.RawMessage `json:"tool_calls"`
}

func (r *Registry) parseAntigravity(ctx context.Context, in FileInput) (*store.ParsedFileBundle, error) {
	// Only parse transcript_full.jsonl as the canonical full transcript (chunks/compact transcripts are still archived raw!)
	if !strings.HasSuffix(in.RelPath, "transcript_full.jsonl") {
		return &store.ParsedFileBundle{}, nil
	}

	rc, err := openMaybeZstd(in.AbsPath)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	nativeID := extractAntigravityBrainID(in.RelPath)
	if nativeID == "" {
		nativeID = in.RelPath
	}
	sid := stableID("sess", in.DeviceID, protocol.SourceAntigravity, nativeID)

	var (
		startedAt  time.Time
		endedAt    time.Time
		firstTitle string
		cwd        string
	)
	modelCounts := make(map[string]int)
	var messages []protocol.MessageRecord
	var toolCalls []protocol.ToolCallRecord
	var usageEvents []protocol.UsageEventRecord
	pendingToolStart := 0

	err = scanJSONL(rc, func(lineNum int, raw []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var step antigravityStep
		if err := json.Unmarshal(raw, &step); err != nil {
			return nil
		}
		ts := parseFlexibleTime(step.CreatedAt)
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

		switch step.Type {
		case "USER_INPUT":
			text := step.Content
			if firstTitle == "" && strings.TrimSpace(text) != "" {
				firstTitle = truncateTitle(text, 100)
			}
			if cwd == "" {
				cwd = extractAntigravityWorkspace(text)
			}
			ord := len(messages)
			messages = append(messages, protocol.MessageRecord{
				ID:          stableID("msg", sid, fmt.Sprintf("%d", ord), fmt.Sprintf("step:%d", step.StepIndex)),
				SessionID:   sid,
				Ordinal:     ord,
				Role:        "user",
				ContentText: text,
				Timestamp:   tsPtr,
			})

		case "PLANNER_RESPONSE":
			model := step.Model
			if model != "" {
				modelCounts[model]++
			}
			ord := len(messages)
			msgID := stableID("msg", sid, fmt.Sprintf("%d", ord), fmt.Sprintf("step:%d", step.StepIndex))
			messages = append(messages, protocol.MessageRecord{
				ID:           msgID,
				SessionID:    sid,
				Ordinal:      ord,
				Role:         "assistant",
				Model:        model,
				ContentText:  step.Content,
				ThinkingText: step.Thinking,
				ContentJSON:  string(step.ToolCalls),
				Timestamp:    tsPtr,
			})

			// Parse tool calls emitted in this planner step
			pendingToolStart = len(toolCalls)
			if len(step.ToolCalls) > 2 {
				var tcs []map[string]any
				if json.Unmarshal(step.ToolCalls, &tcs) == nil {
					for j, tc := range tcs {
						name, _ := tc["name"].(string)
						if name == "" {
							name, _ = tc["tool"].(string)
						}
						if name == "" {
							name = "tool_call"
						}
						callID, _ := tc["id"].(string)
						argsBytes, _ := json.Marshal(tc)
						tcOrd := len(toolCalls)
						toolCalls = append(toolCalls, protocol.ToolCallRecord{
							ID:        stableID("tc", sid, fmt.Sprintf("%d", tcOrd), fmt.Sprintf("%d:%d", step.StepIndex, j)),
							SessionID: sid,
							MessageID: msgID,
							Ordinal:   tcOrd,
							CallID:    callID,
							ToolName:  name,
							InputJSON: string(argsBytes),
							Status:    step.Status,
							Timestamp: tsPtr,
						})
					}
				}
			}

			// Emit per-step API usage event
			if step.InputTokens > 0 || step.CacheReadTokens > 0 || step.OutputTokens > 0 {
				cost := r.Pricing.EstimateCost(model, step.InputTokens, step.CacheReadTokens, 0, 0, step.OutputTokens)
				evTS := ts
				if evTS.IsZero() {
					evTS = startedAt
				}
				rawUsage, _ := json.Marshal(map[string]any{
					"step_index":        step.StepIndex,
					"model":             model,
					"input_tokens":      step.InputTokens,
					"cache_read_tokens": step.CacheReadTokens,
					"output_tokens":     step.OutputTokens,
				})
				usageEvents = append(usageEvents, protocol.UsageEventRecord{
					ID:                stableID("use", sid, fmt.Sprintf("step:%d", step.StepIndex)),
					SessionID:         sid,
					DeviceID:          in.DeviceID,
					Harness:           protocol.SourceAntigravity,
					Model:             model,
					Timestamp:         evTS,
					InputTokens:       step.InputTokens,
					CachedInputTokens: step.CacheReadTokens,
					OutputTokens:      step.OutputTokens,
					EstimatedCostUSD:  cost,
					RawUsageJSON:      string(rawUsage),
				})
			}

		case "GENERIC":
			// Attach tool output to the next unmatched pending tool call from the previous planner response
			if pendingToolStart < len(toolCalls) {
				toolCalls[pendingToolStart].OutputText = step.Content
				pendingToolStart++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(messages) == 0 && len(usageEvents) == 0 {
		return &store.ParsedFileBundle{}, nil
	}

	var inTok, cachedTok, outTok int64
	var estCost float64
	for _, u := range usageEvents {
		inTok += u.InputTokens
		cachedTok += u.CachedInputTokens
		outTok += u.OutputTokens
		estCost += u.EstimatedCostUSD
	}

	title := firstTitle
	if title == "" {
		title = "Antigravity " + nativeID
	}
	var endedPtr *time.Time
	if !endedAt.IsZero() {
		tCopy := endedAt
		endedPtr = &tCopy
	}

	sess := protocol.SessionSummary{
		ID:                sid,
		NativeID:          nativeID,
		DeviceID:          in.DeviceID,
		Harness:           protocol.SourceAntigravity,
		SourceFileID:      in.FileID,
		ProjectPath:       cwd,
		ProjectName:       deriveProjectName(cwd),
		Title:             title,
		ModelPrimary:      mostFrequentModel(modelCounts),
		Originator:        "T3 Code",
		StartedAt:         startedAt,
		EndedAt:           endedPtr,
		MessageCount:      len(messages),
		ToolCallCount:     len(toolCalls),
		APICallCount:      len(usageEvents),
		InputTokens:       inTok,
		CachedInputTokens: cachedTok,
		OutputTokens:      outTok,
		EstimatedCostUSD:  estCost,
	}

	return &store.ParsedFileBundle{
		Sessions:  []protocol.SessionSummary{sess},
		Messages:  messages,
		ToolCalls: toolCalls,
		Usage:     usageEvents,
	}, nil
}

func extractAntigravityBrainID(relPath string) string {
	norm := strings.ReplaceAll(relPath, "\\", "/")
	parts := strings.Split(norm, "/")
	for i, p := range parts {
		if p == "brain" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func extractAntigravityWorkspace(text string) string {
	for _, marker := range []string{"Command Working Directory: ", "Active Workspaces:\n- "} {
		if idx := strings.Index(text, marker); idx >= 0 {
			rest := text[idx+len(marker):]
			if nl := strings.IndexAny(rest, "\r\n"); nl >= 0 {
				rest = rest[:nl]
			}
			rest = strings.TrimSpace(rest)
			if strings.HasPrefix(rest, "/") {
				return rest
			}
		}
	}
	return ""
}
