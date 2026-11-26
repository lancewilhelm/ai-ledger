package parser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/store"
)

type vsCodeChat struct {
	SessionID        string
	CustomTitle      string
	CreationDate     int64
	LastMessageDate  int64
	WorkingDirectory string
	Requests         []struct {
		RequestID         string
		ResponseID        string
		ResponseTimestamp int64
		Timestamp         int64
		ModelID           string
		ResolvedModel     string
		Message           struct{ Text string }
		Response          []json.RawMessage
	}
}

func (r *Registry) parseVSCodeChat(ctx context.Context, in FileInput) (*store.ParsedFileBundle, error) {
	f, err := os.Open(in.AbsPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var doc vsCodeChat
	if path.Ext(in.RelPath) == ".jsonl" {
		err = readVSCodeChatLog(ctx, f, &doc)
	} else {
		err = json.NewDecoder(f).Decode(&doc)
	}
	if err != nil {
		return nil, fmt.Errorf("read VS Code chat: %w", err)
	}
	bundle := &store.ParsedFileBundle{}
	if len(doc.Requests) == 0 {
		return bundle, nil
	}

	nativeID := firstNonEmpty(doc.SessionID, strings.TrimSuffix(path.Base(in.RelPath), path.Ext(in.RelPath)))
	s := newEditorSession(in, nativeID)
	s.summary.Title = doc.CustomTitle
	if folder, err := url.Parse(doc.WorkingDirectory); err == nil && folder.Scheme == "file" {
		s.summary.ProjectPath = folder.Path
		s.summary.ProjectName = deriveProjectName(folder.Path)
	}
	s.observeTime(fromUnixMillis(doc.CreationDate))
	s.observeTime(fromUnixMillis(doc.LastMessageDate))
	models := make(map[string]int)

	for _, request := range doc.Requests {
		ts := fromUnixMillis(request.Timestamp)
		s.observeTime(ts)
		responseTime := fromUnixMillis(request.ResponseTimestamp)
		s.observeTime(responseTime)
		s.addMessage(protocol.MessageRecord{
			Role: "user", NativeMessageID: request.RequestID, ContentText: request.Message.Text, Timestamp: timestampPtr(ts),
		})
		model := firstNonEmpty(request.ResolvedModel, request.ModelID)
		if model != "" {
			models[model]++
		}

		var content, thinking strings.Builder
		var tools []protocol.ToolCallRecord
		for _, raw := range request.Response {
			if len(raw) > 0 && raw[0] == '"' {
				content.WriteString(jsonText(raw))
				continue
			}
			var part struct {
				Kind             string
				Value            json.RawMessage
				Content          struct{ Value string }
				ToolCallID       string
				ToolID           string
				IsComplete       bool
				ToolSpecificData json.RawMessage
				ResultDetails    struct {
					Input   json.RawMessage
					Output  json.RawMessage
					IsError bool
				}
			}
			if err := json.Unmarshal(raw, &part); err != nil {
				continue
			}
			switch part.Kind {
			case "", "markdownContent":
				content.WriteString(firstNonEmpty(part.Content.Value, jsonText(part.Value)))
			case "thinking":
				var fragments []string
				if json.Unmarshal(part.Value, &fragments) == nil {
					thinking.WriteString(strings.Join(fragments, ""))
				} else {
					thinking.WriteString(jsonText(part.Value))
				}
			case "toolInvocation", "toolInvocationSerialized":
				status := ""
				if part.IsComplete {
					status = "completed"
				}
				tools = append(tools, protocol.ToolCallRecord{
					CallID: part.ToolCallID, ToolName: part.ToolID, Status: status,
					InputJSON:  firstNonEmpty(string(part.ResultDetails.Input), string(part.ToolSpecificData)),
					OutputText: jsonText(part.ResultDetails.Output), IsError: part.ResultDetails.IsError,
					Timestamp: timestampPtr(responseTime),
				})
			}
		}
		if content.Len() == 0 && thinking.Len() == 0 && len(tools) == 0 {
			continue
		}
		messageID := s.addMessage(protocol.MessageRecord{
			Role: "assistant", NativeMessageID: request.ResponseID, Model: model,
			ContentText: content.String(), ThinkingText: thinking.String(), Timestamp: timestampPtr(responseTime),
		})
		for _, tool := range tools {
			tool.MessageID = messageID
			s.addTool(tool)
		}
	}
	s.summary.ModelPrimary = mostFrequentModel(models)
	s.appendTo(bundle)
	return bundle, nil
}

func fromUnixMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// VS Code journals mutations to a serialized chat object. Push can truncate the
// target array before appending, including when undo removes requests or parts.
func readVSCodeChatLog(ctx context.Context, reader io.Reader, doc *vsCodeChat) error {
	var state any
	err := scanJSONL(reader, func(line int, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var op struct {
			Kind  int
			Path  []json.RawMessage `json:"k"`
			Value json.RawMessage   `json:"v"`
			Index *int              `json:"i"`
		}
		if err := json.Unmarshal(raw, &op); err != nil {
			return fmt.Errorf("mutation at line %d: %w", line, err)
		}
		var value any
		if len(op.Value) > 0 {
			if err := json.Unmarshal(op.Value, &value); err != nil {
				return err
			}
		}
		if op.Kind == 0 {
			state = value
			return nil
		}
		if state == nil {
			return fmt.Errorf("mutation before initial chat state at line %d", line)
		}
		var err error
		state, err = applyChatMutation(state, op.Path, op.Kind, value, op.Index)
		if err != nil {
			return fmt.Errorf("mutation at line %d: %w", line, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if state == nil {
		return fmt.Errorf("missing initial chat state")
	}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, doc)
}

func applyChatMutation(node any, keys []json.RawMessage, kind int, value any, index *int) (any, error) {
	if len(keys) == 0 {
		switch kind {
		case 1:
			return value, nil
		case 2:
			items, ok := node.([]any)
			if node != nil && !ok {
				return nil, fmt.Errorf("append target is not an array")
			}
			if index != nil {
				if *index < 0 || *index > len(items) {
					return nil, fmt.Errorf("invalid array truncation index")
				}
				items = items[:*index]
			}
			if value == nil {
				return items, nil
			}
			tail, ok := value.([]any)
			if !ok {
				return nil, fmt.Errorf("append value is not an array")
			}
			return append(items, tail...), nil
		case 3:
			return nil, nil
		default:
			return nil, fmt.Errorf("unknown mutation kind %d", kind)
		}
	}
	switch node := node.(type) {
	case map[string]any:
		var key string
		if err := json.Unmarshal(keys[0], &key); err != nil {
			key = string(keys[0])
		}
		if kind == 3 && len(keys) == 1 {
			delete(node, key)
			return node, nil
		}
		child, err := applyChatMutation(node[key], keys[1:], kind, value, index)
		if err != nil {
			return nil, err
		}
		node[key] = child
		return node, nil
	case []any:
		key, err := strconv.Atoi(jsonText(keys[0]))
		if err != nil || key < 0 || key >= len(node) {
			return nil, fmt.Errorf("invalid array mutation path")
		}
		child, err := applyChatMutation(node[key], keys[1:], kind, value, index)
		if err != nil {
			return nil, err
		}
		node[key] = child
		return node, nil
	default:
		return nil, fmt.Errorf("invalid chat mutation path")
	}
}
