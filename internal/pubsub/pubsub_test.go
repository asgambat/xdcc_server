package pubsub

import (
	"sync"
	"testing"
)

// =========================================================================
// Unit tests — basic API
// =========================================================================

func TestNew(t *testing.T) {
	h := New[string](10)
	if h == nil {
		t.Fatal("expected non-nil hub")
	}
	if h.bufSize != 10 {
		t.Errorf("expected bufSize 10, got %d", h.bufSize)
	}
	if len(h.subscribers) != 0 {
		t.Errorf("expected 0 subscribers, got %d", len(h.subscribers))
	}
	if h.closed.Load() {
		t.Error("expected hub not closed initially")
	}
}

func TestNew_ZeroBuffer(t *testing.T) {
	h := New[int](0)
	if h == nil {
		t.Fatal("expected non-nil hub")
	}
	if h.bufSize != 0 {
		t.Errorf("expected bufSize 0, got %d", h.bufSize)
	}
}

func TestSubscribe(t *testing.T) {
	h := New[string](5)
	ch := h.Subscribe()
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}
	if cap(ch) != 5 {
		t.Errorf("expected channel cap 5, got %d", cap(ch))
	}

	h.mu.RLock()
	n := len(h.subscribers)
	h.mu.RUnlock()
	if n != 1 {
		t.Errorf("expected 1 subscriber, got %d", n)
	}
}

func TestSubscribeMultiple(t *testing.T) {
	h := New[int](10)
	ch1 := h.Subscribe()
	ch2 := h.Subscribe()
	ch3 := h.Subscribe()

	h.mu.RLock()
	n := len(h.subscribers)
	h.mu.RUnlock()
	if n != 3 {
		t.Errorf("expected 3 subscribers, got %d", n)
	}

	// Drain channels
	go drain(ch1)
	go drain(ch2)
	go drain(ch3)
	h.Close()
}

func TestUnsubscribe(t *testing.T) {
	h := New[string](10)
	ch := h.Subscribe()
	go drain(ch)

	h.Unsubscribe(ch)

	// Channel should be closed.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after Unsubscribe")
	}

	// Subscriber list should be empty.
	h.mu.RLock()
	n := len(h.subscribers)
	h.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected 0 subscribers, got %d", n)
	}
}

func TestUnsubscribeNonExistent(t *testing.T) {
	h := New[string](10)
	ch := make(chan string, 1)
	// Should not panic when unsubscribing a non-existent channel.
	h.Unsubscribe(ch)
	// Channel should NOT be closed (it wasn't ours).
	select {
	case ch <- "test":
	default:
		t.Error("channel should not be closed by Unsubscribe of non-existent")
	}
}

func TestUnsubscribeOnlyRemovesTarget(t *testing.T) {
	h := New[string](10)
	ch1 := h.Subscribe()
	ch2 := h.Subscribe()
	ch3 := h.Subscribe()

	h.Unsubscribe(ch2)

	// ch2 should be closed.
	_, ok := <-ch2
	if ok {
		t.Error("ch2 should be closed")
	}

	// ch1 and ch3 should still be open and in the list.
	h.mu.RLock()
	n := len(h.subscribers)
	h.mu.RUnlock()
	if n != 2 {
		t.Errorf("expected 2 remaining subscribers, got %d", n)
	}

	go drain(ch1)
	go drain(ch3)
	h.Close()
}

func TestClose(t *testing.T) {
	h := New[string](10)
	ch := h.Subscribe()

	h.Close()

	if !h.closed.Load() {
		t.Error("expected hub to be marked closed")
	}

	// Channel should be closed.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed")
	}

	// Subscriber list should be emptied.
	h.mu.RLock()
	n := len(h.subscribers)
	h.mu.RUnlock()
	if n != 0 {
		t.Errorf("expected 0 subscribers after close, got %d", n)
	}
}

func TestCloseAllSubscribers(t *testing.T) {
	h := New[int](10)
	ch1 := h.Subscribe()
	ch2 := h.Subscribe()
	ch3 := h.Subscribe()

	h.Close()

	// All channels should be closed.
	for i, ch := range []chan int{ch1, ch2, ch3} {
		_, ok := <-ch
		if ok {
			t.Errorf("channel %d should be closed", i)
		}
	}
}

func TestCloseIdempotent(t *testing.T) {
	h := New[string](10)
	h.Close()
	h.Close() // should not panic
	h.Close()
}

func TestPublish(t *testing.T) {
	h := New[string](10)
	ch := h.Subscribe()

	h.Publish("hello")

	select {
	case msg := <-ch:
		if msg != "hello" {
			t.Errorf("expected 'hello', got %q", msg)
		}
	default:
		t.Fatal("expected message on subscriber channel")
	}

	h.Unsubscribe(ch)
}

func TestPublishMultipleEvents(t *testing.T) {
	h := New[int](5)
	ch := h.Subscribe()

	for i := 0; i < 3; i++ {
		h.Publish(i)
	}

	for i := 0; i < 3; i++ {
		select {
		case msg := <-ch:
			if msg != i {
				t.Errorf("expected %d, got %d", i, msg)
			}
		default:
			t.Fatalf("expected message %d on channel", i)
		}
	}

	go drain(ch)
	h.Close()
}

func TestPublishToMultipleSubscribers(t *testing.T) {
	h := New[string](10)
	ch1 := h.Subscribe()
	ch2 := h.Subscribe()
	ch3 := h.Subscribe()

	h.Publish("broadcast")

	for i, ch := range []chan string{ch1, ch2, ch3} {
		select {
		case msg := <-ch:
			if msg != "broadcast" {
				t.Errorf("subscriber %d: expected 'broadcast', got %q", i, msg)
			}
		default:
			t.Fatalf("subscriber %d: expected message", i)
		}
	}

	h.Close()
}

func TestPublishAfterClose(t *testing.T) {
	h := New[string](10)
	h.Close()
	// Should not panic.
	h.Publish("after close")
}

func TestPublishDropOnFull(t *testing.T) {
	h := New[string](1)
	ch := h.Subscribe()

	// Fill the buffer (capacity 1).
	h.Publish("first")

	// This should be dropped (non-blocking).
	h.Publish("second")

	// Only the first message is in the buffer.
	select {
	case msg := <-ch:
		if msg != "first" {
			t.Errorf("expected 'first', got %q", msg)
		}
	default:
		t.Fatal("expected first message")
	}

	// No second message should have been delivered.
	select {
	case <-ch:
		t.Error("second message should have been dropped")
	default:
		// OK: dropped as expected
	}

	h.Close()
}

func TestSubscribeAfterClose(t *testing.T) {
	h := New[string](10)
	h.Close()

	ch := h.Subscribe()
	// The channel is created but publishing to a closed hub is a no-op.
	// The subscriber channel should still be open (Close was called before
	// Subscribe, so the subscriber list in Close was empty).
	select {
	case ch <- "test":
		// Channel is still open.
	default:
		t.Error("subscriber channel should be open")
	}

	h.mu.RLock()
	n := len(h.subscribers)
	h.mu.RUnlock()
	if n != 1 {
		t.Errorf("expected 1 subscriber (added after close), got %d", n)
	}
}

// =========================================================================
// Race/concurrency tests for Hub
// =========================================================================

func TestHubConcurrentPublishSubscribe(t *testing.T) {
	h := New[string](10)

	// Multiple subscribers
	const numSubs = 20
	chs := make([]chan string, numSubs)
	for i := 0; i < numSubs; i++ {
		chs[i] = h.Subscribe()
	}

	// Publish from multiple goroutines
	var pubWg sync.WaitGroup
	for i := 0; i < 50; i++ {
		pubWg.Add(1)
		go func(n int) {
			defer pubWg.Done()
			h.Publish("event")
		}(i)
	}
	pubWg.Wait()

	// Consume all published events from subscribers
	var consumeWg sync.WaitGroup
	for _, ch := range chs {
		consumeWg.Add(1)
		go func(c <-chan string) {
			defer consumeWg.Done()
			for range c {
				continue
			}
		}(ch)
	}

	// Cleanup
	for _, ch := range chs {
		h.Unsubscribe(ch)
	}
	consumeWg.Wait()
	h.Close()
}

func TestHubConcurrentPublishClose(t *testing.T) {
	h := New[int](10)

	_ = h.Subscribe()
	_ = h.Subscribe()
	_ = h.Subscribe()

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			h.Publish(i)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			h.Publish(i)
		}
	}()

	go func() {
		defer wg.Done()
		h.Close()
	}()

	wg.Wait()
}

func TestHubConcurrentSubscribePublishUnsubscribe(t *testing.T) {
	h := New[string](10)

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := h.Subscribe()
			// Drain the channel in a separate goroutine to avoid blocking
			go func(c <-chan string) {
				for range c {
					continue
				}
			}(ch)
			for j := 0; j < 10; j++ {
				h.Publish("test")
			}
			h.Unsubscribe(ch)
		}()
	}
	wg.Wait()
	h.Close()
}

func TestHubConcurrentPublishUnsubscribe(t *testing.T) {
	h := New[string](5)

	ch := h.Subscribe()
	go func() {
		for range ch {
			continue
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			h.Publish("msg")
		}
	}()

	go func() {
		defer wg.Done()
		h.Unsubscribe(ch)
	}()

	wg.Wait()
	h.Close()
}

// =========================================================================
// Drain helper
// =========================================================================

func drain[T any](ch <-chan T) {
	for range ch {
		continue
	}
}
