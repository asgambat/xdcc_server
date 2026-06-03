package pubsub

import (
	"sync"
	"testing"
)

// ===========================================================================
// Race/concurrency tests for Hub
// ===========================================================================

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
			//nolint:revive // channel drain
			for range c {
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
				//nolint:revive // channel drain
				for range c {
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
		//nolint:revive // channel drain
		for range ch {
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
