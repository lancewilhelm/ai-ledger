package store

import (
	"context"
	"time"

	"github.com/lancewilhelm/ai-ledger/internal/protocol"
)

// QueryFilter scopes analytics and session queries.
type QueryFilter struct {
	From    *time.Time
	To      *time.Time
	Device  string
	Harness string
	Model   string
	Project string
	Query   string
}

// SessionListParams controls pagination and sorting for /api/sessions.
type SessionListParams struct {
	Filter QueryFilter
	Sort   string // started_at, cost, tokens, messages
	Order  string // desc, asc
	Limit  int
	Offset int
}

// ParsedFileBundle holds all normalized records extracted from a single raw archived file.
type ParsedFileBundle struct {
	Sessions   []protocol.SessionSummary
	Messages   []protocol.MessageRecord
	ToolCalls  []protocol.ToolCallRecord
	Usage      []protocol.UsageEventRecord
	RateLimits []protocol.RateLimitSnapshot
	T3Links    []protocol.T3ThreadLink
}

// Store defines the persistence boundary so SQLite can later be swapped for Postgres
// without changing the ingest pipeline, parsers, or HTTP API.
type Store interface {
	Close() error

	// Devices & Auth
	CreateDevice(ctx context.Context, name, hostname, osName, arch, tokenHash string) (*protocol.DeviceInfo, error)
	AuthenticateDevice(ctx context.Context, tokenHash string) (*protocol.DeviceInfo, error)
	TouchDevice(ctx context.Context, deviceID, hostname, osName, arch string) error
	RevokeDevice(ctx context.Context, deviceID string) error
	ListDevices(ctx context.Context) ([]protocol.DeviceInfo, error)

	// Raw Files
	GetRawFileByKey(ctx context.Context, deviceID, source, relPath string) (*protocol.RawFileRecord, error)
	GetRawFileByID(ctx context.Context, fileID string) (*protocol.RawFileRecord, error)
	UpsertRawFile(ctx context.Context, rec *protocol.RawFileRecord) error
	MarkRawFileParseError(ctx context.Context, fileID, errMsg string) error
	ListRawFiles(ctx context.Context, deviceID, source, status string, limit, offset int) ([]protocol.RawFileRecord, int64, error)
	ListAllRawFiles(ctx context.Context) ([]protocol.RawFileRecord, error)

	// Parsed Data Persistence
	ReplaceFileParsedData(ctx context.Context, fileID string, bundle *ParsedFileBundle) error

	// Analytics & Dashboard Queries
	GetOverview(ctx context.Context, f QueryFilter) (*protocol.OverviewResponse, error)
	GetTimeseries(ctx context.Context, f QueryFilter, bucket, groupBy string) ([]protocol.TimeseriesPoint, error)
	GetBreakdown(ctx context.Context, f QueryFilter, by string) ([]protocol.BreakdownRow, error)
	ListSessions(ctx context.Context, params SessionListParams) ([]protocol.SessionSummary, int64, error)
	GetSessionDetail(ctx context.Context, sessionID string) (*protocol.SessionDetail, error)
	SearchMessages(ctx context.Context, f QueryFilter, limit, offset int) ([]protocol.SearchHit, error)
	GetFilterOptions(ctx context.Context) (*protocol.FilterOptions, error)
}
