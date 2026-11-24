package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancewilhelm/ai-ledger/internal/archive"
	"github.com/lancewilhelm/ai-ledger/internal/collector"
	"github.com/lancewilhelm/ai-ledger/internal/pricing"
	"github.com/lancewilhelm/ai-ledger/internal/protocol"
	"github.com/lancewilhelm/ai-ledger/internal/server"
	"github.com/lancewilhelm/ai-ledger/internal/store"
)

func TestEndToEndCollectorIngestAndAPI(t *testing.T) {
	srvDir := t.TempDir()
	st, err := store.OpenSQLite(filepath.Join(srvDir, "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	arch, err := archive.New(filepath.Join(srvDir, "archive"), true)
	if err != nil {
		t.Fatal(err)
	}
	calc := pricing.NewCalculator()
	srv := server.New(server.Config{
		SyncParseDefault: true,
	}, st, arch, calc)
	defer srv.Close()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Create device token
	plainTok, hashTok, err := server.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.CreateDevice(context.Background(), "lances-macbook", "lances-macbook", "darwin", "arm64", hashTok)
	if err != nil {
		t.Fatal(err)
	}

	// Verify unauthenticated ingest is rejected
	resp, err := http.Get(ts.URL + "/api/ingest/status?source=codex&path=a.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	// Create fake local log tree on "client device"
	clientDir := t.TempDir()
	codexSessDir := filepath.Join(clientDir, "codex", "sessions", "2026", "11", "24")
	claudeProjDir := filepath.Join(clientDir, "claude", "projects", "-Users-lance-projects-ai-ledger")
	if err := os.MkdirAll(codexSessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(claudeProjDir, 0o755); err != nil {
		t.Fatal(err)
	}

	codexFile := filepath.Join(codexSessDir, "rollout-1.jsonl")
	codexChunk1 := `{"timestamp":"2026-11-24T10:00:00Z","type":"session_meta","payload":{"session_id":"cx-1","cwd":"/Users/lance/projects/ai-ledger"}}
{"timestamp":"2026-11-24T10:00:01Z","type":"turn_context","payload":{"model":"gpt-6.2-sol"}}
{"timestamp":"2026-11-24T10:00:02Z","type":"response_item","payload":{"type":"message","id":"m1","role":"user","content":[{"type":"input_text","text":"Export API_KEY=sk-proj-abcdefghijklmnopqrstuvwxyz123456 safely"}]}}
{"timestamp":"2026-11-24T10:00:05Z","type":"token_usage_record","payload":{"session_id":"cx-1","response_id":"r1","usage":{"input_tokens":5000,"cached_input_tokens":1000,"output_tokens":300,"reasoning_output_tokens":50,"total_tokens":5300}}}
`
	if err := os.WriteFile(codexFile, []byte(codexChunk1), 0o644); err != nil {
		t.Fatal(err)
	}

	claudeFile := filepath.Join(claudeProjDir, "cl-1.jsonl")
	claudeData := `{"type":"user","uuid":"u1","sessionId":"cl-1","cwd":"/Users/lance/projects/ai-ledger","timestamp":"2026-11-24T11:00:00Z","message":{"role":"user","content":"Design SQLite schema"}}
{"type":"assistant","uuid":"a1","sessionId":"cl-1","requestId":"req_9","timestamp":"2026-11-24T11:00:05Z","message":{"id":"msg_9","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"Here is the SQLite WAL schema"}],"usage":{"input_tokens":100,"cache_read_input_tokens":2000,"cache_creation_input_tokens":500,"output_tokens":400}}}
`
	if err := os.WriteFile(claudeFile, []byte(claudeData), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := collector.Config{
		ServerURL:      ts.URL,
		DeviceToken:    plainTok,
		StatePath:      filepath.Join(clientDir, "state.json"),
		ChunkSizeBytes: 1024, // small chunks to exercise multi-chunk upload!
		Sources: collector.SourcesConfig{
			CodexDir:      filepath.Join(clientDir, "codex"),
			ClaudeDir:     filepath.Join(clientDir, "claude"),
			DisableAG:     true,
			DisableT3:     true,
			DisableHermes: true,
		},
	}

	cState, err := collector.LoadState(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	syncer := collector.NewSyncer(cfg, cState, nil, false)

	// Pass 1: initial sync
	stats1, err := syncer.SyncOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats1.UploadedFiles != 2 || stats1.Errors != 0 {
		t.Fatalf("unexpected pass 1 stats: %+v", stats1)
	}

	// Pass 2: unchanged -> 0 uploads
	stats2, err := syncer.SyncOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats2.UploadedFiles != 0 || stats2.Skipped != 2 {
		t.Fatalf("expected 0 uploads on unchanged pass, got %+v", stats2)
	}

	// Pass 3: append a second API call to Codex file
	codexChunk2 := `{"timestamp":"2026-11-24T10:05:00Z","type":"token_usage_record","payload":{"session_id":"cx-1","response_id":"r2","usage":{"input_tokens":6000,"cached_input_tokens":4000,"output_tokens":200,"reasoning_output_tokens":20,"total_tokens":6200}}}
`
	f, err := os.OpenFile(codexFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(codexChunk2)
	f.Close()

	stats3, err := syncer.SyncOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats3.UploadedFiles != 1 || stats3.UploadedBytes != int64(len(codexChunk2)) {
		t.Fatalf("expected incremental append of %d bytes, got %+v", len(codexChunk2), stats3)
	}

	// Query /api/overview
	ov := getJSON[protocol.OverviewResponse](t, ts.URL+"/api/overview")
	if ov.Totals.SessionCount != 2 {
		t.Errorf("expected 2 sessions, got %d", ov.Totals.SessionCount)
	}
	if ov.Totals.APICallCount != 3 {
		t.Errorf("expected 3 API calls (2 codex + 1 claude), got %d", ov.Totals.APICallCount)
	}
	// Codex uncached: (5000-1000) + (6000-4000) = 6000; Claude uncached: 100 -> 6100
	if ov.Totals.InputTokens != 6100 {
		t.Errorf("expected 6100 uncached input tokens, got %d", ov.Totals.InputTokens)
	}
	if ov.Archive.TotalFiles != 2 || ov.Archive.ParsedFiles != 2 {
		t.Errorf("unexpected archive summary: %+v", ov.Archive)
	}

	// Query /api/timeseries
	tsResp := getJSON[struct {
		Points []protocol.TimeseriesPoint `json:"points"`
	}](t, ts.URL+"/api/timeseries?bucket=day&group_by=harness")
	if len(tsResp.Points) != 2 {
		t.Errorf("expected 2 timeseries points (codex + claude), got %d", len(tsResp.Points))
	}

	// Query /api/search (FTS5)
	searchResp := getJSON[struct {
		Hits []protocol.SearchHit `json:"hits"`
	}](t, ts.URL+"/api/search?q=SQLite+WAL")
	if len(searchResp.Hits) != 1 {
		t.Errorf("expected 1 FTS search hit, got %d", len(searchResp.Hits))
	}

	// Query /api/sessions & drill down
	sessResp := getJSON[struct {
		Sessions []protocol.SessionSummary `json:"sessions"`
		Total    int64                     `json:"total"`
	}](t, ts.URL+"/api/sessions")
	if sessResp.Total != 2 || len(sessResp.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", sessResp.Total)
	}
	detail := getJSON[protocol.SessionDetail](t, ts.URL+"/api/sessions/"+sessResp.Sessions[0].ID)
	if len(detail.Messages) == 0 || len(detail.Usage) == 0 {
		t.Errorf("expected session messages & usage, got msgs=%d usage=%d", len(detail.Messages), len(detail.Usage))
	}

	// Verify /api/export/training redacts the sk-proj-... secret!
	expResp, err := http.Get(ts.URL + "/api/export/training?redact=true")
	if err != nil {
		t.Fatal(err)
	}
	expBody, _ := io.ReadAll(expResp.Body)
	expResp.Body.Close()
	if strings.Contains(string(expBody), "sk-proj-abcdefghijklmnopqrstuvwxyz123456") {
		t.Errorf("expected secret to be redacted in training export")
	}
	if !strings.Contains(string(expBody), "[REDACTED") {
		t.Errorf("expected redaction marker in training export: %s", string(expBody))
	}

	// Verify raw download still preserves original unredacted raw archive!
	var codexSessID string
	for _, s := range sessResp.Sessions {
		if s.Harness == protocol.SourceCodex {
			codexSessID = s.ID
		}
	}
	rawResp, err := http.Get(ts.URL + "/api/sessions/" + codexSessID + "/raw")
	if err != nil {
		t.Fatal(err)
	}
	rawBody, _ := io.ReadAll(rawResp.Body)
	rawResp.Body.Close()
	if !strings.Contains(string(rawBody), "sk-proj-abcdefghijklmnopqrstuvwxyz123456") {
		t.Errorf("raw archive should remain lossless and untouched")
	}

	// Verify /api/admin/reparse succeeds cleanly
	repReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/reparse", strings.NewReader("{}"))
	repResp, err := http.DefaultClient.Do(repReq)
	if err != nil {
		t.Fatal(err)
	}
	repBody, _ := io.ReadAll(repResp.Body)
	repResp.Body.Close()
	if repResp.StatusCode != http.StatusOK || !strings.Contains(string(repBody), `"parsed":2`) {
		t.Errorf("unexpected reparse response %d: %s", repResp.StatusCode, string(repBody))
	}

	_ = dev
}

func getJSON[T any](t *testing.T, url string) T {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s HTTP %d: %s", url, resp.StatusCode, string(body))
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v\nbody: %s", url, err, string(body))
	}
	return out
}
