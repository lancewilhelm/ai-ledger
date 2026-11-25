package pricing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Pricing source identifiers in resolution order (lowest to highest priority).
const (
	SourceUnmatched     = "unmatched"
	SourceBuiltin       = "builtin"
	SourceArchivedRates = "archived_rates"
	SourceRatesFile     = "rates_file"
	SourceOverride      = "override"
)

// ModelRate holds per-token USD rates for a model.
type ModelRate struct {
	InputPerToken      float64 `json:"input_cost_per_token"`
	OutputPerToken     float64 `json:"output_cost_per_token"`
	CacheReadPerToken  float64 `json:"cache_read_input_token_cost"`
	CacheWrite5mPerTok float64 `json:"cache_creation_input_token_cost"`
	CacheWrite1hPerTok float64 `json:"cache_creation_input_token_cost_above_1hr"`
}

// PerMillionRates holds USD rates per 1,000,000 tokens for display and editing.
type PerMillionRates struct {
	Input        float64
	CachedInput  float64
	CacheWrite5m float64
	CacheWrite1h float64
	Output       float64
}

// PerMillion converts per-token rates into USD per 1,000,000 tokens.
func (r ModelRate) PerMillion() PerMillionRates {
	const m = 1e6
	return PerMillionRates{
		Input:        r.InputPerToken * m,
		CachedInput:  r.CacheReadPerToken * m,
		CacheWrite5m: r.CacheWrite5mPerTok * m,
		CacheWrite1h: r.CacheWrite1hPerTok * m,
		Output:       r.OutputPerToken * m,
	}
}

// FromPerMillion constructs a ModelRate from USD per 1,000,000 token values.
// Unlike ratePerM, zero values are preserved explicitly so users can set $0 (e.g. free/local models).
func FromPerMillion(inM, cachedReadM, write5mM, write1hM, outM float64) ModelRate {
	const m = 1e6
	return ModelRate{
		InputPerToken:      inM / m,
		OutputPerToken:     outM / m,
		CacheReadPerToken:  cachedReadM / m,
		CacheWrite5mPerTok: write5mM / m,
		CacheWrite1hPerTok: write1hM / m,
	}
}

type sourcedRate struct {
	rate   ModelRate
	detail string
}

// OverrideEntry represents a user-defined manual rate override.
type OverrideEntry struct {
	Model     string
	Rate      ModelRate
	Notes     string
	UpdatedAt time.Time
}

// SourceSummary describes one active layer in the pricing precedence stack.
type SourceSummary struct {
	Kind   string
	Label  string
	Detail string
	Models int
}

// LookupResult reports the resolved rate for a model and where it came from.
type LookupResult struct {
	Found        bool
	MatchedKey   string
	MatchType    string // exact | stripped_prefix | stripped_suffix | prefix | unmatched
	Source       string // override | rates_file | archived_rates | builtin | unmatched
	SourceDetail string
	Rate         ModelRate
	Notes        string
	UpdatedAt    *time.Time
	BaseFound    bool
	BaseSource   string
	BaseDetail   string
	BaseRate     ModelRate
}

// CatalogEntry describes a single model entry in the active pricing catalog.
type CatalogEntry struct {
	Model        string
	Source       string
	SourceDetail string
	Rate         ModelRate
	IsOverride   bool
	Notes        string
	UpdatedAt    *time.Time
	BaseFound    bool
	BaseSource   string
	BaseDetail   string
	BaseRate     ModelRate
}

// Calculator computes estimated USD cost for usage events with transparent source precedence:
//   manual override > custom AI_LEDGER_RATES_FILE > synced T3 usage-model-rates.json > built-in defaults.
type Calculator struct {
	mu              sync.RWMutex
	builtin         map[string]ModelRate
	archived        map[string]sourcedRate
	archivedSources map[string]int
	customFile      map[string]sourcedRate
	customFilePath  string
	overrides       map[string]OverrideEntry
}

// NewCalculator creates a Calculator preloaded with sensible defaults for common models.
func NewCalculator() *Calculator {
	b := builtinRates()
	normBuiltin := make(map[string]ModelRate, len(b))
	for k, v := range b {
		normBuiltin[normalizeKey(k)] = v
	}
	return &Calculator{
		builtin:         normBuiltin,
		archived:        make(map[string]sourcedRate),
		archivedSources: make(map[string]int),
		customFile:      make(map[string]sourcedRate),
		overrides:       make(map[string]OverrideEntry),
	}
}

// LoadT3RatesFile merges rates from a synced LiteLLM / T3 usage-model-rates.json file.
// Supports both {"document": {model: {...}}} and flat {model: {...}} shapes.
func (c *Calculator) LoadT3RatesFile(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return c.loadRatesBytes(raw, SourceArchivedRates, filepath.Base(path))
}

// LoadCustomRatesFile merges rates from a user-configured AI_LEDGER_RATES_FILE path.
func (c *Calculator) LoadCustomRatesFile(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return c.loadRatesBytes(raw, SourceRatesFile, path)
}

// LoadT3RatesBytes merges rates from JSON bytes into the synced T3 rates layer.
func (c *Calculator) LoadT3RatesBytes(raw []byte) (int, error) {
	return c.loadRatesBytes(raw, SourceArchivedRates, "usage-model-rates.json")
}

func (c *Calculator) loadRatesBytes(raw []byte, layer, detail string) (int, error) {
	var wrapper struct {
		Document map[string]ModelRate `json:"document"`
	}
	var parsed map[string]ModelRate
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.Document) > 0 {
		parsed = wrapper.Document
	} else {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return 0, err
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for k, v := range parsed {
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
		nk := normalizeKey(k)
		if nk == "" {
			continue
		}
		if layer == SourceRatesFile {
			c.customFile[nk] = sourcedRate{rate: v, detail: detail}
		} else {
			c.archived[nk] = sourcedRate{rate: v, detail: detail}
		}
		count++
	}
	if layer == SourceRatesFile {
		c.customFilePath = detail
	} else if detail != "" && count > 0 {
		c.archivedSources[detail] = count
	}
	return count, nil
}

// SetOverride inserts or updates a manual model rate override.
func (c *Calculator) SetOverride(entry OverrideEntry) {
	nk := normalizeKey(entry.Model)
	if nk == "" {
		return
	}
	entry.Model = nk
	c.mu.Lock()
	c.overrides[nk] = entry
	c.mu.Unlock()
}

// DeleteOverride removes a manual model rate override.
func (c *Calculator) DeleteOverride(model string) {
	nk := normalizeKey(model)
	if nk == "" {
		return
	}
	c.mu.Lock()
	delete(c.overrides, nk)
	c.mu.Unlock()
}

// ReplaceOverrides replaces all in-memory manual overrides (e.g. on startup from SQLite).
func (c *Calculator) ReplaceOverrides(entries []OverrideEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.overrides = make(map[string]OverrideEntry, len(entries))
	for _, e := range entries {
		nk := normalizeKey(e.Model)
		if nk == "" {
			continue
		}
		e.Model = nk
		c.overrides[nk] = e
	}
}

// Lookup finds the best matching ModelRate for a model name.
func (c *Calculator) Lookup(model string) (ModelRate, bool) {
	res := c.LookupDetailed(model)
	return res.Rate, res.Found
}

// HasOverride reports whether the resolved rate for model comes from a manual override.
func (c *Calculator) HasOverride(model string) bool {
	res := c.LookupDetailed(model)
	return res.Found && res.Source == SourceOverride
}

// LookupDetailed resolves a model's rate and explains which key and source layer matched.
func (c *Calculator) LookupDetailed(model string) LookupResult {
	c.mu.RLock()
	defer c.mu.RUnlock()

	res := c.lookupLocked(model, true)
	if res.Found && res.Source == SourceOverride {
		base := c.lookupLocked(model, false)
		if base.Found {
			res.BaseFound = true
			res.BaseSource = base.Source
			res.BaseDetail = base.SourceDetail
			res.BaseRate = base.Rate
		}
	}
	return res
}

type resolvedKey struct {
	rate   ModelRate
	source string
	detail string
	notes  string
	upd    *time.Time
}

func (c *Calculator) getExactLocked(key string, includeOverrides bool) (resolvedKey, bool) {
	if includeOverrides {
		if ov, ok := c.overrides[key]; ok {
			t := ov.UpdatedAt
			return resolvedKey{
				rate:   ov.Rate,
				source: SourceOverride,
				detail: "Manual override",
				notes:  ov.Notes,
				upd:    &t,
			}, true
		}
	}
	if sr, ok := c.customFile[key]; ok {
		return resolvedKey{
			rate:   sr.rate,
			source: SourceRatesFile,
			detail: sr.detail,
		}, true
	}
	if sr, ok := c.archived[key]; ok {
		return resolvedKey{
			rate:   sr.rate,
			source: SourceArchivedRates,
			detail: sr.detail,
		}, true
	}
	if br, ok := c.builtin[key]; ok {
		return resolvedKey{
			rate:   br,
			source: SourceBuiltin,
			detail: "Built-in catalog",
		}, true
	}
	return resolvedKey{}, false
}

func (c *Calculator) allKeysLocked(includeOverrides bool) map[string]struct{} {
	keys := make(map[string]struct{}, len(c.builtin)+len(c.archived)+len(c.customFile)+len(c.overrides))
	for k := range c.builtin {
		keys[k] = struct{}{}
	}
	for k := range c.archived {
		keys[k] = struct{}{}
	}
	for k := range c.customFile {
		keys[k] = struct{}{}
	}
	if includeOverrides {
		for k := range c.overrides {
			keys[k] = struct{}{}
		}
	}
	return keys
}

func (c *Calculator) lookupLocked(model string, includeOverrides bool) LookupResult {
	norm := normalizeKey(model)
	if norm == "" {
		return LookupResult{MatchType: "unmatched", Source: SourceUnmatched, SourceDetail: "Unpriced"}
	}

	if rk, ok := c.getExactLocked(norm, includeOverrides); ok {
		return LookupResult{
			Found:        true,
			MatchedKey:   norm,
			MatchType:    "exact",
			Source:       rk.source,
			SourceDetail: rk.detail,
			Rate:         rk.rate,
			Notes:        rk.notes,
			UpdatedAt:    rk.upd,
		}
	}

	// Strip common provider prefixes (anthropic/, openai/, google/, etc.)
	if idx := strings.LastIndex(norm, "/"); idx >= 0 {
		trimmed := norm[idx+1:]
		if rk, ok := c.getExactLocked(trimmed, includeOverrides); ok {
			return LookupResult{
				Found:        true,
				MatchedKey:   trimmed,
				MatchType:    "stripped_prefix",
				Source:       rk.source,
				SourceDetail: rk.detail,
				Rate:         rk.rate,
				Notes:        rk.notes,
				UpdatedAt:    rk.upd,
			}
		}
		norm = trimmed
	}

	// Strip trailing effort/variant suffixes like -high, -medium, -low, -xhigh, -max, -preview
	for _, suffix := range []string{"-ultrathink", "-xhigh", "-high", "-medium", "-low", "-max", "-preview", "-latest"} {
		if strings.HasSuffix(norm, suffix) {
			candidate := strings.TrimSuffix(norm, suffix)
			if rk, ok := c.getExactLocked(candidate, includeOverrides); ok {
				return LookupResult{
					Found:        true,
					MatchedKey:   candidate,
					MatchType:    "stripped_suffix",
					Source:       rk.source,
					SourceDetail: rk.detail,
					Rate:         rk.rate,
					Notes:        rk.notes,
					UpdatedAt:    rk.upd,
				}
			}
		}
	}

	// Longest prefix match (handles dated snapshots like claude-opus-4-5-20251101 or gpt-5-2025-08-07)
	bestLen := 0
	var bestKey string
	for k := range c.allKeysLocked(includeOverrides) {
		if strings.HasPrefix(norm, k) && len(k) > bestLen {
			if len(norm) == len(k) || norm[len(k)] == '-' || norm[len(k)] == '.' || norm[len(k)] == ':' {
				bestLen = len(k)
				bestKey = k
			}
		}
	}
	if bestLen > 0 {
		if rk, ok := c.getExactLocked(bestKey, includeOverrides); ok {
			return LookupResult{
				Found:        true,
				MatchedKey:   bestKey,
				MatchType:    "prefix",
				Source:       rk.source,
				SourceDetail: rk.detail,
				Rate:         rk.rate,
				Notes:        rk.notes,
				UpdatedAt:    rk.upd,
			}
		}
	}
	return LookupResult{MatchType: "unmatched", Source: SourceUnmatched, SourceDetail: "Unpriced"}
}

// Catalog returns every known model key across all layers with its active source and rate.
func (c *Calculator) Catalog() []CatalogEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keySet := c.allKeysLocked(true)
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]CatalogEntry, 0, len(keys))
	for _, k := range keys {
		rk, ok := c.getExactLocked(k, true)
		if !ok {
			continue
		}
		entry := CatalogEntry{
			Model:        k,
			Source:       rk.source,
			SourceDetail: rk.detail,
			Rate:         rk.rate,
			IsOverride:   rk.source == SourceOverride,
			Notes:        rk.notes,
			UpdatedAt:    rk.upd,
		}
		if entry.IsOverride {
			base := c.lookupLocked(k, false)
			if base.Found {
				entry.BaseFound = true
				entry.BaseSource = base.Source
				entry.BaseDetail = base.SourceDetail
				entry.BaseRate = base.Rate
			}
		}
		out = append(out, entry)
	}
	return out
}

// Sources returns the pricing layers in resolution priority order (highest priority first).
func (c *Calculator) Sources() []SourceSummary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var out []SourceSummary
	out = append(out, SourceSummary{
		Kind:   SourceOverride,
		Label:  "Manual overrides",
		Detail: "Saved in AI Ledger SQLite database (highest priority)",
		Models: len(c.overrides),
	})
	if c.customFilePath != "" || len(c.customFile) > 0 {
		out = append(out, SourceSummary{
			Kind:   SourceRatesFile,
			Label:  "Custom rates file",
			Detail: c.customFilePath,
			Models: len(c.customFile),
		})
	}
	if len(c.archived) > 0 {
		names := make([]string, 0, len(c.archivedSources))
		for name := range c.archivedSources {
			names = append(names, name)
		}
		sort.Strings(names)
		detail := "usage-model-rates.json"
		if len(names) > 0 {
			detail = strings.Join(names, ", ")
		}
		out = append(out, SourceSummary{
			Kind:   SourceArchivedRates,
			Label:  "Synced T3 / LiteLLM rates",
			Detail: detail,
			Models: len(c.archived),
		})
	} else {
		out = append(out, SourceSummary{
			Kind:   SourceArchivedRates,
			Label:  "Synced T3 / LiteLLM rates",
			Detail: "Not synced yet (~/.t3/userdata/usage-model-rates.json)",
			Models: 0,
		})
	}
	out = append(out, SourceSummary{
		Kind:   SourceBuiltin,
		Label:  "Built-in defaults",
		Detail: "Embedded fallback catalog in ai-ledger-server",
		Models: len(c.builtin),
	})
	return out
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
		"gemini-4-neon":     ratePerM(2.0, 12.0, 0.2, 2.0, 2.0),
		"gemini-4-carbon":   ratePerM(1.25, 10.0, 0.125, 1.25, 1.25),
		"gemini-4-argon":    ratePerM(0.5, 3.0, 0.05, 0.5, 0.5),
		"gemini-4-hydrogen": ratePerM(0.2, 1.0, 0.02, 0.2, 0.2),
		"gemini-3.8-flash":  ratePerM(0.3, 2.5, 0.03, 0.3, 0.3),
		"gemini-3.1-pro":    ratePerM(2.0, 12.0, 0.2, 2.0, 2.0),
		"gemini-3.1-flash":  ratePerM(0.3, 2.5, 0.03, 0.3, 0.3),
		"gemini-3-pro":      ratePerM(2.0, 12.0, 0.2, 2.0, 2.0),
		"gemini-2.5-pro":    ratePerM(1.25, 10.0, 0.125, 1.25, 1.25),
		"gemini-2.5-flash":  ratePerM(0.3, 2.5, 0.03, 0.3, 0.3),
	}
}
