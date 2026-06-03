// Package sse implements a Server-Sent Events hub for real-time client updates.
// It provides a unified event stream that aggregates events from multiple sources
// (IRC manager, download queue manager) and broadcasts them to HTTP SSE clients.
package sse

import (
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Event — unified event for SSE broadcast
// ---------------------------------------------------------------------------

// Event is a universal event that can represent changes from any component.
type Event struct {
	// ID is a monotonic, increasing identifier for Last-Event-ID tracking.
	ID int64 `json:"id"`

	// Type identifies the event category (e.g. "server_connected",
	// "download_progress", "download_completed").
	Type string `json:"type"`

	// Payload carries the event-specific data as a map for JSON serialization.
	Payload map[string]interface{} `json:"payload,omitempty"`

	// Timestamp is when the event was created.
	Timestamp time.Time `json:"timestamp"`
}

// ---------------------------------------------------------------------------
// Hub
// ---------------------------------------------------------------------------

// Hub manages SSE client connections and broadcasts events to all connected
// clients. It maintains a rolling buffer of recent events to support the
// Last-Event-ID reconnection mechanism.
type Hub struct {
	mu sync.RWMutex

	// clients holds the set of connected SSE client channels (bidirectional).
	clients map[chan Event]struct{}

	// eventBuffer is a circular buffer of recent events for Last-Event-ID.
	eventBuffer []Event
	bufferSize  int
	bufferHead  int
	bufferCount int

	// nextID is the monotonic event ID counter.
	nextID int64

	// closed signals that the hub is shutting down.
	closed bool
}

// NewHub creates a new SSE Hub with the given buffer size (number of events
// to retain for Last-Event-ID reconnection).
func NewHub(bufferSize int) *Hub {
	if bufferSize < 1 {
		bufferSize = 100 // default
	}
	return &Hub{
		clients:     make(map[chan Event]struct{}),
		eventBuffer: make([]Event, bufferSize),
		bufferSize:  bufferSize,
	}
}

// ---------------------------------------------------------------------------
// Client management
// ---------------------------------------------------------------------------

// Subscribe adds a new client channel. The returned channel should be used
// by the HTTP handler to receive events for writing to the SSE stream.
// The channel has a buffer of 256 events to absorb temporary back-pressure.
func (h *Hub) Subscribe() chan Event {
	ch := make(chan Event, 256)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch
	}
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes a client channel and closes it.
// Safe to call even after hub.Close() - won't panic on closed channels.
func (h *Hub) Unsubscribe(ch chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// If hub is closed, channels are already closed - skip
	if h.closed {
		return
	}

	// Find and remove the channel from the map
	for c := range h.clients {
		if c == ch {
			delete(h.clients, c)
			close(c)
			break
		}
	}
}

// IsClosed returns true if the hub has been closed.
func (h *Hub) IsClosed() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.closed
}

// ClientCount returns the number of currently connected SSE clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Close shuts down the hub and all client connections.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	for ch := range h.clients {
		close(ch)
	}
	h.clients = nil
	h.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Event publishing
// ---------------------------------------------------------------------------

// Publish sends an event to all connected clients. Non-blocking: if a
// client's channel buffer is full, the client is evicted (channel closed)
// to force reconnection via Last-Event-ID, preventing silent state drift
// in the frontend.
//
// The entire broadcast executes under h.mu so that a concurrent Unsubscribe
// cannot close a channel while we are sending to it. Since every send is a
// non-blocking select, holding the lock for the duration of the broadcast
// is safe and eliminates the need for fragile recover() guards.
func (h *Hub) Publish(eventType string, payload map[string]interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return
	}

	// Assign event ID and store in buffer
	id := atomic.AddInt64(&h.nextID, 1)
	evt := Event{
		ID:        id,
		Type:      eventType,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	// Add to circular buffer
	h.eventBuffer[h.bufferHead] = evt
	h.bufferHead = (h.bufferHead + 1) % h.bufferSize
	if h.bufferCount < h.bufferSize {
		h.bufferCount++
	}

	// Broadcast to all clients while holding the lock — this prevents
	// a concurrent Unsubscribe from closing a channel mid-send.
	var dead []chan Event
	for ch := range h.clients {
		select {
		case ch <- evt:
		default:
			// Buffer full — client is too slow. Evict it to force
			// reconnection via Last-Event-ID.
			dead = append(dead, ch)
		}
	}

	// Evict slow clients.
	for _, ch := range dead {
		if _, ok := h.clients[ch]; ok {
			delete(h.clients, ch)
			close(ch)
		}
	}
}

// ---------------------------------------------------------------------------
// Last-Event-ID support
// ---------------------------------------------------------------------------

// EventsSince returns all buffered events with IDs greater than the given
// lastEventID. This supports SSE reconnection via Last-Event-ID.
// Returns nil if lastEventID is too old (no longer in buffer), indicating
// the client needs a full resync.
func (h *Hub) EventsSince(lastEventID int64) []Event {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.bufferCount == 0 {
		return nil
	}

	// Find the oldest ID in the buffer
	oldestID := h.nextID - int64(h.bufferCount)
	if lastEventID < oldestID {
		// Event ID too old — client needs full resync
		return nil
	}

	// Compute how many events are newer than lastEventID. Event IDs are
	// sequential, so we can calculate the count and starting offset directly
	// instead of iterating the entire buffer.
	// nextID is the next ID to assign, so the newest event has ID nextID-1.
	count := int(h.nextID - lastEventID - 1)
	if count <= 0 {
		return nil
	}
	if count > h.bufferCount {
		count = h.bufferCount
	}

	result := make([]Event, count)
	for i := 0; i < count; i++ {
		idx := (h.bufferHead - count + i + h.bufferSize) % h.bufferSize
		result[i] = h.eventBuffer[idx]
	}
	return result
}

// LastEventID returns the most recent event ID (for initial sync).
func (h *Hub) LastEventID() int64 {
	return atomic.LoadInt64(&h.nextID)
}
