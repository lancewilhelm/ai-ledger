package protocol

import "time"

// FileKind describes the format/mutability of an uploaded raw file.
const (
	FileKindJSONL          = "jsonl"
	FileKindJSONLZst       = "jsonl_zst"
	FileKindSQLiteSnapshot = "sqlite_snapshot"
	FileKindJSON           = "json"
	FileKindOther          = "other"
)

// Source names for known AI harnesses/tools.
const (
	SourceCodex       = "codex"
	SourceClaude      = "claude"
	SourceAntigravity = "antigravity"
	SourceT3          = "t3"
	SourceHermes      = "hermes"
	SourceZed         = "zed"
	SourceVSCodeChat  = "vscode_chat"
	SourceCopilotCLI  = "copilot_cli"
	SourceRates       = "rates"
)

// IngestStatusResponse reports the server's current byte offset and hash for a file,
// allowing the collector to resume incremental uploads cleanly.
type IngestStatusResponse struct {
	Exists       bool   `json:"exists"`
	FileID       string `json:"file_id,omitempty"`
	ByteSize     int64  `json:"byte_size"`
	SHA256       string `json:"sha256,omitempty"`
	HeaderSHA256 string `json:"header_sha256,omitempty"`
	ParseStatus  string `json:"parse_status,omitempty"`
}

// IngestChunkResponse is returned after uploading a chunk or snapshot.
type IngestChunkResponse struct {
	FileID          string `json:"file_id"`
	ByteSize        int64  `json:"byte_size"`
	SHA256          string `json:"sha256"`
	HeaderSHA256    string `json:"header_sha256,omitempty"`
	ParseQueued     bool   `json:"parse_queued"`
	Duplicate       bool   `json:"duplicate,omitempty"`
	ArchivedVersion string `json:"archived_version,omitempty"`
}

// DeviceInfo represents a collector device.
type DeviceInfo struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Hostname      string     `json:"hostname"`
	OS            string     `json:"os"`
	Arch          string     `json:"arch"`
	CreatedAt     time.Time  `json:"created_at"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	FileCount     int64      `json:"file_count"`
	ArchiveBytes  int64      `json:"archive_bytes"`
	SessionCount  int64      `json:"session_count"`
	TotalTokens   int64      `json:"total_tokens"`
	EstimatedCost float64    `json:"estimated_cost_usd"`
}

// RawFileRecord represents a single archived raw file from a device.
type RawFileRecord struct {
	ID           string     `json:"id"`
	DeviceID     string     `json:"device_id"`
	DeviceName   string     `json:"device_name,omitempty"`
	Source       string     `json:"source"`
	RelPath      string     `json:"rel_path"`
	FileKind     string     `json:"file_kind"`
	ArchivePath  string     `json:"archive_path"`
	ByteSize     int64      `json:"byte_size"`
	SHA256       string     `json:"sha256"`
	HeaderSHA256 string     `json:"header_sha256,omitempty"`
	ParseStatus  string     `json:"parse_status"`
	ParseError   string     `json:"parse_error,omitempty"`
	ParsedAt     *time.Time `json:"parsed_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// SessionSummary is the normalized metadata & rolled-up usage for one conversation/session.
type SessionSummary struct {
	ID                   string     `json:"id"`
	NativeID             string     `json:"native_id"`
	DeviceID             string     `json:"device_id"`
	DeviceName           string     `json:"device_name,omitempty"`
	Harness              string     `json:"harness"`
	SourceFileID         string     `json:"source_file_id"`
	ProjectPath          string     `json:"project_path"`
	ProjectName          string     `json:"project_name"`
	GitBranch            string     `json:"git_branch,omitempty"`
	Title                string     `json:"title"`
	ModelPrimary         string     `json:"model_primary"`
	ParentSessionID      string     `json:"parent_session_id,omitempty"`
	IsSidechain          bool       `json:"is_sidechain"`
	Originator           string     `json:"originator,omitempty"`
	StartedAt            time.Time  `json:"started_at"`
	EndedAt              *time.Time `json:"ended_at,omitempty"`
	MessageCount         int        `json:"message_count"`
	ToolCallCount        int        `json:"tool_call_count"`
	APICallCount         int        `json:"api_call_count"`
	InputTokens          int64      `json:"input_tokens"`
	CachedInputTokens    int64      `json:"cached_input_tokens"`
	CacheWrite5mTokens   int64      `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens   int64      `json:"cache_write_1h_tokens"`
	OutputTokens         int64      `json:"output_tokens"`
	ReasoningTokens      int64      `json:"reasoning_tokens"`
	TotalTokens          int64      `json:"total_tokens"`
	EstimatedCostUSD     float64    `json:"estimated_cost_usd"`
	ReportedCostUSD      *float64   `json:"reported_cost_usd,omitempty"`
	MetadataJSON         string     `json:"metadata_json,omitempty"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// MessageRecord is a single normalized conversation turn/message.
type MessageRecord struct {
	ID              string     `json:"id"`
	SessionID       string     `json:"session_id"`
	Ordinal         int        `json:"ordinal"`
	NativeMessageID string     `json:"native_message_id,omitempty"`
	ParentMessageID string     `json:"parent_message_id,omitempty"`
	Role            string     `json:"role"`
	Model           string     `json:"model,omitempty"`
	ContentText     string     `json:"content_text"`
	ThinkingText    string     `json:"thinking_text,omitempty"`
	ContentJSON     string     `json:"content_json,omitempty"`
	StopReason      string     `json:"stop_reason,omitempty"`
	Timestamp       *time.Time `json:"timestamp,omitempty"`
	DurationMs      int64      `json:"duration_ms,omitempty"`
	MetadataJSON    string     `json:"metadata_json,omitempty"`
}

// ToolCallRecord is a normalized tool invocation + output.
type ToolCallRecord struct {
	ID         string     `json:"id"`
	SessionID  string     `json:"session_id"`
	MessageID  string     `json:"message_id,omitempty"`
	Ordinal    int        `json:"ordinal"`
	CallID     string     `json:"call_id,omitempty"`
	ToolName   string     `json:"tool_name"`
	InputJSON  string     `json:"input_json,omitempty"`
	OutputText string     `json:"output_text,omitempty"`
	Status     string     `json:"status,omitempty"`
	IsError    bool       `json:"is_error"`
	Timestamp  *time.Time `json:"timestamp,omitempty"`
	DurationMs int64      `json:"duration_ms,omitempty"`
}

// UsageEventRecord represents one billed LLM API call.
// Token accounting convention:
//   - InputTokens: uncached input tokens only
//   - CachedInputTokens: cache read tokens
//   - CacheWrite5mTokens / CacheWrite1hTokens: cache creation tokens
//   - OutputTokens: total output tokens billed (includes ReasoningTokens)
//   - ReasoningTokens: subset of OutputTokens spent on reasoning/thinking
type UsageEventRecord struct {
	ID                 string    `json:"id"`
	SessionID          string    `json:"session_id"`
	DeviceID           string    `json:"device_id"`
	Harness            string    `json:"harness"`
	Model              string    `json:"model"`
	RequestID          string    `json:"request_id,omitempty"`
	Timestamp          time.Time `json:"timestamp"`
	InputTokens        int64     `json:"input_tokens"`
	CachedInputTokens  int64     `json:"cached_input_tokens"`
	CacheWrite5mTokens int64     `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int64     `json:"cache_write_1h_tokens"`
	OutputTokens       int64     `json:"output_tokens"`
	ReasoningTokens    int64     `json:"reasoning_tokens"`
	WebSearchRequests  int       `json:"web_search_requests,omitempty"`
	WebFetchRequests   int       `json:"web_fetch_requests,omitempty"`
	ContextWindow      int64     `json:"context_window,omitempty"`
	Effort             string    `json:"effort,omitempty"`
	ServiceTier        string    `json:"service_tier,omitempty"`
	EstimatedCostUSD   float64   `json:"estimated_cost_usd"`
	RawUsageJSON       string    `json:"raw_usage_json,omitempty"`
}

// RateLimitSnapshot captures provider quota/rate-limit telemetry when present (e.g. Codex).
type RateLimitSnapshot struct {
	ID               string    `json:"id"`
	DeviceID         string    `json:"device_id"`
	Harness          string    `json:"harness"`
	SessionID        string    `json:"session_id"`
	Timestamp        time.Time `json:"timestamp"`
	LimitID          string    `json:"limit_id"`
	PlanType         string    `json:"plan_type,omitempty"`
	PrimaryUsedPct   *float64  `json:"primary_used_pct,omitempty"`
	SecondaryUsedPct *float64  `json:"secondary_used_pct,omitempty"`
	RawJSON          string    `json:"raw_json,omitempty"`
}

// T3ThreadLink stores T3 Code metadata used to enrich native Codex/Claude/Antigravity sessions.
type T3ThreadLink struct {
	ThreadID         string    `json:"thread_id"`
	NativeSessionID  string    `json:"native_session_id"`
	DeviceID         string    `json:"device_id"`
	Provider         string    `json:"provider"`
	ProjectID        string    `json:"project_id"`
	ProjectTitle     string    `json:"project_title"`
	WorkspaceRoot    string    `json:"workspace_root"`
	ThreadTitle      string    `json:"thread_title"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// SessionDetail bundles a session with all its messages, tool calls, usage events, and rate limits.
type SessionDetail struct {
	Session    SessionSummary      `json:"session"`
	Messages   []MessageRecord     `json:"messages"`
	ToolCalls  []ToolCallRecord    `json:"tool_calls"`
	Usage      []UsageEventRecord  `json:"usage_events"`
	RateLimits []RateLimitSnapshot `json:"rate_limits,omitempty"`
}

// OverviewResponse summarizes usage metrics for the dashboard header cards & top lists.
type OverviewResponse struct {
	Totals       UsageAggregate   `json:"totals"`
	Archive      ArchiveSummary   `json:"archive"`
	ByHarness    []BreakdownRow   `json:"by_harness"`
	ByModel      []BreakdownRow   `json:"by_model"`
	ByDevice     []BreakdownRow   `json:"by_device"`
	ByProject    []BreakdownRow   `json:"by_project"`
}

type ArchiveSummary struct {
	TotalFiles    int64      `json:"total_files"`
	TotalBytes    int64      `json:"total_bytes"`
	ParsedFiles   int64      `json:"parsed_files"`
	ErrorFiles    int64      `json:"error_files"`
	ActiveDevices int64      `json:"active_devices"`
	TotalDevices  int64      `json:"total_devices"`
	LastUpdatedAt *time.Time `json:"last_updated_at,omitempty"`
}

type UsageAggregate struct {
	SessionCount       int64   `json:"session_count"`
	MessageCount       int64   `json:"message_count"`
	ToolCallCount      int64   `json:"tool_call_count"`
	APICallCount       int64   `json:"api_call_count"`
	InputTokens        int64   `json:"input_tokens"`
	CachedInputTokens  int64   `json:"cached_input_tokens"`
	CacheWrite5mTokens int64   `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int64   `json:"cache_write_1h_tokens"`
	OutputTokens       int64   `json:"output_tokens"`
	ReasoningTokens    int64   `json:"reasoning_tokens"`
	TotalTokens        int64   `json:"total_tokens"`
	CacheHitRate       float64 `json:"cache_hit_rate"`
	EstimatedCostUSD   float64 `json:"estimated_cost_usd"`
	ReportedCostUSD    float64 `json:"reported_cost_usd"`
}

type BreakdownRow struct {
	Key                string  `json:"key"`
	Label              string  `json:"label"`
	SessionCount       int64   `json:"session_count"`
	APICallCount       int64   `json:"api_call_count"`
	InputTokens        int64   `json:"input_tokens"`
	CachedInputTokens  int64   `json:"cached_input_tokens"`
	CacheWrite5mTokens int64   `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int64   `json:"cache_write_1h_tokens"`
	OutputTokens       int64   `json:"output_tokens"`
	ReasoningTokens    int64   `json:"reasoning_tokens"`
	TotalTokens        int64   `json:"total_tokens"`
	CacheHitRate       float64 `json:"cache_hit_rate"`
	EstimatedCostUSD   float64 `json:"estimated_cost_usd"`
	CostShare          float64 `json:"cost_share"`
}

type TimeseriesPoint struct {
	Bucket             string  `json:"bucket"`
	Group              string  `json:"group,omitempty"`
	APICallCount       int64   `json:"api_call_count"`
	SessionCount       int64   `json:"session_count"`
	InputTokens        int64   `json:"input_tokens"`
	CachedInputTokens  int64   `json:"cached_input_tokens"`
	CacheWrite5mTokens int64   `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens int64   `json:"cache_write_1h_tokens"`
	OutputTokens       int64   `json:"output_tokens"`
	ReasoningTokens    int64   `json:"reasoning_tokens"`
	TotalTokens        int64   `json:"total_tokens"`
	EstimatedCostUSD   float64 `json:"estimated_cost_usd"`
}

type SearchHit struct {
	MessageID    string     `json:"message_id"`
	SessionID    string     `json:"session_id"`
	SessionTitle string     `json:"session_title"`
	Harness      string     `json:"harness"`
	ProjectName  string     `json:"project_name"`
	DeviceName   string     `json:"device_name"`
	Model        string     `json:"model"`
	Role         string     `json:"role"`
	Snippet      string     `json:"snippet"`
	Timestamp    *time.Time `json:"timestamp,omitempty"`
}

type FilterOptions struct {
	Harnesses     []string          `json:"harnesses"`
	Models        []string          `json:"models"`
	Devices       []FilterDeviceOpt `json:"devices"`
	Projects      []string          `json:"projects"`
	LastUpdatedAt *time.Time        `json:"last_updated_at,omitempty"`
}

type FilterDeviceOpt struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PricingOverrideRecord represents a persisted manual pricing override (USD per 1M tokens).
type PricingOverrideRecord struct {
	Model            string    `json:"model"`
	InputPerM        float64   `json:"input_per_m"`
	CachedInputPerM  float64   `json:"cached_input_per_m"`
	CacheWrite5mPerM float64   `json:"cache_write_5m_per_m"`
	CacheWrite1hPerM float64   `json:"cache_write_1h_per_m"`
	OutputPerM       float64   `json:"output_per_m"`
	Notes            string    `json:"notes,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ObservedModelUsage summarizes usage across all recorded events for a model.
type ObservedModelUsage struct {
	Model            string   `json:"model"`
	Harnesses        []string `json:"harnesses"`
	APICallCount     int64    `json:"api_call_count"`
	TotalTokens      int64    `json:"total_tokens"`
	EstimatedCostUSD float64  `json:"estimated_cost_usd"`
}

// PricingSourceInfo describes an active pricing source layer and its model count.
type PricingSourceInfo struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
	Models int    `json:"models"`
}

// ModelRateView exposes a model's active per-million-token pricing and provenance.
type ModelRateView struct {
	Model                string     `json:"model"`
	Harnesses            []string   `json:"harnesses,omitempty"`
	APICallCount         int64      `json:"api_call_count,omitempty"`
	TotalTokens          int64      `json:"total_tokens,omitempty"`
	EstimatedCostUSD     float64    `json:"estimated_cost_usd,omitempty"`
	MatchedKey           string     `json:"matched_key,omitempty"`
	MatchType            string     `json:"match_type"`
	Source               string     `json:"source"`
	SourceDetail         string     `json:"source_detail"`
	IsOverride           bool       `json:"is_override"`
	InputPerM            float64    `json:"input_per_m"`
	CachedInputPerM      float64    `json:"cached_input_per_m"`
	CacheWrite5mPerM     float64    `json:"cache_write_5m_per_m"`
	CacheWrite1hPerM     float64    `json:"cache_write_1h_per_m"`
	OutputPerM           float64    `json:"output_per_m"`
	Notes                string     `json:"notes,omitempty"`
	UpdatedAt            *time.Time `json:"updated_at,omitempty"`
	BaseSource           string     `json:"base_source,omitempty"`
	BaseDetail           string     `json:"base_detail,omitempty"`
	BaseInputPerM        *float64   `json:"base_input_per_m,omitempty"`
	BaseCachedInputPerM  *float64   `json:"base_cached_input_per_m,omitempty"`
	BaseCacheWrite5mPerM *float64   `json:"base_cache_write_5m_per_m,omitempty"`
	BaseCacheWrite1hPerM *float64   `json:"base_cache_write_1h_per_m,omitempty"`
	BaseOutputPerM       *float64   `json:"base_output_per_m,omitempty"`
}

// PricingResponse is returned by GET /api/pricing.
type PricingResponse struct {
	Sources        []PricingSourceInfo     `json:"sources"`
	ObservedModels []ModelRateView         `json:"observed_models"`
	Catalog        []ModelRateView         `json:"catalog"`
	Overrides      []PricingOverrideRecord `json:"overrides"`
}
