package store

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// IRC Server
// ---------------------------------------------------------------------------

// ServerRecord represents an IRC server stored in the database.
type ServerRecord struct {
	ID              int64      `json:"id"`
	Address         string     `json:"address"`
	Port            int        `json:"port"`
	AutoConnect     bool       `json:"auto_connect"`
	Status          string     `json:"status"` // disconnected, connected, reconnecting
	LastConnectedAt *time.Time `json:"last_connected_at,omitempty"`
	RetryCount      int        `json:"retry_count"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// IRC Channel
// ---------------------------------------------------------------------------

// ChannelRecord represents an IRC channel stored in the database.
type ChannelRecord struct {
	ID       int64  `json:"id"`
	ServerID int64  `json:"server_id"`
	Name     string `json:"name"`
	AutoJoin bool   `json:"auto_join"`
	Topic    string `json:"topic,omitempty"`
	Joined   bool   `json:"joined"`
}

// ---------------------------------------------------------------------------
// Download status constants
// ---------------------------------------------------------------------------

const (
	DownloadStatusQueued      = "queued"
	DownloadStatusDownloading = "downloading"
	DownloadStatusCompleted   = "completed"
	DownloadStatusFailed      = "failed"
	DownloadStatusPaused      = "paused"
	DownloadStatusSkipped     = "skipped_existing"
)

// DownloadRecord represents a download job stored in the database.
type DownloadRecord struct {
	ID            int64      `json:"id"`
	PackMessage   string     `json:"pack_message"`
	Bot           string     `json:"bot"`
	ServerAddress string     `json:"server_address"`
	Channel       string     `json:"channel"`
	Filename      string     `json:"filename"`
	FileSize      int64      `json:"file_size"`
	Status        string     `json:"status"` // queued, downloading, completed, failed, paused, skipped_existing
	ProgressBytes int64      `json:"progress_bytes"`
	SpeedBPS      int64      `json:"speed_bps,omitempty"`
	ErrorMessage  string     `json:"error_message,omitempty"`
	RetryCount    int        `json:"retry_count"` // number of auto-retry attempts
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Priority      int        `json:"priority"` // lower = higher priority (used only for queue ordering)
}

// ---------------------------------------------------------------------------
// Search Cache
// ---------------------------------------------------------------------------

// SearchCacheEntry represents a cached search result.
type SearchCacheEntry struct {
	QueryKey       string    `json:"query_key"`
	Provider       string    `json:"provider"`
	PayloadJSON    string    `json:"payload_json"`
	FetchedAt      time.Time `json:"fetched_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	StaleExpiresAt time.Time `json:"stale_expires_at"`
}

// ---------------------------------------------------------------------------
// Search Presets
// ---------------------------------------------------------------------------

// SearchPreset represents a saved search preset.
type SearchPreset struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Query       string    `json:"query"`
	FiltersJSON string    `json:"filters_json,omitempty"`
	IsDefault   bool      `json:"is_default"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Watchlist
// ---------------------------------------------------------------------------

// Watchlist represents a saved watchlist for periodic search and notification.
type Watchlist struct {
	ID                   int64           `json:"id"`
	Name                 string          `json:"name"`
	Query                string          `json:"query"`
	IntervalMinutes      int             `json:"interval_minutes"`
	FiltersJSON          string          `json:"filters_json,omitempty"`
	NotifyEnabled        bool            `json:"notify_enabled"`
	Enabled              bool            `json:"enabled"`
	AutoEnqueue          bool            `json:"auto_enqueue"`
	LastCheckedAt        *time.Time      `json:"last_checked_at,omitempty"`
	LastMatchFingerprint string          `json:"last_match_fingerprint,omitempty"`
	LastResultsJSON      json.RawMessage `json:"last_results,omitempty"`
	LastNotifiedAt       *time.Time      `json:"last_notified_at,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Provider Stats
// ---------------------------------------------------------------------------

// ProviderStats represents collected metrics for a search provider.
type ProviderStats struct {
	Provider     string    `json:"provider"`
	WindowStart  time.Time `json:"window_start"`
	WindowEnd    time.Time `json:"window_end"`
	Requests     int       `json:"requests"`
	Successes    int       `json:"successes"`
	Timeouts     int       `json:"timeouts"`
	Failures     int       `json:"failures"`
	AvgLatencyMs float64   `json:"avg_latency_ms"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Schema Version
// ---------------------------------------------------------------------------

// SchemaVersion tracks the current schema version applied to the database.
type SchemaVersion struct {
	Version   int       `json:"version"`
	AppliedAt time.Time `json:"applied_at"`
}

// ---------------------------------------------------------------------------
// Helper types
// ---------------------------------------------------------------------------

// HistoryFilter holds optional filter criteria for download history queries.
type HistoryFilter struct {
	Filename   string   `json:"filename"`
	Bot        string   `json:"bot"`
	StatusList []string `json:"status_list"`
	MinBytes   int64    `json:"min_bytes"`
	MaxBytes   int64    `json:"max_bytes"`
	DateFrom   string   `json:"date_from"` // YYYY-MM-DD
	DateTo     string   `json:"date_to"`   // YYYY-MM-DD
}

// DownloadHistoryPage holds a paginated list of download history records.
type DownloadHistoryPage struct {
	Downloads  []DownloadRecord `json:"downloads"`
	TotalCount int              `json:"total_count"`
	Page       int              `json:"page"`
	PageSize   int              `json:"page_size"`
	TotalPages int              `json:"total_pages"`
}
