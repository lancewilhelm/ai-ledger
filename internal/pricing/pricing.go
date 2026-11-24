package pricing

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// ModelRate holds per-token USD rates for a model.
type ModelRate struct {
	InputPerToken       float64 `json:"input_cost_per_token"`
	OutputPerToken      float64 `json:"output_cost_per_token"`
	CacheReadPerToken   float64 `json:"cache_read_input_token_cost"`
	CacheWrite5mPerTok  float64 `json:"cache_creation_input_token_cost"`
	CacheWrite1hPerTok  float64 `json:"cache_creation_input_token_cost_above_1hr"`
}

// Calculator computes estimated USD cost for usage events.
type Calculator struct {
	mu    sync.RWMutex
	rates map[string]ModelRate
}

// NewCalculator creates a Calculator preloaded with sensible defaults for common models.
func NewCalculator() *Calculator {
	c := &Calculator{
		rates: make(map[string]ModelRate, 64),
	}
	for k, v := range builtinRates() {
		c.rates[normalizeKey(k)] = v
	}
	return c
}

// LoadT3RatesFile merges rates from a LiteLLM / T3 usage-model-rates.json file.
// Supports both {"document": {model: {...}}} and flat {model: {...}} shapes.
func (c *Calculator) LoadT3RatesFile(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return c.LoadT3RatesBytes(raw)
}

// LoadT3RatesBytes merges rates from JSON bytes.
func (c *Calculator) LoadT3RatesBytes(raw []byte) (int, error) {
	var wrapper struct {
		Document map[string]ModelRate `json:"document"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.Document) > 0 {
		return c.mergeRates(wrapper.Document), nil
	}
	var flat map[string]ModelRate
	if err := json.Unmarshal(raw, &flat); err != nil {
		return 0, err
	}
	return c.mergeRates(flat), nil
}

func (c *Calculator) mergeRates(m map[string]ModelRate) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for k, v := range m {
		if v.InputPerToken == 0 && v.OutputPerToken == 0 {
			continue
		}
		if v.CacheReadPerToken == 0 && v.InputPerToken > 0 {
			v.CacheReadPerToken = v.InputPerToken * 0.1
		}
		if v.CacheWrite5mPerTok == 0 && v.InputPerToken > 0 {
			v.CacheWrite5mPerTok = v.InputPerToken * 1.25
		}
		if v.CacheWrite1hPerTok == 0 && v.InputPerToken > 0 {
			v.CacheWrite1hPerTok = v.InputPerToken * 2.0
		}
		c.rates[normalizeKey(k)] = v
		count++
	}
	return count
}

// Lookup finds the best matching ModelRate for a model name.
func (c *Calculator) Lookup(model string) (ModelRate, bool) {
	norm := normalizeKey(model)
	if norm == "" {
		return ModelRate{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	if r, ok := c.rates[norm]; ok {
		return r, true
	}

	// Strip common provider prefixes (anthropic/, openai/, google/, etc.)
	if idx := strings.LastIndex(norm, "/"); idx >= 0 {
		trimmed := norm[idx+1:]
		if r, ok := c.rates[trimmed]; ok {
			return r, true
		}
		norm = trimmed
	}

	// Strip trailing effort/variant suffixes like -high, -medium, -low, -xhigh, -max, -preview
	for _, suffix := range []string{"-ultrathink", "-xhigh", "-high", "-medium", "-low", "-max", "-preview", "-latest"} {
		if strings.HasSuffix(norm, suffix) {
			candidate := strings.TrimSuffix(norm, suffix)
			if r, ok := c.rates[candidate]; ok {
				return r, true
			}
		}
	}

	// Longest prefix match (handles dated snapshots like claude-opus-4-5-20251101 or gpt-5-2025-08-07)
	bestLen := 0
	var best ModelRate
	for k, r := range c.rates {
		if strings.HasPrefix(norm, k) && len(k) > bestLen {
			// Ensure boundary match (-, ., :, or exact)
			if len(norm) == len(k) || norm[len(k)] == '-' || norm[len(k)] == '.' || norm[len(k)] == ':' {
				bestLen = len(k)
				best = r
			}
		}
	}
	if bestLen > 0 {
		return best, true
	}
	return ModelRate{}, false
}

// EstimateCost calculates USD cost for a single usage event.
func (c *Calculator) EstimateCost(model string, input, cachedRead, cacheWrite5m, cacheWrite1h, output int64) float64 {
	rate, ok := c.Lookup(model)
	if !ok {
		return 0
	}
	if input < 0 {
		input = 0
	}
	cost := float64(input)*rate.InputPerToken +
		float64(cachedRead)*rate.CacheReadPerToken +
		float64(cacheWrite5m)*rate.CacheWrite5mPerTok +
		float64(cacheWrite1h)*rate.CacheWrite1hPerTok +
		float64(output)*rate.OutputPerToken
	return cost
}

func normalizeKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func ratePerM(inM, outM, readM, write5mM, write1hM float64) ModelRate {
	const m = 1e6
	if readM == 0 {
		readM = inM * 0.1
	}
	if write5mM == 0 {
		write5mM = inM * 1.25
	}
	if write1hM == 0 {
		write1hM = inM * 2.0
	}
	return ModelRate{
		InputPerToken:      inM / m,
		OutputPerToken:     outM / m,
		CacheReadPerToken:  readM / m,
		CacheWrite5mPerTok: write5mM / m,
		CacheWrite1hPerTok: write1hM / m,
	}
}

func builtinRates() map[string]ModelRate {
	return map[string]ModelRate{
		// Anthropic Claude
		"claude-opus-5-5":   ratePerM(15.0, 75.0, 1.5, 18.75, 30.0),
		"claude-opus-5":     ratePerM(15.0, 75.0, 1.5, 18.75, 30.0),
		"claude-sonnet-5-5": ratePerM(3.0, 15.0, 0.3, 3.75, 6.0),
		"claude-sonnet-5":   ratePerM(2.0, 10.0, 0.2, 2.5, 4.0),
		"claude-haiku-5-5":  ratePerM(1.0, 5.0, 0.1, 1.25, 2.0),
		"claude-fable-5-2":  ratePerM(5.0, 25.0, 0.5, 6.25, 10.0),
		"claude-opus-4-6":   ratePerM(15.0, 75.0, 1.5, 18.75, 30.0),
		"claude-opus-4.6":   ratePerM(15.0, 75.0, 1.5, 18.75, 30.0),
		"claude-opus-4-5":   ratePerM(5.0, 25.0, 0.5, 6.25, 10.0),
		"claude-opus-4-1":   ratePerM(15.0, 75.0, 1.5, 18.75, 30.0),
		"claude-opus-4":     ratePerM(15.0, 75.0, 1.5, 18.75, 30.0),
		"claude-sonnet-4-6": ratePerM(3.0, 15.0, 0.3, 3.75, 6.0),
		"claude-sonnet-4-5": ratePerM(3.0, 15.0, 0.3, 3.75, 6.0),
		"claude-sonnet-4":   ratePerM(3.0, 15.0, 0.3, 3.75, 6.0),
		"claude-haiku-4-5":  ratePerM(1.0, 5.0, 0.1, 1.25, 2.0),
		"claude-3-5-sonnet": ratePerM(3.0, 15.0, 0.3, 3.75, 6.0),
		"claude-3-5-haiku":  ratePerM(0.8, 4.0, 0.08, 1.0, 1.6),

		// OpenAI / Codex
		"gpt-6.2-sol":        ratePerM(10.0, 50.0, 1.0, 12.5, 12.5),
		"gpt-6.2-astra":      ratePerM(10.0, 50.0, 1.0, 12.5, 12.5),
		"gpt-6.1-terra":      ratePerM(10.0, 50.0, 1.0, 12.5, 12.5),
		"gpt-6.1-sol":        ratePerM(10.0, 50.0, 1.0, 12.5, 12.5),
		"gpt-6-astra":        ratePerM(10.0, 50.0, 1.0, 12.5, 12.5),
		"gpt-6-sol":          ratePerM(10.0, 50.0, 1.0, 12.5, 12.5),
		"gpt-6-luna":         ratePerM(2.5, 15.0, 0.25, 3.125, 3.125),
		"gpt-5.4":            ratePerM(2.5, 15.0, 0.25, 2.5, 2.5),
		"gpt-5.3-codex":      ratePerM(1.75, 14.0, 0.175, 1.75, 1.75),
		"gpt-5.2-codex":      ratePerM(1.75, 14.0, 0.175, 1.75, 1.75),
		"gpt-5.2":            ratePerM(1.75, 14.0, 0.175, 1.75, 1.75),
		"gpt-5.1-codex-max":  ratePerM(1.25, 10.0, 0.125, 1.25, 1.25),
		"gpt-5.1-codex-mini": ratePerM(0.25, 2.0, 0.025, 0.25, 0.25),
		"gpt-5.1-codex":      ratePerM(1.25, 10.0, 0.125, 1.25, 1.25),
		"gpt-5.1":            ratePerM(1.25, 10.0, 0.125, 1.25, 1.25),
		"gpt-5-codex":        ratePerM(1.25, 10.0, 0.125, 1.25, 1.25),
		"gpt-5-mini":         ratePerM(0.25, 2.0, 0.025, 0.25, 0.25),
		"gpt-5-nano":         ratePerM(0.05, 0.4, 0.005, 0.05, 0.05),
		"gpt-5":              ratePerM(1.25, 10.0, 0.125, 1.25, 1.25),
		"gpt-4o-mini":        ratePerM(0.15, 0.6, 0.075, 0.15, 0.15),
		"gpt-4o":             ratePerM(2.5, 10.0, 1.25, 2.5, 2.5),
		"o3-mini":            ratePerM(1.1, 4.4, 0.55, 1.1, 1.1),
		"o3":                 ratePerM(2.0, 8.0, 0.5, 2.0, 2.0),
		"o4-mini":            ratePerM(1.1, 4.4, 0.275, 1.1, 1.1),

		// Google Gemini / Antigravity
		"gemini-4-neon":       ratePerM(2.0, 12.0, 0.2, 2.0, 2.0),
		"gemini-4-carbon":     ratePerM(1.25, 10.0, 0.125, 1.25, 1.25),
		"gemini-4-argon":      ratePerM(0.5, 3.0, 0.05, 0.5, 0.5),
		"gemini-4-hydrogen":   ratePerM(0.2, 1.0, 0.02, 0.2, 0.2),
		"gemini-3.8-flash":    ratePerM(0.3, 2.5, 0.03, 0.3, 0.3),
		"gemini-3.1-pro":      ratePerM(2.0, 12.0, 0.2, 2.0, 2.0),
		"gemini-3.1-flash":    ratePerM(0.3, 2.5, 0.03, 0.3, 0.3),
		"gemini-3-pro":        ratePerM(2.0, 12.0, 0.2, 2.0, 2.0),
		"gemini-2.5-pro":      ratePerM(1.25, 10.0, 0.125, 1.25, 1.25),
		"gemini-2.5-flash":    ratePerM(0.3, 2.5, 0.03, 0.3, 0.3),
	}
}
