package api

import (
	"sync"
	"time"
)

// =========================================================================
// RateLimiter — per-IP fixed-window counter
// =========================================================================

// RateLimiter tracks request counts per client IP within a fixed-duration
// window and rejects requests that exceed the configured limit. Each IP's
// window resets to now+window on its first request after the window expiry.
// This is a simple approach that can briefly allow up to 2x limit at window
// boundaries — acceptable for anti-flood (5 requests / 60s).
//
// Entries are lazily evicted on each Allow() call to keep memory bounded.
type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*windowEntry
	limit   int           // max requests allowed per window
	window  time.Duration // window duration
}

// windowEntry records the request count for a single IP within the current
// window epoch.
type windowEntry struct {
	count   int
	resetAt time.Time
}

// NewRateLimiter creates a rate limiter that allows up to `limit` requests
// within a `window` per unique client IP.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		entries: make(map[string]*windowEntry),
		limit:   limit,
		window:  window,
	}
}

// Allow checks whether a request from the given IP is within the rate limit.
// It returns true if the request should be allowed, false if it should be
// rejected. Stale entries are cleaned up opportunistically to prevent
// unbounded memory growth under heavy traffic from many distinct IPs.
func (rl *RateLimiter) Allow(ip string) bool {
	now := time.Now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Lazy eviction: sweep entries that expired more than 2 windows ago.
	// This bounds the map and avoids a dedicated cleanup goroutine.
	// Threshold of 200 keeps memory bounded even under moderate traffic.
	if len(rl.entries) > 200 {
		cutoff := now.Add(-2 * rl.window)
		for k, e := range rl.entries {
			if e.resetAt.Before(cutoff) {
				delete(rl.entries, k)
			}
		}
	}

	entry, exists := rl.entries[ip]
	if !exists || now.After(entry.resetAt) {
		// First request or window has expired — start a new window.
		rl.entries[ip] = &windowEntry{
			count:   1,
			resetAt: now.Add(rl.window),
		}
		return true
	}

	// Within the current window.
	entry.count++
	return entry.count <= rl.limit
}

// Reconfigure updates the rate limit parameters at runtime. Existing
// entries are preserved but new requests will use the updated limit
// and window duration from the next window boundary.
func (rl *RateLimiter) Reconfigure(limit int, window time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.limit = limit
	rl.window = window
}
