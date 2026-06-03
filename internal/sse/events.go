// Package sse defines SSE event type constants used across the application.
package sse

// ---------------------------------------------------------------------------
// SSE Event Types (must match frontend api.js event listeners)
// ---------------------------------------------------------------------------

const (
	// Server status
	EventServerConnected    = "server_connected"
	EventServerDisconnected = "server_disconnected"
	EventServerReconnecting = "server_reconnecting"

	// Channel events
	EventChannelJoined       = "channel_joined"
	EventChannelLeft         = "channel_left"
	EventChannelTopicUpdated = "channel_topic_updated"

	// Download events
	EventDownloadQueued      = "download_queued"
	EventDownloadStarted     = "download_started"
	EventDownloadProgress    = "download_progress"
	EventDownloadCompleted   = "download_completed"
	EventDownloadSkipped     = "download_skipped"
	EventDownloadFailed      = "download_failed"
	EventDownloadPaused      = "download_paused"
	EventDownloadRemoved     = "download_removed"
	EventDownloadBulkResult  = "download_bulk_action_result"
	EventDownloadAlternative = "download_alternative_found"

	// Disk space events (Fase 9.2)
	EventDiskSpaceLow = "disk_space_low"
	EventDiskSpaceOK  = "disk_space_ok"

	// Watchlist events (Fase 9.8)
	EventWatchlistNewResults = "watchlist_new_results"

	// Provider health
	EventProviderHealthChanged = "provider_health_changed"

	// Log streaming (Fase 10.1)
	EventLogEntry = "log_entry"

	// Resync
	EventResyncRequired = "resync_required"
)
