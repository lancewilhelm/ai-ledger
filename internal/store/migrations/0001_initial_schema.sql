-- Initial AI Ledger schema (v1).
-- Uses IF NOT EXISTS so databases initialized prior to versioned migrations
-- bootstrap cleanly without losing any existing rows.

CREATE TABLE IF NOT EXISTS devices (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	hostname TEXT NOT NULL DEFAULT '',
	os TEXT NOT NULL DEFAULT '',
	arch TEXT NOT NULL DEFAULT '',
	token_hash TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL,
	last_seen_at TEXT,
	revoked_at TEXT
);

CREATE TABLE IF NOT EXISTS raw_files (
	id TEXT PRIMARY KEY,
	device_id TEXT NOT NULL REFERENCES devices(id),
	source TEXT NOT NULL,
	rel_path TEXT NOT NULL,
	file_kind TEXT NOT NULL,
	archive_path TEXT NOT NULL,
	byte_size INTEGER NOT NULL DEFAULT 0,
	sha256 TEXT NOT NULL DEFAULT '',
	header_sha256 TEXT NOT NULL DEFAULT '',
	parse_status TEXT NOT NULL DEFAULT 'pending',
	parse_error TEXT NOT NULL DEFAULT '',
	parsed_at TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(device_id, source, rel_path)
);
CREATE INDEX IF NOT EXISTS idx_raw_files_device_source ON raw_files(device_id, source);
CREATE INDEX IF NOT EXISTS idx_raw_files_status ON raw_files(parse_status);

CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	native_id TEXT NOT NULL,
	device_id TEXT NOT NULL REFERENCES devices(id),
	harness TEXT NOT NULL,
	source_file_id TEXT NOT NULL REFERENCES raw_files(id) ON DELETE CASCADE,
	project_path TEXT NOT NULL DEFAULT '',
	project_name TEXT NOT NULL DEFAULT '',
	git_branch TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	model_primary TEXT NOT NULL DEFAULT '',
	parent_session_id TEXT NOT NULL DEFAULT '',
	is_sidechain INTEGER NOT NULL DEFAULT 0,
	originator TEXT NOT NULL DEFAULT '',
	started_at TEXT NOT NULL,
	ended_at TEXT,
	message_count INTEGER NOT NULL DEFAULT 0,
	tool_call_count INTEGER NOT NULL DEFAULT 0,
	api_call_count INTEGER NOT NULL DEFAULT 0,
	input_tokens INTEGER NOT NULL DEFAULT 0,
	cached_input_tokens INTEGER NOT NULL DEFAULT 0,
	cache_write_5m_tokens INTEGER NOT NULL DEFAULT 0,
	cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens INTEGER NOT NULL DEFAULT 0,
	estimated_cost_usd REAL NOT NULL DEFAULT 0,
	reported_cost_usd REAL,
	metadata_json TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_source_file ON sessions(source_file_id);
CREATE INDEX IF NOT EXISTS idx_sessions_started_at ON sessions(started_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_harness ON sessions(harness);
CREATE INDEX IF NOT EXISTS idx_sessions_device ON sessions(device_id);
CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_name);
CREATE INDEX IF NOT EXISTS idx_sessions_native ON sessions(native_id);

CREATE TABLE IF NOT EXISTS messages (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	ordinal INTEGER NOT NULL,
	native_message_id TEXT NOT NULL DEFAULT '',
	parent_message_id TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	content_text TEXT NOT NULL DEFAULT '',
	thinking_text TEXT NOT NULL DEFAULT '',
	content_json TEXT NOT NULL DEFAULT '',
	stop_reason TEXT NOT NULL DEFAULT '',
	timestamp TEXT,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	metadata_json TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_messages_session_ord ON messages(session_id, ordinal);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
	message_id UNINDEXED,
	session_id UNINDEXED,
	content_text,
	thinking_text,
	tokenize = 'porter unicode61'
);

CREATE TABLE IF NOT EXISTS tool_calls (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	message_id TEXT NOT NULL DEFAULT '',
	ordinal INTEGER NOT NULL,
	call_id TEXT NOT NULL DEFAULT '',
	tool_name TEXT NOT NULL,
	input_json TEXT NOT NULL DEFAULT '',
	output_text TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT '',
	is_error INTEGER NOT NULL DEFAULT 0,
	timestamp TEXT,
	duration_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_tool_calls_session_ord ON tool_calls(session_id, ordinal);

CREATE TABLE IF NOT EXISTS usage_events (
	id TEXT PRIMARY KEY,
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	device_id TEXT NOT NULL REFERENCES devices(id),
	harness TEXT NOT NULL,
	model TEXT NOT NULL,
	request_id TEXT NOT NULL DEFAULT '',
	timestamp TEXT NOT NULL,
	input_tokens INTEGER NOT NULL DEFAULT 0,
	cached_input_tokens INTEGER NOT NULL DEFAULT 0,
	cache_write_5m_tokens INTEGER NOT NULL DEFAULT 0,
	cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens INTEGER NOT NULL DEFAULT 0,
	web_search_requests INTEGER NOT NULL DEFAULT 0,
	web_fetch_requests INTEGER NOT NULL DEFAULT 0,
	context_window INTEGER NOT NULL DEFAULT 0,
	effort TEXT NOT NULL DEFAULT '',
	service_tier TEXT NOT NULL DEFAULT '',
	estimated_cost_usd REAL NOT NULL DEFAULT 0,
	raw_usage_json TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_usage_ts ON usage_events(timestamp);
CREATE INDEX IF NOT EXISTS idx_usage_session ON usage_events(session_id);
CREATE INDEX IF NOT EXISTS idx_usage_device ON usage_events(device_id);
CREATE INDEX IF NOT EXISTS idx_usage_harness ON usage_events(harness);
CREATE INDEX IF NOT EXISTS idx_usage_model ON usage_events(model);

CREATE TABLE IF NOT EXISTS rate_limit_snapshots (
	id TEXT PRIMARY KEY,
	device_id TEXT NOT NULL REFERENCES devices(id),
	harness TEXT NOT NULL,
	session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	timestamp TEXT NOT NULL,
	limit_id TEXT NOT NULL DEFAULT '',
	plan_type TEXT NOT NULL DEFAULT '',
	primary_used_pct REAL,
	secondary_used_pct REAL,
	raw_json TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_rate_limits_session ON rate_limit_snapshots(session_id);

CREATE TABLE IF NOT EXISTS t3_thread_links (
	thread_id TEXT NOT NULL,
	native_session_id TEXT NOT NULL,
	device_id TEXT NOT NULL REFERENCES devices(id),
	provider TEXT NOT NULL DEFAULT '',
	project_id TEXT NOT NULL DEFAULT '',
	project_title TEXT NOT NULL DEFAULT '',
	workspace_root TEXT NOT NULL DEFAULT '',
	thread_title TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL,
	PRIMARY KEY (device_id, thread_id, native_session_id)
);
CREATE INDEX IF NOT EXISTS idx_t3_links_native ON t3_thread_links(native_session_id);
