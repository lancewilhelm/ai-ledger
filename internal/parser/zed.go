package parser

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/store"
)

type zedThread struct {
	Title   string
	Summary string
	Model   struct {
		Provider string
		Model    string
	}
	InitialProjectSnapshot struct {
		Worktrees []struct {
			Path string `json:"worktree_path"`
		} `json:"worktree_snapshots"`
	} `json:"initial_project_snapshot"`
	Messages []json.RawMessage
	Usage    struct {
		Input      int64 `json:"input_tokens"`
		Output     int64 `json:"output_tokens"`
		CacheRead  int64 `json:"cache_read_input_tokens"`
		CacheWrite int64 `json:"cache_creation_input_tokens"`
	} `json:"cumulative_token_usage"`
}

type zedToolUse struct {
	ID       string
	Name     string
	Input    json.RawMessage
	RawInput string `json:"raw_input"`
}

type zedToolResult struct {
	ID      string `json:"tool_use_id"`
	Content json.RawMessage
	IsError bool `json:"is_error"`
}

type zedMessage struct {
	ID      json.RawMessage
	Role    string
	Content []struct {
		Text     string
		Thinking struct{ Text string }
		Mention  struct{ Content string }
		ToolUse  zedToolUse
	}
	Segments []struct {
		Type string
		Text string
	}
	Context     string
	ToolUses    []zedToolUse    `json:"tool_uses"`
	ToolResults json.RawMessage `json:"tool_results"`
}

func (r *Registry) parseZed(ctx context.Context, in FileInput) (*store.ParsedFileBundle, error) {
	if filepath.Ext(in.RelPath) == ".json" {
		return parseZedLegacy(in)
	}
	dbPath, cleanup, err := prepareSQLitePath(in.AbsPath)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	dsn := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// The original schema lacked created_at and the fields for worktrees and child threads.
	optional := []string{"created_at", "folder_paths", "parent_id", "worktree_branch"}
	columns := make([]string, 0, len(optional))
	for _, name := range optional {
		var present bool
		err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_table_info('threads') WHERE name = ?)", name).Scan(&present)
		if err != nil {
			return nil, err
		}
		expr := "''"
		if present {
			expr = "COALESCE(" + name + ", '')"
		}
		columns = append(columns, expr)
	}
	rows, err := db.QueryContext(ctx, "SELECT id, summary, updated_at, data_type, data, "+strings.Join(columns, ", ")+" FROM threads ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer decoder.Close()

	bundle := &store.ParsedFileBundle{}
	for rows.Next() {
		var id, title, updated, encoding, created, project, parent, branch string
		var data []byte
		if err := rows.Scan(&id, &title, &updated, &encoding, &data, &created, &project, &parent, &branch); err != nil {
			return nil, err
		}
		switch encoding {
		case "zstd":
			data, err = decoder.DecodeAll(data, nil)
			if err != nil {
				return nil, fmt.Errorf("decode Zed thread %s: %w", id, err)
			}
		case "json":
		default:
			return nil, fmt.Errorf("unsupported Zed thread encoding %q", encoding)
		}
		var thread zedThread
		if err := json.Unmarshal(data, &thread); err != nil {
			return nil, fmt.Errorf("read Zed thread %s: %w", id, err)
		}
		s := newEditorSession(in, id)
		s.summary.Title = firstNonEmpty(thread.Title, thread.Summary, title)
		s.summary.ModelPrimary = thread.Model.Model
		s.summary.Originator = thread.Model.Provider
		s.summary.GitBranch = branch
		s.summary.ParentSessionID = parent
		s.summary.IsSidechain = parent != ""
		s.summary.ProjectPath = strings.SplitN(project, "\n", 2)[0]
		if worktrees := thread.InitialProjectSnapshot.Worktrees; len(worktrees) > 0 {
			s.summary.ProjectPath = firstNonEmpty(s.summary.ProjectPath, worktrees[0].Path)
		}
		s.summary.ProjectName = deriveProjectName(s.summary.ProjectPath)
		s.summary.StartedAt = parseFlexibleTime(firstNonEmpty(created, updated))
		s.summary.EndedAt = timestampPtr(parseFlexibleTime(updated))

		for _, msg := range thread.Messages {
			if err := s.addZedMessage(msg); err != nil {
				return nil, fmt.Errorf("read Zed thread %s message: %w", id, err)
			}
		}
		usage := thread.Usage
		if usage.Input+usage.Output+usage.CacheRead+usage.CacheWrite > 0 {
			s.summary.InputTokens = usage.Input
			s.summary.OutputTokens = usage.Output
			s.summary.CachedInputTokens = usage.CacheRead
			s.summary.CacheWrite5mTokens = usage.CacheWrite
			s.summary.EstimatedCostUSD = r.Pricing.EstimateCost(thread.Model.Model, usage.Input, usage.CacheRead, usage.CacheWrite, 0, usage.Output)
			// Zed stores cumulative usage without individual API timestamps or model attribution.
			// The session's last model and update time are the available attribution.
			bundle.Usage = append(bundle.Usage, protocol.UsageEventRecord{
				ID: stableID("use", s.summary.ID, "summary"), SessionID: s.summary.ID,
				DeviceID: in.DeviceID, Harness: in.Source, Model: thread.Model.Model,
				Timestamp: parseFlexibleTime(updated), InputTokens: usage.Input, OutputTokens: usage.Output,
				CachedInputTokens: usage.CacheRead, CacheWrite5mTokens: usage.CacheWrite,
				EstimatedCostUSD: s.summary.EstimatedCostUSD,
				RawUsageJSON:     `{"scope":"thread_total"}`,
			})
		}
		s.appendTo(bundle)
	}
	return bundle, rows.Err()
}

func (s *editorSession) addZedMessage(raw json.RawMessage) error {
	if len(raw) == 0 || raw[0] != '{' {
		return nil // Some Zed message variants are markers, such as "Resume".
	}
	var envelope struct{ User, Agent json.RawMessage }
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	role := ""
	if envelope.User != nil {
		role, raw = "user", envelope.User
	} else if envelope.Agent != nil {
		role, raw = "assistant", envelope.Agent
	}
	var msg zedMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return err
	}
	role = firstNonEmpty(role, msg.Role)
	if role == "" {
		return nil
	}

	var content, thinking strings.Builder
	for _, segment := range msg.Segments {
		switch strings.ToLower(segment.Type) {
		case "text":
			content.WriteString(segment.Text)
		case "thinking":
			thinking.WriteString(segment.Text)
		}
	}
	for _, part := range msg.Content {
		content.WriteString(part.Text)
		content.WriteString(part.Mention.Content)
		thinking.WriteString(part.Thinking.Text)
		if part.ToolUse.ID != "" {
			msg.ToolUses = append(msg.ToolUses, part.ToolUse)
		}
	}

	results := make(map[string]zedToolResult)
	if len(msg.ToolResults) > 0 && msg.ToolResults[0] == '[' {
		var legacy []zedToolResult
		if err := json.Unmarshal(msg.ToolResults, &legacy); err != nil {
			return err
		}
		for _, result := range legacy {
			results[result.ID] = result
		}
	} else if len(msg.ToolResults) > 0 {
		if err := json.Unmarshal(msg.ToolResults, &results); err != nil {
			return err
		}
	}
	model := ""
	if role == "assistant" {
		model = s.summary.ModelPrimary
	}
	messageID := s.addMessage(protocol.MessageRecord{
		Role: role, NativeMessageID: jsonText(msg.ID), Model: model,
		ContentText: firstNonEmpty(content.String(), msg.Context), ThinkingText: thinking.String(),
	})
	for _, tool := range msg.ToolUses {
		result, completed := results[tool.ID]
		status := ""
		if completed {
			status = "completed"
		}
		s.addTool(protocol.ToolCallRecord{
			MessageID: messageID, CallID: tool.ID, ToolName: tool.Name,
			InputJSON:  firstNonEmpty(tool.RawInput, string(tool.Input)),
			OutputText: zedToolText(result.Content), IsError: result.IsError, Status: status,
		})
	}
	return nil
}

func zedToolText(raw json.RawMessage) string {
	if len(raw) == 0 || raw[0] != '[' {
		return jsonText(raw)
	}
	var parts []struct{ Text string }
	if err := json.Unmarshal(raw, &parts); err != nil {
		return jsonText(raw)
	}
	var texts []string
	for _, p := range parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func parseZedLegacy(in FileInput) (*store.ParsedFileBundle, error) {
	f, err := os.Open(in.AbsPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var doc struct {
		ID       string
		Summary  string
		Text     string
		Messages []struct {
			ID       json.RawMessage
			Start    int
			Metadata struct {
				Role      string
				Timestamp json.RawMessage
			}
		}
	}
	if err := json.NewDecoder(f).Decode(&doc); err != nil {
		return nil, err
	}
	s := newEditorSession(in, firstNonEmpty(doc.ID, in.RelPath))
	s.summary.Title = doc.Summary
	for i, msg := range doc.Messages {
		end := len(doc.Text)
		if i+1 < len(doc.Messages) {
			end = doc.Messages[i+1].Start
		}
		if msg.Start < 0 || msg.Start > end || end > len(doc.Text) {
			return nil, fmt.Errorf("invalid message offsets in Zed conversation")
		}
		ts := parseFlexibleTime(jsonText(msg.Metadata.Timestamp))
		s.observeTime(ts)
		s.addMessage(protocol.MessageRecord{Role: strings.ToLower(msg.Metadata.Role), NativeMessageID: jsonText(msg.ID), ContentText: doc.Text[msg.Start:end], Timestamp: timestampPtr(ts)})
	}
	bundle := &store.ParsedFileBundle{}
	s.appendTo(bundle)
	return bundle, nil
}
