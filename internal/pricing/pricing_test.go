package pricing_test

import (
	"math"
	"testing"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/pricing"
)

func TestPricingSourcePrecedenceAndOverrides(t *testing.T) {
	calc := pricing.NewCalculator()

	// 1. Built-in default lookup with provider prefix + dated suffix
	lk := calc.LookupDetailed("anthropic/claude-opus-4-6-20260201")
	if !lk.Found || lk.Source != pricing.SourceBuiltin || lk.MatchedKey != "claude-opus-4-6" {
		t.Fatalf("unexpected builtin lookup: %+v", lk)
	}

	// 2. Synced T3 rates layer overrides built-in
	t3JSON := `{"document":{"claude-opus-4-6":{"input_cost_per_token":0.000012,"output_cost_per_token":0.000060}}}`
	if n, err := calc.LoadT3RatesBytes([]byte(t3JSON)); err != nil || n != 1 {
		t.Fatalf("LoadT3RatesBytes: n=%d err=%v", n, err)
	}
	lk = calc.LookupDetailed("claude-opus-4-6")
	if lk.Source != pricing.SourceArchivedRates || math.Abs(lk.Rate.PerMillion().Input-12.0) > 1e-6 {
		t.Fatalf("expected archived_rates $12/M, got %+v", lk)
	}

	// 3. Manual override beats synced T3 rates and exposes base fallback
	now := time.Now().UTC()
	calc.SetOverride(pricing.OverrideEntry{
		Model:     "claude-opus-4-6",
		Rate:      pricing.FromPerMillion(5.0, 0.5, 6.25, 10.0, 25.0),
		Notes:     "Custom contract rate",
		UpdatedAt: now,
	})
	if !calc.HasOverride("anthropic/claude-opus-4-6") {
		t.Fatalf("expected HasOverride true for prefixed model")
	}
	lk = calc.LookupDetailed("claude-opus-4-6")
	if lk.Source != pricing.SourceOverride || !lk.BaseFound || lk.BaseSource != pricing.SourceArchivedRates {
		t.Fatalf("expected override with archived_rates base, got %+v", lk)
	}
	if math.Abs(lk.Rate.PerMillion().Input-5.0) > 1e-6 || math.Abs(lk.BaseRate.PerMillion().Input-12.0) > 1e-6 {
		t.Fatalf("unexpected override/base rates: active=%+v base=%+v", lk.Rate.PerMillion(), lk.BaseRate.PerMillion())
	}

	// 4. Zero-cost manual override (e.g. local model) is respected
	calc.SetOverride(pricing.OverrideEntry{
		Model:     "ollama/qwen3",
		Rate:      pricing.FromPerMillion(0, 0, 0, 0, 0),
		Notes:     "Local GPU",
		UpdatedAt: now,
	})
	if cost := calc.EstimateCost("ollama/qwen3", 1_000_000, 0, 0, 0, 500_000); cost != 0 {
		t.Fatalf("expected $0 cost for local override, got %v", cost)
	}

	// 5. Deleting override reverts cleanly to synced T3 rate
	calc.DeleteOverride("claude-opus-4-6")
	lk = calc.LookupDetailed("claude-opus-4-6")
	if lk.Source != pricing.SourceArchivedRates || math.Abs(lk.Rate.PerMillion().Input-12.0) > 1e-6 {
		t.Fatalf("expected revert to archived_rates $12/M, got %+v", lk)
	}
}
