// Mirrors internal/protocol/protocol.go wire types.

export interface DeviceInfo {
  id: string
  name: string
  hostname: string
  os: string
  arch: string
  created_at: string
  last_seen_at?: string
  revoked_at?: string
  file_count: number
  archive_bytes: number
  session_count: number
  total_tokens: number
  estimated_cost_usd: number
}

export interface RawFileRecord {
  id: string
  device_id: string
  device_name?: string
  source: string
  rel_path: string
  file_kind: string
  archive_path: string
  byte_size: number
  sha256: string
  header_sha256?: string
  parse_status: string
  parse_error?: string
  parsed_at?: string
  created_at: string
  updated_at: string
}

export interface SessionSummary {
  id: string
  native_id: string
  device_id: string
  device_name?: string
  harness: string
  source_file_id: string
  project_path: string
  project_name: string
  git_branch?: string
  title: string
  model_primary: string
  parent_session_id?: string
  is_sidechain: boolean
  originator?: string
  started_at: string
  ended_at?: string
  message_count: number
  tool_call_count: number
  api_call_count: number
  input_tokens: number
  cached_input_tokens: number
  cache_write_5m_tokens: number
  cache_write_1h_tokens: number
  output_tokens: number
  reasoning_tokens: number
  total_tokens: number
  estimated_cost_usd: number
  reported_cost_usd?: number
  metadata_json?: string
  updated_at: string
}

export interface MessageRecord {
  id: string
  session_id: string
  ordinal: number
  native_message_id?: string
  parent_message_id?: string
  role: string
  model?: string
  content_text: string
  thinking_text?: string
  content_json?: string
  stop_reason?: string
  timestamp?: string
  duration_ms?: number
  metadata_json?: string
}

export interface ToolCallRecord {
  id: string
  session_id: string
  message_id?: string
  ordinal: number
  call_id?: string
  tool_name: string
  input_json?: string
  output_text?: string
  status?: string
  is_error: boolean
  timestamp?: string
  duration_ms?: number
}

export interface UsageEventRecord {
  id: string
  session_id: string
  device_id: string
  harness: string
  model: string
  request_id?: string
  timestamp: string
  input_tokens: number
  cached_input_tokens: number
  cache_write_5m_tokens: number
  cache_write_1h_tokens: number
  output_tokens: number
  reasoning_tokens: number
  web_search_requests?: number
  web_fetch_requests?: number
  context_window?: number
  effort?: string
  service_tier?: string
  estimated_cost_usd: number
  raw_usage_json?: string
}

export interface RateLimitSnapshot {
  id: string
  device_id: string
  harness: string
  session_id: string
  timestamp: string
  limit_id: string
  plan_type?: string
  primary_used_pct?: number
  secondary_used_pct?: number
  raw_json?: string
}

export interface SessionDetail {
  session: SessionSummary
  messages: MessageRecord[] | null
  tool_calls: ToolCallRecord[] | null
  usage_events: UsageEventRecord[] | null
  rate_limits?: RateLimitSnapshot[] | null
}

export interface ArchiveSummary {
  total_files: number
  total_bytes: number
  parsed_files: number
  error_files: number
  active_devices: number
  total_devices: number
}

export interface UsageAggregate {
  session_count: number
  message_count: number
  tool_call_count: number
  api_call_count: number
  input_tokens: number
  cached_input_tokens: number
  cache_write_5m_tokens: number
  cache_write_1h_tokens: number
  output_tokens: number
  reasoning_tokens: number
  total_tokens: number
  cache_hit_rate: number
  estimated_cost_usd: number
  reported_cost_usd: number
}

export interface BreakdownRow {
  key: string
  label: string
  session_count: number
  api_call_count: number
  input_tokens: number
  cached_input_tokens: number
  cache_write_5m_tokens: number
  cache_write_1h_tokens: number
  output_tokens: number
  reasoning_tokens: number
  total_tokens: number
  cache_hit_rate: number
  estimated_cost_usd: number
  cost_share: number
}

export interface OverviewResponse {
  totals: UsageAggregate
  archive: ArchiveSummary
  by_harness: BreakdownRow[] | null
  by_model: BreakdownRow[] | null
  by_device: BreakdownRow[] | null
  by_project: BreakdownRow[] | null
}

export interface TimeseriesPoint {
  bucket: string
  group?: string
  api_call_count: number
  session_count: number
  input_tokens: number
  cached_input_tokens: number
  cache_write_5m_tokens: number
  cache_write_1h_tokens: number
  output_tokens: number
  reasoning_tokens: number
  total_tokens: number
  estimated_cost_usd: number
}

export interface TimeseriesResponse {
  bucket: string
  group_by: string
  points: TimeseriesPoint[] | null
}

export interface SearchHit {
  message_id: string
  session_id: string
  session_title: string
  harness: string
  project_name: string
  device_name: string
  model: string
  role: string
  snippet: string
  timestamp?: string
}

export interface FilterOptions {
  harnesses: string[] | null
  models: string[] | null
  devices: { id: string; name: string }[] | null
  projects: string[] | null
}

export type RangePreset = '24h' | '7d' | '30d' | '90d' | 'all'

export interface Filters {
  range: RangePreset
  harness: string
  model: string
  device: string
  project: string
}

export interface PricingOverrideRecord {
  model: string
  input_per_m: number
  cached_input_per_m: number
  cache_write_5m_per_m: number
  cache_write_1h_per_m: number
  output_per_m: number
  notes?: string
  updated_at: string
}

export interface PricingSourceInfo {
  kind: 'override' | 'rates_file' | 'archived_rates' | 'builtin' | string
  label: string
  detail: string
  models: number
}

export interface ModelRateView {
  model: string
  harnesses?: string[]
  api_call_count?: number
  total_tokens?: number
  estimated_cost_usd?: number
  matched_key?: string
  match_type: string
  source: 'override' | 'rates_file' | 'archived_rates' | 'builtin' | 'unmatched' | string
  source_detail: string
  is_override: boolean
  input_per_m: number
  cached_input_per_m: number
  cache_write_5m_per_m: number
  cache_write_1h_per_m: number
  output_per_m: number
  notes?: string
  updated_at?: string
  base_source?: string
  base_detail?: string
  base_input_per_m?: number
  base_cached_input_per_m?: number
  base_cache_write_5m_per_m?: number
  base_cache_write_1h_per_m?: number
  base_output_per_m?: number
}

export interface PricingResponse {
  sources: PricingSourceInfo[]
  observed_models: ModelRateView[]
  catalog: ModelRateView[]
  overrides: PricingOverrideRecord[]
}
