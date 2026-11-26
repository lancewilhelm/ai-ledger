package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/store"
)

type copilotToolRequest struct {
	ToolCallID string
	Name       string
	Arguments  json.RawMessage
}

type copilotEvent struct {
	ID        string
	Type      string
	Timestamp string
	Data      struct {
		Title            string
		Content          string
		ReasoningText    string
		Model            string
		SelectedModel    string
		NewModel         string
		MessageID        string
		InputTokens      int64
		OutputTokens     int64
		CacheReadTokens  int64
		CacheWriteTokens int64
		ReasoningTokens  int64
		APIEndpoint      string
		APICallID        string
		Cwd              string
		Branch           string
		Context          struct{ Cwd, Branch string }
		ToolRequests     []copilotToolRequest
		ToolCallID       string
		ToolName         string
		Arguments        json.RawMessage
		Success          *bool
		Result           struct{ Content, DetailedContent string }
		Error            struct{ Message string }
	}
}

func (r *Registry) parseCopilotCLI(ctx context.Context, in FileInput) (*store.ParsedFileBundle, error) {
	f, err := os.Open(in.AbsPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	bundle := &store.ParsedFileBundle{}
	s := newEditorSession(in, path.Base(path.Dir(in.RelPath)))
	model := ""
	models := make(map[string]int)
	toolIndexes := make(map[string]int)
	seen := make(map[string]bool)
	usageIDs := make(map[string]bool)
	err = scanJSONL(f, func(line int, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var ev copilotEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil // The current last event may still be in flight.
		}
		if ev.ID != "" {
			if seen[ev.ID] {
				return nil
			}
			seen[ev.ID] = true
		}
		data := ev.Data
		ts := parseFlexibleTime(ev.Timestamp)
		s.observeTime(ts)
		switch ev.Type {
		case "session.start", "session.context_changed":
			s.summary.ProjectPath = firstNonEmpty(data.Context.Cwd, data.Cwd, s.summary.ProjectPath)
			s.summary.GitBranch = firstNonEmpty(data.Context.Branch, data.Branch, s.summary.GitBranch)
			model = firstNonEmpty(data.SelectedModel, data.Model, model)
		case "session.title_changed":
			s.summary.Title = firstNonEmpty(data.Title, s.summary.Title)
		case "session.model_change", "model.change":
			model = firstNonEmpty(data.NewModel, data.Model, model)
		case "user.message":
			s.addMessage(protocol.MessageRecord{
				Role: "user", NativeMessageID: firstNonEmpty(data.MessageID, ev.ID),
				ContentText: data.Content, Timestamp: timestampPtr(ts),
			})
		case "assistant.message":
			messageModel := firstNonEmpty(data.Model, model)
			models[messageModel]++
			messageID := s.addMessage(protocol.MessageRecord{
				Role: "assistant", NativeMessageID: firstNonEmpty(data.MessageID, ev.ID),
				Model: messageModel, ContentText: data.Content, ThinkingText: data.ReasoningText, Timestamp: timestampPtr(ts),
			})
			for _, tool := range data.ToolRequests {
				s.addCopilotTool(toolIndexes, protocol.ToolCallRecord{
					MessageID: messageID, CallID: tool.ToolCallID, ToolName: tool.Name,
					InputJSON: string(tool.Arguments), Timestamp: timestampPtr(ts),
				})
			}
		case "tool.execution_start", "tool.user_requested":
			s.addCopilotTool(toolIndexes, protocol.ToolCallRecord{
				CallID: data.ToolCallID, ToolName: data.ToolName,
				InputJSON: string(data.Arguments), Timestamp: timestampPtr(ts),
			})
		case "tool.execution_complete":
			if idx, ok := toolIndexes[data.ToolCallID]; ok {
				tool := &s.tools[idx]
				tool.OutputText = firstNonEmpty(data.Result.DetailedContent, data.Result.Content, data.Error.Message)
				tool.Status = "completed"
				tool.IsError = data.Success != nil && !*data.Success
			}
		case "assistant.usage":
			if ts.IsZero() || data.InputTokens+data.OutputTokens+data.CacheReadTokens+data.CacheWriteTokens == 0 {
				break
			}
			useID := firstNonEmpty(data.APICallID, ev.ID, strconv.Itoa(line))
			if usageIDs[useID] {
				break
			}
			usageIDs[useID] = true
			uncached := data.InputTokens
			if !strings.HasSuffix(data.APIEndpoint, "/messages") {
				uncached = max(0, uncached-data.CacheReadTokens-data.CacheWriteTokens)
			}
			usageModel := firstNonEmpty(data.Model, model)
			models[usageModel]++
			cost := r.Pricing.EstimateCost(usageModel, uncached, data.CacheReadTokens, data.CacheWriteTokens, 0, data.OutputTokens)
			bundle.Usage = append(bundle.Usage, protocol.UsageEventRecord{
				ID: stableID("use", s.summary.ID, useID), SessionID: s.summary.ID,
				DeviceID: in.DeviceID, Harness: in.Source, Model: usageModel, RequestID: data.APICallID,
				Timestamp: ts, InputTokens: uncached, OutputTokens: data.OutputTokens,
				CachedInputTokens: data.CacheReadTokens, CacheWrite5mTokens: data.CacheWriteTokens,
				ReasoningTokens: data.ReasoningTokens, EstimatedCostUSD: cost,
			})
			s.summary.InputTokens += uncached
			s.summary.OutputTokens += data.OutputTokens
			s.summary.CachedInputTokens += data.CacheReadTokens
			s.summary.CacheWrite5mTokens += data.CacheWriteTokens
			s.summary.ReasoningTokens += data.ReasoningTokens
			s.summary.EstimatedCostUSD += cost
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read Copilot CLI events: %w", err)
	}
	if len(s.messages) > 0 || len(s.tools) > 0 || len(bundle.Usage) > 0 {
		s.summary.APICallCount = len(bundle.Usage)
		s.summary.ProjectName = deriveProjectName(s.summary.ProjectPath)
		s.summary.ModelPrimary = firstNonEmpty(mostFrequentModel(models), model)
		s.appendTo(bundle)
	}
	return bundle, nil
}

func (s *editorSession) addCopilotTool(indexes map[string]int, tool protocol.ToolCallRecord) {
	if tool.CallID == "" {
		return
	}
	if idx, ok := indexes[tool.CallID]; ok {
		prior := &s.tools[idx]
		prior.MessageID = firstNonEmpty(prior.MessageID, tool.MessageID)
		prior.ToolName = firstNonEmpty(prior.ToolName, tool.ToolName)
		prior.InputJSON = firstNonEmpty(prior.InputJSON, tool.InputJSON)
		return
	}
	indexes[tool.CallID] = len(s.tools)
	s.addTool(tool)
}
