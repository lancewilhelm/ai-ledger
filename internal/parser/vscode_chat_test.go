package parser_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lancewilhelm/ai-ledger/internal/parser"
	"github.com/lancewilhelm/ai-ledger/internal/pricing"
	"github.com/lancewilhelm/ai-ledger/internal/protocol"
)

func TestParseVSCodeChatJournal(t *testing.T) {
	p := filepath.Join(t.TempDir(), "chat.jsonl")
	journal := `{"kind":0,"v":{"sessionId":"vs-chat","creationDate":1795514400000}}
{"kind":2,"k":["requests"],"v":[{"requestId":"r","message":{"text":"Find the response"}},{"requestId":"deleted","message":{"text":"Discard me"}}]}
{"kind":2,"k":["requests"],"i":1}
{"kind":1,"k":["customTitle"],"v":"Renamed chat"}
{"kind":1,"k":["requests",0,"modelId"],"v":"gpt-5"}
{"kind":2,"k":["requests",0,"response"],"v":[{"value":"Answer "},{"kind":"markdownContent","content":{"value":"continued"}},{"kind":"toolInvocationSerialized","toolId":"read","toolCallId":"tool","isComplete":true}]}
{"kind":3,"k":["requests",0,"unused"]}
`
	if err := os.WriteFile(p, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := parser.NewRegistry(pricing.NewCalculator())
	result, err := reg.Parse(context.Background(), parser.FileInput{
		FileID: "file", DeviceID: "device", Source: protocol.SourceVSCodeChat, RelPath: "chat.jsonl", AbsPath: p,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 || result.Sessions[0].Title != "Renamed chat" || result.Sessions[0].ModelPrimary != "gpt-5" {
		t.Fatalf("unexpected sessions: %+v", result.Sessions)
	}
	if len(result.Messages) != 2 || result.Messages[0].ContentText != "Find the response" || result.Messages[1].ContentText != "Answer continued" {
		t.Fatalf("unexpected messages: %+v", result.Messages)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].MessageID != result.Messages[1].ID || result.ToolCalls[0].ToolName != "read" {
		t.Fatalf("unexpected tools: %+v", result.ToolCalls)
	}
	if len(result.Usage) != 0 {
		t.Fatalf("expected no usage without token records, got %+v", result.Usage)
	}
}
