package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/protocol"
)

func TestHarnessBreakdownIncludesSessionsWithoutUsage(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "ledger.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	device, err := st.CreateDevice(ctx, "test-mbp", "", "", "", "device-token-hash")
	if err != nil {
		t.Fatal(err)
	}
	recent := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	older := recent.AddDate(0, -3, 0)
	for _, fixture := range []struct {
		harness string
		started time.Time
		tokens  int64
	}{
		{protocol.SourceVSCodeChat, older, 0},
		{protocol.SourceZed, recent, 50},
		{protocol.SourceCodex, recent, 100},
	} {
		file := protocol.RawFileRecord{DeviceID: device.ID, Source: fixture.harness, RelPath: "chat"}
		if err := st.UpsertRawFile(ctx, &file); err != nil {
			t.Fatal(err)
		}
		bundle := &ParsedFileBundle{Sessions: []protocol.SessionSummary{{
			ID: fixture.harness, NativeID: "chat", DeviceID: device.ID, Harness: fixture.harness,
			SourceFileID: file.ID, StartedAt: fixture.started, InputTokens: fixture.tokens,
		}}}
		if fixture.tokens > 0 {
			bundle.Usage = []protocol.UsageEventRecord{{
				ID: "usage-" + fixture.harness, SessionID: fixture.harness, DeviceID: device.ID,
				Harness: fixture.harness, Timestamp: fixture.started, InputTokens: fixture.tokens,
			}}
		}
		if err := st.ReplaceFileParsedData(ctx, file.ID, bundle); err != nil {
			t.Fatal(err)
		}
	}
	overview, err := st.GetOverview(ctx, QueryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	harnesses := make(map[string]protocol.BreakdownRow)
	for _, row := range overview.ByHarness {
		harnesses[row.Key] = row
	}
	for _, want := range []struct {
		harness       string
		tokens, calls int64
	}{
		{protocol.SourceVSCodeChat, 0, 0},
		{protocol.SourceZed, 50, 0},
		{protocol.SourceCodex, 100, 1},
	} {
		got := harnesses[want.harness]
		if got.SessionCount != 1 || got.TotalTokens != want.tokens || got.APICallCount != want.calls {
			t.Errorf("%s breakdown: got %d sessions, %d tokens, %d calls", want.harness, got.SessionCount, got.TotalTokens, got.APICallCount)
		}
	}
	rows, err := st.GetBreakdown(ctx, QueryFilter{From: &recent}, "harness")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Key == protocol.SourceVSCodeChat {
			t.Error("date filter included an older transcript-only session")
		}
	}
}
