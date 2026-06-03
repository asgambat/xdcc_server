// Package bridge forwards internal events (IRC manager, download queue) to the
// SSE hub for real-time delivery to web clients. Extracted from cmd/xdcc-server/main.go
// to keep the daemon entrypoint focused on orchestration.
package bridge

import (
	"context"
	"sync"
	"time"

	"xdcc-go/internal/ircmanager"
	"xdcc-go/internal/logging"
	"xdcc-go/internal/queue"
	"xdcc-go/internal/sse"
)

// ---------------------------------------------------------------------------
// EventBridge
// ---------------------------------------------------------------------------

// EventBridge wires internal event sources (IRC manager, download queue) to the
// SSE hub for real-time client push.
type EventBridge struct {
	sseHub *sse.Hub
	logger *logging.Logger
}

// New creates an EventBridge.
func New(sseHub *sse.Hub, logger *logging.Logger) *EventBridge {
	return &EventBridge{
		sseHub: sseHub,
		logger: logger,
	}
}

// Hub returns the underlying SSE hub (needed for the log broadcaster setup).
func (b *EventBridge) Hub() *sse.Hub { return b.sseHub }

// ---------------------------------------------------------------------------
// IRC event forwarding
// ---------------------------------------------------------------------------

// ForwardIRCEvents forwards ircmanager events to the SSE hub until the context
// is cancelled. On shutdown it drains remaining events with a short timeout.
// The caller is responsible for adding to the WaitGroup before calling.
func (b *EventBridge) ForwardIRCEvents(ctx context.Context, ch <-chan ircmanager.Event, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			drainIRCEvents(ch, b.sseHub)
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			b.sseHub.Publish(string(evt.Type), map[string]interface{}{
				"server_id":   evt.ServerID,
				"server_addr": evt.ServerAddr,
				"channel":     evt.Channel,
				"topic":       evt.Topic,
				"timestamp":   evt.Timestamp,
			})
		}
	}
}

// drainIRCEvents drains remaining IRC events during shutdown.
func drainIRCEvents(ch <-chan ircmanager.Event, sseHub *sse.Hub) {
	timeout := time.After(100 * time.Millisecond)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			sseHub.Publish(string(evt.Type), map[string]interface{}{
				"server_id":   evt.ServerID,
				"server_addr": evt.ServerAddr,
				"channel":     evt.Channel,
				"topic":       evt.Topic,
				"timestamp":   evt.Timestamp,
			})
		case <-timeout:
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Queue event forwarding
// ---------------------------------------------------------------------------

// ForwardQueueEvents forwards queue events to the SSE hub until the context is
// cancelled. On shutdown it drains remaining events with a short timeout.
// The caller is responsible for adding to the WaitGroup before calling.
func (b *EventBridge) ForwardQueueEvents(ctx context.Context, ch <-chan queue.Event, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			drainQueueEvents(ch, b.sseHub)
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			b.sseHub.Publish(string(evt.Type), map[string]interface{}{
				"download_id":    evt.DownloadID,
				"bot":            evt.Bot,
				"server_address": evt.ServerAddress,
				"channel":        evt.Channel,
				"filename":       evt.Filename,
				"progress_bytes": evt.ProgressBytes,
				"file_size":      evt.FileSize,
				"speed_bps":      evt.SpeedBPS,
				"error_message":  evt.ErrorMessage,
				"timestamp":      evt.Timestamp,
			})
		}
	}
}

// drainQueueEvents drains remaining queue events during shutdown.
func drainQueueEvents(ch <-chan queue.Event, sseHub *sse.Hub) {
	timeout := time.After(100 * time.Millisecond)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			sseHub.Publish(string(evt.Type), map[string]interface{}{
				"download_id":    evt.DownloadID,
				"bot":            evt.Bot,
				"server_address": evt.ServerAddress,
				"channel":        evt.Channel,
				"filename":       evt.Filename,
				"progress_bytes": evt.ProgressBytes,
				"file_size":      evt.FileSize,
				"speed_bps":      evt.SpeedBPS,
				"error_message":  evt.ErrorMessage,
				"timestamp":      evt.Timestamp,
			})
		case <-timeout:
			return
		}
	}
}
