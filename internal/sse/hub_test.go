package sse

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// NewHub
// ---------------------------------------------------------------------------

func TestNewHubDefaultBuffer(t *testing.T) {
	h := NewHub(0)
	if h == nil {
		t.Fatal("expected non-nil hub")
	}
	if h.bufferSize != 100 {
		t.Errorf("expected default buffer size 100, got %d", h.bufferSize)
	}
	if h.ClientCount() != 0 {
		t.Errorf("expected 0 clients, got %d", h.ClientCount())
	}
}

func TestNewHubCustomBuffer(t *testing.T) {
	h := NewHub(50)
	if h.bufferSize != 50 {
		t.Errorf("expected buffer size 50, got %d", h.bufferSize)
	}
}

// ---------------------------------------------------------------------------
// Subscribe / Unsubscribe / ClientCount
// ---------------------------------------------------------------------------

func TestSubscribeAndClientCount(t *testing.T) {
	h := NewHub(10)
	ch1 := h.Subscribe()
	if ch1 == nil {
		t.Fatal("expected non-nil channel")
	}
	if h.ClientCount() != 1 {
		t.Errorf("expected 1 client, got %d", h.ClientCount())
	}

	ch2 := h.Subscribe()
	if h.ClientCount() != 2 {
		t.Errorf("expected 2 clients, got %d", h.ClientCount())
	}

	// Drain channels to avoid blocking
	go drainChan(ch1)
	go drainChan(ch2)
}

func TestUnsubscribe(t *testing.T) {
	h := NewHub(10)
	ch := h.Subscribe()
	go drainChan(ch)

	if h.ClientCount() != 1 {
		t.Fatalf("expected 1 client before unsubscribe, got %d", h.ClientCount())
	}

	h.Unsubscribe(ch)
	if h.ClientCount() != 0 {
		t.Errorf("expected 0 clients after unsubscribe, got %d", h.ClientCount())
	}

	// Verify channel is closed
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after unsubscribe")
	}
}

func TestUnsubscribeNonExistent(t *testing.T) {
	h := NewHub(10)
	ch := make(chan Event, 1)
	// Should not panic
	h.Unsubscribe(ch)
}

// ---------------------------------------------------------------------------
// IsClosed / Close
// ---------------------------------------------------------------------------

func TestIsClosed(t *testing.T) {
	h := NewHub(10)
	if h.IsClosed() {
		t.Error("expected hub to be open initially")
	}

	h.Close()
	if !h.IsClosed() {
		t.Error("expected hub to be closed after Close()")
	}
}

func TestCloseClosesAllChannels(t *testing.T) {
	h := NewHub(10)
	ch1 := h.Subscribe()
	ch2 := h.Subscribe()
	go drainChan(ch1)
	go drainChan(ch2)

	h.Close()

	// Both channels should be closed
	_, ok1 := <-ch1
	if ok1 {
		t.Error("ch1 should be closed")
	}
	_, ok2 := <-ch2
	if ok2 {
		t.Error("ch2 should be closed")
	}
	if h.ClientCount() != 0 {
		t.Errorf("expected 0 clients after close, got %d", h.ClientCount())
	}
}

func TestSubscribeAfterClose(t *testing.T) {
	h := NewHub(10)
	h.Close()

	ch := h.Subscribe()
	// Channel should be closed immediately
	_, ok := <-ch
	if ok {
		t.Error("expected closed channel when subscribing to closed hub")
	}
}

func TestUnsubscribeAfterClose(t *testing.T) {
	h := NewHub(10)
	ch := h.Subscribe()
	go drainChan(ch)
	h.Close()

	// Should not panic: Unsubscribe on closed hub is a no-op
	h.Unsubscribe(ch)
}

// ---------------------------------------------------------------------------
// Publish
// ---------------------------------------------------------------------------

func TestPublishToClient(t *testing.T) {
	h := NewHub(10)
	ch := h.Subscribe()

	h.Publish("test_event", map[string]interface{}{"key": "value"})

	select {
	case evt := <-ch:
		if evt.Type != "test_event" {
			t.Errorf("expected event type 'test_event', got %q", evt.Type)
		}
		if evt.ID != 1 {
			t.Errorf("expected event ID 1, got %d", evt.ID)
		}
		if v, ok := evt.Payload["key"]; !ok || v != "value" {
			t.Errorf("expected payload key='value', got %v", evt.Payload)
		}
		if evt.Timestamp.IsZero() {
			t.Error("expected non-zero timestamp")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}

	h.Unsubscribe(ch)
}

func TestPublishToMultipleClients(t *testing.T) {
	h := NewHub(10)
	ch1 := h.Subscribe()
	ch2 := h.Subscribe()
	ch3 := h.Subscribe()

	h.Publish("broadcast", nil)

	for i, ch := range []chan Event{ch1, ch2, ch3} {
		select {
		case evt := <-ch:
			if evt.Type != "broadcast" {
				t.Errorf("client %d: expected 'broadcast', got %q", i, evt.Type)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("client %d: timeout waiting for broadcast", i)
		}
	}

	h.Unsubscribe(ch1)
	h.Unsubscribe(ch2)
	h.Unsubscribe(ch3)
}

func TestPublishOnClosedHub(t *testing.T) {
	h := NewHub(10)
	ch := h.Subscribe()
	go drainChan(ch)

	h.Close()

	// Should not panic
	h.Publish("after_close", nil)
}

func TestPublishSlowClientEviction(t *testing.T) {
	h := NewHub(10)
	// Create a channel with a small buffer (1) to simulate a slow client.
	// We override the default buffer by directly creating a channel.
	// Since Subscribe always creates 256-buffer channels, we test by
	// filling the buffer first.
	ch := h.Subscribe()

	// Fill the buffer (256 events)
	for i := 0; i < 256; i++ {
		h.Publish("fill", map[string]interface{}{"n": i})
	}

	// Now publish one more — the client channel is full, so this should
	// evict the slow client.
	if h.ClientCount() != 1 {
		t.Fatalf("expected 1 client before eviction, got %d", h.ClientCount())
	}
	h.Publish("overflow", nil)

	// Client should have been evicted
	if h.ClientCount() != 0 {
		t.Errorf("expected 0 clients after eviction, got %d", h.ClientCount())
	}

	// Verify channel was closed
	// Drain the buffer first to reach the close signal
drainLoop:
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				break drainLoop
			}
		default:
			break drainLoop
		}
	}
}

// ---------------------------------------------------------------------------
// EventsSince / LastEventID
// ---------------------------------------------------------------------------

func TestLastEventIDInitial(t *testing.T) {
	h := NewHub(10)
	if id := h.LastEventID(); id != 0 {
		t.Errorf("expected initial LastEventID=0, got %d", id)
	}
}

func TestLastEventIDAfterPublish(t *testing.T) {
	h := NewHub(10)
	ch := h.Subscribe()
	go drainChan(ch)

	h.Publish("e1", nil)
	h.Publish("e2", nil)
	h.Publish("e3", nil)

	if id := h.LastEventID(); id != 3 {
		t.Errorf("expected LastEventID=3, got %d", id)
	}
}

func TestEventsSinceEmptyBuffer(t *testing.T) {
	h := NewHub(10)
	events := h.EventsSince(0)
	if events != nil {
		t.Errorf("expected nil for empty buffer, got %v", events)
	}
}

func TestEventsSinceAllEvents(t *testing.T) {
	h := NewHub(10)
	ch := h.Subscribe()
	go drainChan(ch)

	h.Publish("e1", nil) // ID 1
	h.Publish("e2", nil) // ID 2
	h.Publish("e3", nil) // ID 3

	events := h.EventsSince(0) // ask for events since ID 0
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].ID != 1 || events[1].ID != 2 || events[2].ID != 3 {
		t.Errorf("unexpected event order: %v", eventIDs(events))
	}
}

func TestEventsSincePartial(t *testing.T) {
	h := NewHub(10)
	ch := h.Subscribe()
	go drainChan(ch)

	for i := 0; i < 5; i++ {
		h.Publish("event", nil)
	}

	// Ask for events since ID 2 (should get IDs 3, 4, 5)
	events := h.EventsSince(2)
	if len(events) != 3 {
		t.Fatalf("expected 3 events (since ID 2), got %d", len(events))
	}
	if events[0].ID != 3 {
		t.Errorf("expected first event ID=3, got %d", events[0].ID)
	}
	if events[2].ID != 5 {
		t.Errorf("expected last event ID=5, got %d", events[2].ID)
	}
}

func TestEventsSinceTooOld(t *testing.T) {
	h := NewHub(10) // buffer of 10

	// Publish 20 events to wrap around the buffer
	ch := h.Subscribe()
	go drainChan(ch)
	for i := 0; i < 20; i++ {
		h.Publish("event", nil)
	}

	// The oldest event in the buffer has ID 11 (20-10+1)
	// Asking for events since ID 5 should return nil (too old)
	events := h.EventsSince(5)
	if events != nil {
		t.Errorf("expected nil for too-old event ID, got %d events", len(events))
	}
}

func TestEventsSinceUpToDate(t *testing.T) {
	h := NewHub(10)
	ch := h.Subscribe()
	go drainChan(ch)

	h.Publish("e1", nil) // ID 1
	h.Publish("e2", nil) // ID 2

	// Ask for events since the last ID (ID 2) — should get none
	events := h.EventsSince(2)
	if events != nil {
		t.Errorf("expected nil for up-to-date client, got %d events", len(events))
	}
}

func TestEventsSinceBufferWrap(t *testing.T) {
	h := NewHub(5) // small buffer to test wrapping
	ch := h.Subscribe()
	go drainChan(ch)

	// Publish 10 events — buffer wraps
	for i := 0; i < 10; i++ {
		h.Publish("event", nil)
	}

	// Events 6-10 should be in buffer (5 events)
	// Asking since ID 5 should return events 6,7,8,9,10
	events := h.EventsSince(5)
	if len(events) != 5 {
		t.Fatalf("expected 5 events after buffer wrap, got %d", len(events))
	}
	for i, evt := range events {
		expectedID := int64(6 + i)
		if evt.ID != expectedID {
			t.Errorf("event %d: expected ID %d, got %d", i, expectedID, evt.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// Concurrent Publish + Subscribe/Unsubscribe (smoke test)
// ---------------------------------------------------------------------------

func TestConcurrentPublishSubscribe(t *testing.T) {
	h := NewHub(10)
	done := make(chan struct{})

	// Goroutine that publishes events
	go func() {
		for i := 0; i < 50; i++ {
			h.Publish("concurrent", map[string]interface{}{"n": i})
		}
		close(done)
	}()

	// Subscribe/unsubscribe while publishing is happening
	for i := 0; i < 10; i++ {
		ch := h.Subscribe()
		time.Sleep(1 * time.Millisecond)
		// Drain a few events
		select {
		case <-ch:
		default:
		}
		h.Unsubscribe(ch)
	}

	<-done
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func drainChan(ch chan Event) {
	for range ch {
		continue
	}
}

func eventIDs(events []Event) []int64 {
	ids := make([]int64, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	return ids
}
