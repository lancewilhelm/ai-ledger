package parser

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/store"
)

type editorSession struct {
	summary  protocol.SessionSummary
	messages []protocol.MessageRecord
	tools    []protocol.ToolCallRecord
}

func newEditorSession(in FileInput, nativeID string) editorSession {
	return editorSession{summary: protocol.SessionSummary{
		ID:       stableID("sess", in.DeviceID, in.Source, nativeID),
		NativeID: nativeID, DeviceID: in.DeviceID, Harness: in.Source, SourceFileID: in.FileID,
	}}
}

func (s *editorSession) addMessage(msg protocol.MessageRecord) string {
	msg.SessionID = s.summary.ID
	msg.Ordinal = len(s.messages)
	msg.ID = stableID("msg", msg.SessionID, strconv.Itoa(msg.Ordinal), msg.NativeMessageID)
	s.messages = append(s.messages, msg)
	if s.summary.Title == "" && msg.Role == "user" {
		s.summary.Title = truncateTitle(msg.ContentText, 100)
	}
	return msg.ID
}

func (s *editorSession) addTool(tool protocol.ToolCallRecord) {
	tool.SessionID = s.summary.ID
	tool.Ordinal = len(s.tools)
	tool.ID = stableID("tc", tool.SessionID, strconv.Itoa(tool.Ordinal), tool.CallID)
	s.tools = append(s.tools, tool)
}

func timestampPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func (s *editorSession) observeTime(t time.Time) {
	if t.IsZero() {
		return
	}
	if s.summary.StartedAt.IsZero() || t.Before(s.summary.StartedAt) {
		s.summary.StartedAt = t
	}
	if s.summary.EndedAt == nil || t.After(*s.summary.EndedAt) {
		s.summary.EndedAt = &t
	}
}

func (s *editorSession) appendTo(bundle *store.ParsedFileBundle) {
	s.summary.MessageCount = len(s.messages)
	s.summary.ToolCallCount = len(s.tools)
	s.summary.Title = firstNonEmpty(s.summary.Title, s.summary.NativeID)
	bundle.Sessions = append(bundle.Sessions, s.summary)
	bundle.Messages = append(bundle.Messages, s.messages...)
	bundle.ToolCalls = append(bundle.ToolCalls, s.tools...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
