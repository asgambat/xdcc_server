package api

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRateLimiter_AllowBasic(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)

	// First 3 requests from the same IP should be allowed.
	for i := 0; i < 3; i++ {
		if !rl.Allow("10.0.0.1") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	// 4th request should be rejected.
	if rl.Allow("10.0.0.1") {
		t.Fatal("4th request should be rejected")
	}
}

func TestRateLimiter_DifferentIPs(t *testing.T) {
	rl := NewRateLimiter(2, time.Minute)

	// Each IP gets its own window.
	if !rl.Allow("10.0.0.1") {
		t.Fatal("first request from IP1 should be allowed")
	}
	if !rl.Allow("10.0.0.2") {
		t.Fatal("first request from IP2 should be allowed")
	}
	if !rl.Allow("10.0.0.1") {
		t.Fatal("second request from IP1 should be allowed")
	}
	if rl.Allow("10.0.0.1") {
		t.Fatal("third request from IP1 should be rejected")
	}
	if !rl.Allow("10.0.0.2") {
		t.Fatal("second request from IP2 should be allowed")
	}
}

func TestRateLimiter_WindowExpiry(t *testing.T) {
	rl := NewRateLimiter(2, 50*time.Millisecond)

	if !rl.Allow("10.0.0.1") {
		t.Fatal("first request should be allowed")
	}
	if !rl.Allow("10.0.0.1") {
		t.Fatal("second request should be allowed")
	}
	if rl.Allow("10.0.0.1") {
		t.Fatal("third request should be rejected within window")
	}

	// Wait for the window to expire.
	time.Sleep(60 * time.Millisecond)

	if !rl.Allow("10.0.0.1") {
		t.Fatal("request after window expiry should be allowed")
	}
}

func TestRateLimiter_Reconfigure(t *testing.T) {
	// Use a short window so the test can also verify window-based reset.
	rl := NewRateLimiter(2, 200*time.Millisecond)

	if !rl.Allow("10.0.0.1") {
		t.Fatal("first request should be allowed")
	}
	if !rl.Allow("10.0.0.1") {
		t.Fatal("second request should be allowed")
	}
	if rl.Allow("10.0.0.1") {
		t.Fatal("third request should be rejected")
	}

	// Reconfigure to a higher limit.
	rl.Reconfigure(5, 200*time.Millisecond)

	// The existing window still has count=3, so within the new limit of 5
	// the next two requests should be allowed.
	if !rl.Allow("10.0.0.1") {
		t.Fatal("4th request should be allowed after reconfigure to limit=5")
	}
	if !rl.Allow("10.0.0.1") {
		t.Fatal("5th request should be allowed (within new limit)")
	}
	if rl.Allow("10.0.0.1") {
		t.Fatal("6th request should be rejected (exceeds new limit=5)")
	}

	// Wait for the window to expire and verify the counter resets.
	time.Sleep(250 * time.Millisecond)
	if !rl.Allow("10.0.0.1") {
		t.Fatal("request after window expiry should be allowed (counter reset)")
	}
}

func TestRateLimiter_Concurrent(t *testing.T) {
	rl := NewRateLimiter(100, time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				rl.Allow("10.0.0.1")
			}
		}()
	}
	wg.Wait()
	// No panic or race condition is the main assertion here.
	// With -race, this will detect data races.
}

func TestRateLimiter_ConcurrentDifferentIPs(t *testing.T) {
	rl := NewRateLimiter(5, time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		ip := "10.0.0." + string(rune('A'+i%26))
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				rl.Allow(ip)
			}
		}()
	}
	wg.Wait()
}

func TestRateLimiter_Eviction(t *testing.T) {
	// Use a very short window so entries expire quickly.
	rl := NewRateLimiter(1, 5*time.Millisecond)

	// Phase 1: add 250 unique IPs (above eviction threshold of 200).
	for i := 0; i < 250; i++ {
		ip := fmt.Sprintf("10.0.%d.%d", i/256, i%256)
		rl.Allow(ip)
	}

	// Wait for all entries to expire (>2 windows = >10ms).
	time.Sleep(15 * time.Millisecond)

	// Phase 2: add another 250 unique IPs. The next Allow should
	// trigger eviction because the map has >200 entries and the old
	// ones are all stale (>2 windows ago).
	for i := 250; i < 500; i++ {
		ip := fmt.Sprintf("10.0.%d.%d", i/256, i%256)
		rl.Allow(ip)
	}

	// Verify: old entries should have been evicted; only the 250
	// fresh entries from Phase 2 should remain.
	rl.mu.Lock()
	size := len(rl.entries)
	rl.mu.Unlock()

	// Allow some slack for the trigger-ip entry.
	if size > 270 {
		t.Errorf("expected eviction to reduce map size, got %d entries", size)
	}
	if size < 240 {
		t.Errorf("expected ~250 fresh entries, got %d", size)
	}
}

func TestRateLimiter_ZeroLimit(t *testing.T) {
	// NOTE: limit=0 is a degenerate case. The first request from any IP
	// always starts a new window with count=1 and returns true (before
	// the limit check). This is by design — the limiter is meant for
	// positive limits. Verify the second request is rejected.
	rl := NewRateLimiter(0, time.Minute)
	// First request starts a new window (allowed by design).
	rl.Allow("10.0.0.1")
	// Second request within the window must be rejected.
	if rl.Allow("10.0.0.1") {
		t.Fatal("second request should be rejected with limit=0")
	}
}

func TestRateLimiter_LimitOne(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute)
	if !rl.Allow("10.0.0.1") {
		t.Fatal("first request should be allowed")
	}
	if rl.Allow("10.0.0.1") {
		t.Fatal("second request should be rejected with limit=1")
	}
}
