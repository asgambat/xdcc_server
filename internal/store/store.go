// Package store provides persistence for the xdcc-server using SQLite.
package store

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// Focused interfaces — each consumer should depend only on the methods it needs.
// ---------------------------------------------------------------------------

// ServerStore covers IRC server and channel persistence.
type ServerStore interface {
	AddServer(ctx context.Context, s ServerRecord) (int64, error)
	GetServer(ctx context.Context, id int64) (*ServerRecord, error)
	ListServers(ctx context.Context) ([]ServerRecord, error)
	UpdateServer(ctx context.Context, s ServerRecord) error
	DeleteServer(ctx context.Context, id int64) error
	SetServerStatus(ctx context.Context, id int64, status string) error
	SetServerConnected(ctx context.Context, id int64) error
	IncrementServerRetry(ctx context.Context, id int64) error
	ResetAllServerStatuses(ctx context.Context) error

	AddChannel(ctx context.Context, c ChannelRecord) (int64, error)
	GetChannelsByServer(ctx context.Context, serverID int64) ([]ChannelRecord, error)
	GetChannelsByServerAndName(ctx context.Context, serverID int64, name string) (*ChannelRecord, error)
	UpdateChannel(ctx context.Context, c ChannelRecord) error
	DeleteChannel(ctx context.Context, id int64) error
	SetChannelJoined(ctx context.Context, id int64, joined bool) error
	UpdateChannelTopic(ctx context.Context, id int64, topic string) error
	UpdateChannelAvgSpeed(ctx context.Context, serverAddress, channelName string, avgSpeed float64) error
	GetAutoJoinChannels(ctx context.Context) ([]ChannelRecord, error)
}

// DownloadStore covers download queue and history persistence.
type DownloadStore interface {
	EnqueueDownload(ctx context.Context, d DownloadRecord) (int64, error)
	GetDownload(ctx context.Context, id int64) (*DownloadRecord, error)
	GetQueue(ctx context.Context) ([]DownloadRecord, error)
	GetQueueByChannel(ctx context.Context, channel string) ([]DownloadRecord, error)
	GetActiveDownloads(ctx context.Context) ([]DownloadRecord, error)
	GetPendingByChannel(ctx context.Context, channel string) ([]DownloadRecord, error)
	UpdateDownloadProgress(ctx context.Context, id int64, progressBytes int64, speedBPS int64) error
	MarkDownloadStarted(ctx context.Context, id int64) error
	MarkDownloadCompleted(ctx context.Context, id int64, filename string, fileSize int64) error
	MarkDownloadFailed(ctx context.Context, id int64, errMsg string) error
	MarkDownloadSkipped(ctx context.Context, id int64) error
	MarkDownloadPaused(ctx context.Context, id int64) error
	MarkDownloadRetry(ctx context.Context, id int64, newStatus string) error
	DeleteDownload(ctx context.Context, id int64) error
	RetryDownload(ctx context.Context, id int64) error
	GetDownloadHistory(ctx context.Context, page, pageSize int, filter HistoryFilter) ([]DownloadRecord, int, error)
	GetTotalDownloadedBytes(ctx context.Context) (int64, error)
	RecoverDownloadsOnStartup(ctx context.Context) ([]DownloadRecord, error)
	RequeueDownload(ctx context.Context, id int64) error
	SetDownloadPriority(ctx context.Context, id int64, priority int) error
	IncrementDownloadRetry(ctx context.Context, id int64) error
	UpdateDownloadMetadata(ctx context.Context, id int64, filename string, fileSize int64) error
	BulkActionDownloads(ctx context.Context, ids []int64, action string) (map[int64]string, error)
	FindDuplicateDownload(ctx context.Context, bot, serverAddress string, packNumber int) (*DownloadRecord, error)
	GetDownloadByBotMessage(ctx context.Context, bot, packMessage string) (*DownloadRecord, error)
	FilenamesExist(ctx context.Context, filenames []string) (map[string]bool, error)
	UpdateChannelAvgSpeed(ctx context.Context, serverAddress, channelName string, lastSpeedBPS float64) error
	DeleteAllHistory(ctx context.Context) (int64, error)
	GetHistoricalAvgSpeed(ctx context.Context) (float64, error)
}

// SearchCacheStore covers search result caching in SQLite.
type SearchCacheStore interface {
	SetSearchCache(ctx context.Context, entry SearchCacheEntry) error
	GetSearchCache(ctx context.Context, queryKey, provider string) (*SearchCacheEntry, error)
	GetSearchCacheByQuery(ctx context.Context, queryKey string) ([]SearchCacheEntry, error)
	DeleteExpiredSearchCache(ctx context.Context, staleBefore time.Time) error
}

// SearchPresetStore covers saved search presets.
type SearchPresetStore interface {
	AddSearchPreset(ctx context.Context, p SearchPreset) (int64, error)
	GetSearchPreset(ctx context.Context, id int64) (*SearchPreset, error)
	ListSearchPresets(ctx context.Context) ([]SearchPreset, error)
	UpdateSearchPreset(ctx context.Context, p SearchPreset) error
	DeleteSearchPreset(ctx context.Context, id int64) error
	SetDefaultSearchPreset(ctx context.Context, id int64) error
}

// WatchlistStore covers saved watchlists for periodic search and notification.
type WatchlistStore interface {
	AddWatchlist(ctx context.Context, w Watchlist) (int64, error)
	GetWatchlist(ctx context.Context, id int64) (*Watchlist, error)
	ListWatchlists(ctx context.Context) ([]Watchlist, error)
	UpdateWatchlist(ctx context.Context, w Watchlist) error
	DeleteWatchlist(ctx context.Context, id int64) error
	SetWatchlistChecked(ctx context.Context, id int64, fingerprint string, resultsJSON string) error
	SetWatchlistNotified(ctx context.Context, id int64) error
	GetEnabledWatchlists(ctx context.Context) ([]Watchlist, error)
}

// ProviderStatsStore covers search provider metrics.
type ProviderStatsStore interface {
	RecordProviderStats(ctx context.Context, s ProviderStats) error
	GetProviderStats(ctx context.Context, provider string, since time.Time) ([]ProviderStats, error)
	GetAllProviderStats(ctx context.Context, since time.Time) (map[string][]ProviderStats, error)
}

// ---------------------------------------------------------------------------
// Composite Store — embeds all focused interfaces for convenience and
// backward compatibility. New code should prefer one of the focused interfaces.
// ---------------------------------------------------------------------------

// Store defines the full interface for all persistence operations.
type Store interface {
	ServerStore
	DownloadStore
	SearchCacheStore
	SearchPresetStore
	WatchlistStore
	ProviderStatsStore

	// ---- Lifecycle ----
	Close() error
	Migrate(ctx context.Context) error
	CurrentSchemaVersion(ctx context.Context) (int, error)

	// ---- Cleanup ----
	CleanupOldDownloads(ctx context.Context, retentionDays int) (int, error)
	RunCleanup(ctx context.Context, retentionDays int, cleanupInterval time.Duration) (stopCh chan struct{}, doneCh chan struct{}, err error)
	Vacuum(ctx context.Context) error

	// ---- Backup / Export / Import ----
	ExportData(ctx context.Context) (*ExportData, error)
	ImportData(ctx context.Context, data *ExportData) error
	BackupDatabase(ctx context.Context, destPath string) error
}

// ---------------------------------------------------------------------------
// ExportData — used for export/import of config + state
// ---------------------------------------------------------------------------

// ExportData holds a snapshot of database state for export/import.
type ExportData struct {
	SchemaVersion int              `json:"schema_version"`
	ExportedAt    time.Time        `json:"exported_at"`
	Servers       []ServerRecord   `json:"servers,omitempty"`
	Channels      []ChannelRecord  `json:"channels,omitempty"`
	Downloads     []DownloadRecord `json:"downloads,omitempty"`
	SearchPresets []SearchPreset   `json:"search_presets,omitempty"`
	Watchlists    []Watchlist      `json:"watchlists,omitempty"`
}
