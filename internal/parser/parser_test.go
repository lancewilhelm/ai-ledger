package parser_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/lancewilhelm/ai-ledger/internal/parser"
	"github.com/lancewilhelm/ai-ledger/internal/pricing"
	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	_ "modernc.org/sqlite"
)

func TestParseCodexNoDoubleCountAndZstd(t *testing.T) {
	dir := t.TempDir()
	jsonl := `{"timestamp":"2026-11-24T16:50:47.077Z","ordinal":0,"type":"session_meta","payload":{"session_id":"01a20e77-1111","cwd":"/Users/lance/.t3/worktrees/delb/feat-abc","originator":"T3 Code","git":{"branch":"feat/abc"}}}
{"timestamp":"2026-11-24T16:50:50.401Z","ordinal":1,"type":"turn_context","payload":{"model":"gpt-6.2-sol","effort":"high"}}
{"timestamp":"2026-11-24T16:50:51.000Z","ordinal":2,"type":"response_item","payload":{"type":"message","id":"m1","role":"user","content":[{"type":"input_text","text":"Build the dashboard backend"}]}}
{"timestamp":"2026-11-24T16:50:52.000Z","ordinal":3,"type":"response_item","payload":{"type":"function_call","id":"fc1","call_id":"call_1","name":"shell","arguments":"{\"cmd\":\"ls\"}"}}
{"timestamp":"2026-11-24T16:50:53.000Z","ordinal":4,"type":"response_item","payload":{"type":"function_call_output","id":"fco1","call_id":"call_1","output":"README.md"}}
{"timestamp":"2026-11-24T16:50:56.416Z","ordinal":5,"type":"token_usage_record","payload":{"session_id":"01a20e77-1111","response_id":"resp_1","usage":{"input_tokens":30000,"cached_input_tokens":20000,"cache_write_input_tokens":0,"output_tokens":500,"reasoning_output_tokens":100,"total_tokens":30500}}}
{"timestamp":"2026-11-24T16:50:56.616Z","ordinal":6,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":30000,"cached_input_tokens":20000,"output_tokens":500,"reasoning_output_tokens":100,"total_tokens":30500},"last_token_usage":{"input_tokens":30000,"cached_input_tokens":20000,"output_tokens":500,"reasoning_output_tokens":100,"total_tokens":30500},"model_context_window":475000},"rate_limits":{"limit_id":"codex","plan_type":"pro","primary":{"used_percent":12.5}}}}
{"timestamp":"2026-11-24T16:50:57.000Z","ordinal":7,"type":"response_item","payload":{"type":"message","id":"m2","role":"assistant","content":[{"type":"output_text","text":"Done!"}]}}
`

	// Test plain .jsonl
	plainPath := filepath.Join(dir, "rollout.jsonl")
	if err := os.WriteFile(plainPath, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	// Test .jsonl.zst
	zstPath := filepath.Join(dir, "rollout.jsonl.zst")
	enc, _ := zstd.NewWriter(nil)
	compressed := enc.EncodeAll([]byte(jsonl), nil)
	enc.Close()
	if err := os.WriteFile(zstPath, compressed, 0o644); err != nil {
		t.Fatal(err)
	}

	reg := parser.NewRegistry(pricing.NewCalculator())
	for _, p := range []string{plainPath, zstPath} {
		b, err := reg.Parse(context.Background(), parser.FileInput{
			FileID:   "f1",
			DeviceID: "dev1",
			Source:   protocol.SourceCodex,
			RelPath:  filepath.Base(p),
			AbsPath:  p,
		})
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		if len(b.Sessions) != 1 {
			t.Fatalf("expected 1 session, got %d", len(b.Sessions))
		}
		s := b.Sessions[0]
		if s.ProjectName != "delb" {
			t.Errorf("expected project delb from worktree, got %q", s.ProjectName)
		}
		if len(b.Usage) != 1 {
			t.Fatalf("expected exactly 1 deduplicated usage event (no double count), got %d", len(b.Usage))
		}
		// Uncached input = 30000 - 20000 = 10000
		if s.InputTokens != 10000 || s.CachedInputTokens != 20000 || s.OutputTokens != 500 || s.ReasoningTokens != 100 {
			t.Errorf("unexpected tokens: in=%d cached=%d out=%d reas=%d", s.InputTokens, s.CachedInputTokens, s.OutputTokens, s.ReasoningTokens)
		}
		if len(b.ToolCalls) != 1 || b.ToolCalls[0].OutputText != "README.md" {
			t.Errorf("expected paired tool call output, got %+v", b.ToolCalls)
		}
		if len(b.RateLimits) != 1 || b.RateLimits[0].PrimaryUsedPct == nil || *b.RateLimits[0].PrimaryUsedPct != 12.5 {
			t.Errorf("expected rate limit snapshot, got %+v", b.RateLimits)
		}
		if s.EstimatedCostUSD <= 0 {
			t.Errorf("expected positive estimated cost")
		}
	}
}

func TestParseClaudeDeduplicatesMultiBlockAssistant(t *testing.T) {
	dir := t.TempDir()
	jsonl := `{"type":"custom-title","customTitle":"Personal AI Usage Logging","sessionId":"sess-claude-1"}
{"type":"user","uuid":"u1","sessionId":"sess-claude-1","cwd":"/Users/lance/projects/ai-ledger","gitBranch":"main","timestamp":"2026-11-24T17:00:00Z","message":{"role":"user","content":"Check token usage"}}
{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"sess-claude-1","requestId":"req_1","effort":"high","timestamp":"2026-11-24T17:00:02Z","message":{"id":"msg_1","model":"claude-opus-5-5","role":"assistant","content":[{"type":"thinking","thinking":"Let me check."}],"usage":{"input_tokens":10,"cache_creation_input_tokens":1000,"cache_read_input_tokens":5000,"output_tokens":50,"output_tokens_details":{"thinking_tokens":20},"cache_creation":{"ephemeral_1h_input_tokens":1000,"ephemeral_5m_input_tokens":0}}}}
{"type":"assistant","uuid":"a2","parentUuid":"u1","sessionId":"sess-claude-1","requestId":"req_1","effort":"high","timestamp":"2026-11-24T17:00:03Z","message":{"id":"msg_1","model":"claude-opus-5-5","role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"Running tool"},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":10,"cache_creation_input_tokens":1000,"cache_read_input_tokens":5000,"output_tokens":200,"output_tokens_details":{"thinking_tokens":20},"cache_creation":{"ephemeral_1h_input_tokens":1000,"ephemeral_5m_input_tokens":0}}}}
{"type":"user","uuid":"u2","sessionId":"sess-claude-1","timestamp":"2026-11-24T17:00:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"file1.go"}]}}
{"type":"cost-state","sessionId":"sess-claude-1","totalCostUSD":0.42}
`
	p := filepath.Join(dir, "sess-claude-1.jsonl")
	if err := os.WriteFile(p, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := parser.NewRegistry(pricing.NewCalculator())
	b, err := reg.Parse(context.Background(), parser.FileInput{
		FileID:   "f2",
		DeviceID: "dev1",
		Source:   protocol.SourceClaude,
		RelPath:  "projects/ai-ledger/sess-claude-1.jsonl",
		AbsPath:  p,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(b.Sessions))
	}
	s := b.Sessions[0]
	if s.Title != "Personal AI Usage Logging" {
		t.Errorf("unexpected title: %q", s.Title)
	}
	if len(b.Usage) != 1 {
		t.Fatalf("expected 1 deduplicated usage event, got %d", len(b.Usage))
	}
	if b.Usage[0].OutputTokens != 200 || b.Usage[0].CacheWrite1hTokens != 1000 || b.Usage[0].CachedInputTokens != 5000 {
		t.Errorf("unexpected usage: %+v", b.Usage[0])
	}
	if s.ReportedCostUSD == nil || *s.ReportedCostUSD != 0.42 {
		t.Errorf("expected reported cost 0.42, got %v", s.ReportedCostUSD)
	}
	if len(b.Messages) != 2 {
		t.Fatalf("expected 2 merged messages (user + merged assistant), got %d", len(b.Messages))
	}
	if b.Messages[1].ThinkingText != "Let me check." || b.Messages[1].ContentText != "Running tool" {
		t.Errorf("merged assistant mismatch: %+v", b.Messages[1])
	}
	if len(b.ToolCalls) != 1 || b.ToolCalls[0].OutputText != "file1.go" {
		t.Errorf("expected paired tool result, got %+v", b.ToolCalls)
	}
}

func TestParseAntigravity(t *testing.T) {
	dir := t.TempDir()
	jsonl := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-22T14:08:21Z","content":"Command Working Directory: /Users/lance/projects/ai-ledger\n<user_request>Hello world</user_request>"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-22T14:08:22Z","model":"gemini-4-neon-high","input_tokens":2000,"cache_read_tokens":34000,"output_tokens":250,"thinking":"Thinking...","content":"Hi there","tool_calls":[{"name":"run_command","args":{"cmd":"pwd"}}]}
{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-22T14:08:24Z","content":"/Users/lance/projects/ai-ledger"}
`
	p := filepath.Join(dir, "transcript_full.jsonl")
	if err := os.WriteFile(p, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := parser.NewRegistry(pricing.NewCalculator())
	b, err := reg.Parse(context.Background(), parser.FileInput{
		FileID:   "f3",
		DeviceID: "dev1",
		Source:   protocol.SourceAntigravity,
		RelPath:  "abc/antigravity-acp/brain/f700b03d-021b/.system_generated/logs/transcript_full.jsonl",
		AbsPath:  p,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(b.Sessions))
	}
	s := b.Sessions[0]
	if s.NativeID != "f700b03d-021b" || s.ProjectName != "ai-ledger" {
		t.Errorf("unexpected metadata: native=%q project=%q", s.NativeID, s.ProjectName)
	}
	if s.InputTokens != 2000 || s.CachedInputTokens != 34000 || s.OutputTokens != 250 {
		t.Errorf("unexpected tokens: %+v", s)
	}
	if len(b.ToolCalls) != 1 || b.ToolCalls[0].OutputText != "/Users/lance/projects/ai-ledger" {
		t.Errorf("unexpected tool calls: %+v", b.ToolCalls)
	}
}

func TestParseT3AndHermesSQLite(t *testing.T) {
	dir := t.TempDir()

	// Create mock T3 statev2.sqlite
	t3Path := filepath.Join(dir, "statev2.sqlite")
	db, err := sql.Open("sqlite", t3Path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE projection_projects (project_id TEXT PRIMARY KEY, title TEXT, workspace_root TEXT);
		CREATE TABLE orchestration_v2_projection_threads (thread_id TEXT PRIMARY KEY, project_id TEXT, title TEXT, default_provider TEXT, active_provider_thread_id TEXT, updated_at TEXT);
		CREATE TABLE orchestration_v2_projection_provider_threads (provider_thread_id TEXT PRIMARY KEY, thread_id TEXT, provider TEXT, updated_at TEXT);
		INSERT INTO projection_projects VALUES ('p1', 'Delb App', '/Users/lance/projects/delb');
		INSERT INTO orchestration_v2_projection_threads VALUES ('t1', 'p1', 'Fix Sidebar', 'codex', 'provider-thread:provider:codex:native-thread:01a20e77-1111', '2026-11-24T17:00:00Z');
		INSERT INTO orchestration_v2_projection_provider_threads VALUES ('provider-thread:provider:codex:native-thread:01a20e77-1111', 't1', 'codex', '2026-11-24T17:00:00Z');
	`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	reg := parser.NewRegistry(pricing.NewCalculator())
	tb, err := reg.Parse(context.Background(), parser.FileInput{
		FileID:   "ft3",
		DeviceID: "dev1",
		Source:   protocol.SourceT3,
		RelPath:  "statev2.sqlite",
		AbsPath:  t3Path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.T3Links) != 1 || tb.T3Links[0].NativeSessionID != "01a20e77-1111" || tb.T3Links[0].ProjectTitle != "Delb App" {
		t.Fatalf("unexpected T3 links: %+v", tb.T3Links)
	}

	// Also verify zstd-compressed T3 snapshot (.sqlite.zst)
	t3ZstPath := filepath.Join(dir, "statev2-meta.sqlite.zst")
	t3Raw, err := os.ReadFile(t3Path)
	if err != nil {
		t.Fatal(err)
	}
	zEnc, _ := zstd.NewWriter(nil)
	if err := os.WriteFile(t3ZstPath, zEnc.EncodeAll(t3Raw, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	zEnc.Close()
	tbZ, err := reg.Parse(context.Background(), parser.FileInput{
		FileID:   "ft3z",
		DeviceID: "dev1",
		Source:   protocol.SourceT3,
		RelPath:  "statev2-meta.sqlite.zst",
		AbsPath:  t3ZstPath,
	})
	if err != nil {
		t.Fatalf("parse t3 zst: %v", err)
	}
	if len(tbZ.T3Links) != 1 || tbZ.T3Links[0].NativeSessionID != "01a20e77-1111" {
		t.Fatalf("unexpected T3 zst links: %+v", tbZ.T3Links)
	}

	// Create mock Hermes state.db
	hPath := filepath.Join(dir, "state.db")
	hdb, err := sql.Open("sqlite", hPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = hdb.Exec(`
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY, source TEXT, model TEXT, parent_session_id TEXT,
			started_at REAL, ended_at REAL, message_count INTEGER, tool_call_count INTEGER,
			api_call_count INTEGER, input_tokens INTEGER, output_tokens INTEGER,
			cache_read_tokens INTEGER, cache_write_tokens INTEGER, reasoning_tokens INTEGER,
			cwd TEXT, git_branch TEXT, title TEXT, estimated_cost_usd REAL, actual_cost_usd REAL
		);
		CREATE TABLE messages (
			id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, content TEXT, tool_call_id TEXT,
			tool_calls TEXT, tool_name TEXT, timestamp REAL, finish_reason TEXT, reasoning TEXT, reasoning_content TEXT
		);
		CREATE TABLE session_model_usage (
			session_id TEXT, model TEXT, task TEXT, api_call_count INTEGER,
			input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER,
			cache_write_tokens INTEGER, reasoning_tokens INTEGER,
			estimated_cost_usd REAL, actual_cost_usd REAL, first_seen REAL, last_seen REAL
		);
		INSERT INTO sessions VALUES ('h1','cli','anthropic/claude-opus-4-6','',1795500000,1795500100,2,0,1,500,150,4000,0,0,'/home/lance/bot','main','Hermes Test',0.02,0.02);
		INSERT INTO messages VALUES (1,'h1','user','Hello Hermes','','','',1795500000,'','','');
		INSERT INTO messages VALUES (2,'h1','assistant','Hi Lance!','','','',1795500010,'stop','pondering','');
		INSERT INTO session_model_usage VALUES ('h1','anthropic/claude-opus-4-6','chat',1,500,150,4000,0,0,0.02,0.02,1795500000,1795500010);
	`)
	if err != nil {
		t.Fatal(err)
	}
	hdb.Close()

	hb, err := reg.Parse(context.Background(), parser.FileInput{
		FileID:   "fhermes",
		DeviceID: "dev2",
		Source:   protocol.SourceHermes,
		RelPath:  "state.db",
		AbsPath:  hPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hb.Sessions) != 1 || len(hb.Messages) != 2 || len(hb.Usage) != 1 {
		t.Fatalf("unexpected hermes bundle: sessions=%d msgs=%d usage=%d", len(hb.Sessions), len(hb.Messages), len(hb.Usage))
	}
	if hb.Sessions[0].Title != "Hermes Test" || hb.Usage[0].EstimatedCostUSD != 0.02 || hb.Sessions[0].EstimatedCostUSD != 0.02 {
		t.Errorf("unexpected hermes values: %+v %+v", hb.Sessions[0], hb.Usage[0])
	}

	// Also verify zstd-compressed Hermes snapshot (.db.zst)
	hZstPath := filepath.Join(dir, "state.db.zst")
	hRaw, err := os.ReadFile(hPath)
	if err != nil {
		t.Fatal(err)
	}
	zEnc2, _ := zstd.NewWriter(nil)
	if err := os.WriteFile(hZstPath, zEnc2.EncodeAll(hRaw, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	zEnc2.Close()
	hbZ, err := reg.Parse(context.Background(), parser.FileInput{
		FileID:   "fhermesz",
		DeviceID: "dev2",
		Source:   protocol.SourceHermes,
		RelPath:  "state.db.zst",
		AbsPath:  hZstPath,
	})
	if err != nil {
		t.Fatalf("parse hermes zst: %v", err)
	}
	if len(hbZ.Sessions) != 1 || len(hbZ.Messages) != 2 || len(hbZ.Usage) != 1 {
		t.Fatalf("unexpected hermes zst bundle: sessions=%d msgs=%d usage=%d", len(hbZ.Sessions), len(hbZ.Messages), len(hbZ.Usage))
	}
}

func TestParseCodexSubagentDistinctSessionID(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "rollout-parent.jsonl")
	childPath := filepath.Join(dir, "rollout-child.jsonl")

	parentData := strings.Join([]string{
		`{"timestamp":"2026-11-16T20:10:04Z","type":"session_meta","payload":{"id":"parent-uuid","session_id":"parent-uuid","cwd":"/Users/dev/app","source":"vscode"}}`,
		`{"timestamp":"2026-11-16T20:10:05Z","type":"turn_context","payload":{"model":"gpt-5.4-codex"}}`,
		`{"timestamp":"2026-11-16T20:10:06Z","type":"token_usage_record","payload":{"session_id":"parent-uuid","response_id":"resp_p1","model":"gpt-5.4-codex","usage":{"input_tokens":2000,"cached_input_tokens":1000,"output_tokens":300}}}`,
	}, "\n") + "\n"

	childData := strings.Join([]string{
		`{"timestamp":"2026-11-16T20:10:44Z","type":"session_meta","payload":{"id":"child-uuid","session_id":"parent-uuid","parent_thread_id":"parent-uuid","cwd":"/Users/dev/app","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent-uuid","depth":1}}}}}`,
		`{"timestamp":"2026-11-16T20:10:45Z","type":"turn_context","payload":{"model":"gpt-5.4-codex"}}`,
		`{"timestamp":"2026-11-16T20:10:46Z","type":"token_usage_record","payload":{"session_id":"parent-uuid","response_id":"resp_c1","model":"gpt-5.4-codex","usage":{"input_tokens":1000,"cached_input_tokens":500,"output_tokens":100}}}`,
	}, "\n") + "\n"

	if err := os.WriteFile(parentPath, []byte(parentData), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(childPath, []byte(childData), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := parser.NewRegistry(pricing.NewCalculator())
	pb, err := reg.Parse(context.Background(), parser.FileInput{
		FileID: "fp", DeviceID: "dev1", Source: protocol.SourceCodex, RelPath: "rollout-parent.jsonl", AbsPath: parentPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	cb, err := reg.Parse(context.Background(), parser.FileInput{
		FileID: "fc", DeviceID: "dev1", Source: protocol.SourceCodex, RelPath: "rollout-child.jsonl", AbsPath: childPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(pb.Sessions) != 1 || len(cb.Sessions) != 1 {
		t.Fatalf("expected 1 session each, got parent=%d child=%d", len(pb.Sessions), len(cb.Sessions))
	}
	if pb.Sessions[0].ID == cb.Sessions[0].ID {
		t.Fatalf("subagent collided with parent session ID %s", pb.Sessions[0].ID)
	}
	if cb.Sessions[0].NativeID != "child-uuid" || !cb.Sessions[0].IsSidechain || cb.Sessions[0].ParentSessionID != "parent-uuid" {
		t.Fatalf("unexpected subagent metadata: %+v", cb.Sessions[0])
	}
}
