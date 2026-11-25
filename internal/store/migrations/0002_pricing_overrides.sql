-- Manual per-model pricing overrides (v2).
-- Rates are stored in USD per 1,000,000 tokens.

CREATE TABLE IF NOT EXISTS pricing_overrides (
	model TEXT PRIMARY KEY,
	input_per_m REAL NOT NULL DEFAULT 0,
	cached_input_per_m REAL NOT NULL DEFAULT 0,
	cache_write_5m_per_m REAL NOT NULL DEFAULT 0,
	cache_write_1h_per_m REAL NOT NULL DEFAULT 0,
	output_per_m REAL NOT NULL DEFAULT 0,
	notes TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);
