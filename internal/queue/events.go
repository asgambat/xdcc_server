// Package queue manages the download queue for the xdcc-server.
// It enforces the rule: max 1 active download per IRC channel, parallel across
// channels, with a configurable global parallel limit.
package queue

import (
	"time"
)

// ---------------------------------------------------------------------------
// Queue event types
// ---------------------------------------------------------------------------

// EventType categorizes a queue state change event.
type EventType string

const (
	EventDownloadQueued      EventType = "download_queued"
	EventDownloadStarted     EventType = "download_started"
	EventDownloadProgress    EventType = "download_progress"
	EventDownloadCompleted   EventType = "download_completed"
	EventDownloadSkipped     EventType = "download_skipped"
	EventDownloadFailed      EventType = "download_failed"
	EventDownloadPaused      EventType = "download_paused"
	EventDownloadRemoved     EventType = "download_removed"
	EventDownloadBulkResult  EventType = "download_bulk_action_result"
	EventDownloadAlternative EventType = "download_alternative_found"

	// Download metadata (filename/size) discovered mid-download via bot notice or DCC
	EventDownloadMetadataUpdate EventType = "download_metadata_update"

	// Disk space events (Fase 9.2)
	EventDiskSpaceLow EventType = "disk_space_low"
	EventDiskSpaceOK  EventType = "disk_space_ok"
)

// Event holds details about a queue state change.
type Event struct {
	Type            EventType `json:"type"`
	DownloadID      int64     `json:"download_id"`
	Bot             string    `json:"bot,omitempty"`
	ServerAddress   string    `json:"server_address,omitempty"`
	Channel         string    `json:"channel,omitempty"`
	Filename        string    `json:"filename,omitempty"`
	ProgressBytes   int64     `json:"progress_bytes,omitempty"`
	FileSize        int64     `json:"file_size,omitempty"`
	SpeedBPS        float64   `json:"speed_bps,omitempty"`
	ErrorMessage    string    `json:"error_message,omitempty"`
	AlternativeDesc string    `json:"alternative_desc,omitempty"`
	Timestamp       time.Time `json:"timestamp"`
	EventID         int64     `json:"event_id,omitempty"` // monotonic ID for SSE Last-Event-ID
}
