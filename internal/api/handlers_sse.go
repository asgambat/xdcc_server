package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"xdcc-go/internal/sse"
)

// =========================================================================
// GET /api/events — SSE stream for real-time updates (Fase 7.1)
// =========================================================================

func (a *API) handleEvents(w http.ResponseWriter, r *http.Request) {
	// Safely get request ID from context
	reqID := "unknown"
	if id := r.Context().Value(requestIDKey); id != nil {
		if idStr, ok := id.(string); ok {
			reqID = idStr
		}
	}

	start := time.Now()

	// Log SSE client connection with current client count
	clientsBefore := a.SSEHub.ClientCount()
	a.Logger.Debugf("[SSE] client connected [%s] remote=%s clients_before=%d", reqID, r.RemoteAddr, clientsBefore)
	defer func() {
		duration := time.Since(start)
		clientsAfter := a.SSEHub.ClientCount()
		a.Logger.Debugf("[SSE] client disconnected [%s] duration=%v clients_after=%d", reqID, duration, clientsAfter)
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "SSE_UNSUPPORTED",
			"Streaming not supported")
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	// Subscribe to the SSE hub
	ch := a.SSEHub.Subscribe()
	defer a.SSEHub.Unsubscribe(ch)

	// Log after subscription
	clientsAfterSub := a.SSEHub.ClientCount()
	a.Logger.Debugf("[SSE] subscribed [%s] total_clients=%d", reqID, clientsAfterSub)

	// Handle Last-Event-ID reconnection (Fase 7.5)
	lastEventIDStr := r.Header.Get("Last-Event-ID")
	var lastEventID int64
	if lastEventIDStr != "" {
		_, _ = fmt.Sscanf(lastEventIDStr, "%d", &lastEventID)
	}

	// If Last-Event-ID is provided, replay missed events
	if lastEventID > 0 {
		missed := a.SSEHub.EventsSince(lastEventID)
		if missed == nil {
			// Event ID too old — send resync_required
			data, _ := json.Marshal(map[string]string{
				"message": "Event history too old, please reload state via API",
			})
			fmt.Fprintf(w, "event: resync_required\ndata: %s\n\n", data)
			flusher.Flush()
		} else {
			for _, evt := range missed {
				a.writeSSEEvent(w, evt)
				flusher.Flush()
			}
		}
	}

	// Replay only very recent log entries for new clients (last ~10 entries).
	// Sending too many historical logs on every SSE connection causes a "log flood"
	// because all those old entries are re-sent to every new client.
	if a.LogBroadcaster != nil {
		recentLogs := a.LogBroadcaster.RecentEntries(10)
		for _, entry := range recentLogs {
			logData, _ := json.Marshal(map[string]interface{}{
				"timestamp": entry.Timestamp,
				"level":     entry.Level,
				"message":   entry.Message,
			})
			fmt.Fprintf(w, "event: log_entry\ndata: %s\n\n", logData)
		}
		if len(recentLogs) > 0 {
			flusher.Flush()
		}
	}

	// Notify the client of successful connection
	connectedData, _ := json.Marshal(map[string]interface{}{
		"status":    "connected",
		"server_id": a.SSEHub.LastEventID(),
	})
	fmt.Fprintf(w, "event: connected\ndata: %s\n\n", connectedData)
	flusher.Flush()

	// Keepalive ticker (every 30 seconds)
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	// Main event loop
	a.Logger.Debugf("[SSE] entering main event loop [%s]", reqID)
	for {
		select {
		case <-r.Context().Done():
			// Client disconnected or server shutting down
			a.Logger.Debugf("[SSE] context canceled [%s]: %v", reqID, r.Context().Err())
			return
		case <-keepalive.C:
			// Send keepalive as a named event so the frontend can track
			// liveness (SSE comments don't fire JS listeners).
			a.Logger.Debugf("[SSE] sending keepalive [%s]", reqID)
			fmt.Fprintf(w, "event: keepalive\ndata: {}\n\n")
			flusher.Flush()
		case evt, ok := <-ch:
			if !ok {
				// Hub closed - channel was closed
				a.Logger.Debugf("[SSE] channel closed (hub shutdown) [%s]", reqID)
				return
			}
			// Use SseDebugLogger instead of a.Logger to avoid feedback loop.
			// SseDebugLogger is a separate logger instance that doesn't have
			// LogBroadcaster as a writer, so it won't trigger SSE events.
			if a.SseDebugLogger != nil {
				a.SseDebugLogger.Debugf("[SSE] sending event type=%s [%s]", evt.Type, reqID)
			}
			a.writeSSEEvent(w, evt)
			flusher.Flush()
		}
	}
}

// writeSSEEvent serializes an sse.Event to SSE format and writes it.
func (a *API) writeSSEEvent(w http.ResponseWriter, evt sse.Event) {
	data, err := json.Marshal(evt.Payload)
	if err != nil {
		a.Logger.Warnf("[SSE] json marshal error for event type=%s: %v", evt.Type, err)
		return
	}

	// Format: event: <type>\nid: <id>\ndata: <json>\n\n
	fmt.Fprintf(w, "event: %s\n", evt.Type)
	fmt.Fprintf(w, "id: %d\n", evt.ID)
	fmt.Fprintf(w, "data: %s\n\n", data)
}
