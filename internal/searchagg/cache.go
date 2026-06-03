package searchagg

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"xdcc-go/internal/entities"
	"xdcc-go/internal/store"
)

// ---------------------------------------------------------------------------
// Cache entry
// ---------------------------------------------------------------------------

// cacheEntry holds cached search results for a single query+provider.
type cacheEntry struct {
	Packs     []*entities.XDCCPack `json:"packs"`
	FetchedAt time.Time            `json:"fetched_at"`
	ExpiresAt time.Time            `json:"expires_at"`
	StaleAt   time.Time            `json:"stale_at"`
}

// isFresh returns true if the entry is within the fresh TTL.
func (e *cacheEntry) isFresh() bool {
	return time.Now().Before(e.ExpiresAt)
}

// isStale returns true if the entry is within the stale TTL.
func (e *cacheEntry) isStale() bool {
	return time.Now().Before(e.StaleAt)
}

// ---------------------------------------------------------------------------
// searchCache — in-memory cache with optional SQLite persistence
// ---------------------------------------------------------------------------

// searchCache provides a two-level cache: fast in-memory map with a SQLite
// persistence layer for surviving restarts.
type searchCache struct {
	mu       sync.RWMutex
	entries  map[string]map[string]*cacheEntry // queryKey → provider → entry
	st       store.SearchCacheStore            // optional SQLite persistence
	enabled  bool
	freshTTL time.Duration
	staleTTL time.Duration
}

// newSearchCache creates a new search cache.
func newSearchCache(st store.SearchCacheStore, enabled bool, freshTTL, staleTTL time.Duration) *searchCache {
	return &searchCache{
		entries:  make(map[string]map[string]*cacheEntry),
		st:       st,
		enabled:  enabled && st != nil,
		freshTTL: freshTTL,
		staleTTL: staleTTL,
	}
}

// cacheKey normalises a query string for use as a cache key.
// Uses strings.ToLower and strings.Fields (both rune-safe) instead of
// manual rune→byte iteration which would truncate non-ASCII characters.
func cacheKey(query string) string {
	parts := strings.Fields(strings.ToLower(query))
	return strings.Join(parts, " ")
}

// get retrieves a cached entry. Returns nil if not found or expired beyond stale.
func (c *searchCache) get(ctx context.Context, queryKey, provider string) *cacheEntry {
	c.mu.RLock()
	entry, ok := c.entries[queryKey][provider]
	c.mu.RUnlock()

	if ok && entry.isStale() {
		return entry
	}

	// Try SQLite persistence
	if c.enabled && c.st != nil {
		sqlEntry, err := c.st.GetSearchCache(ctx, queryKey, provider)
		if err == nil && sqlEntry != nil {
			var packs []*entities.XDCCPack
			if err := json.Unmarshal([]byte(sqlEntry.PayloadJSON), &packs); err == nil {
				entry = &cacheEntry{
					Packs:     packs,
					FetchedAt: sqlEntry.FetchedAt,
					ExpiresAt: sqlEntry.ExpiresAt,
					StaleAt:   sqlEntry.StaleExpiresAt,
				}
				if entry.isStale() {
					// Promote to in-memory
					c.mu.Lock()
					if c.entries[queryKey] == nil {
						c.entries[queryKey] = make(map[string]*cacheEntry)
					}
					c.entries[queryKey][provider] = entry
					c.mu.Unlock()
					return entry
				}
			}
		}
	}

	return nil
}

// set stores an entry in the cache.
func (c *searchCache) set(ctx context.Context, queryKey, provider string, packs []*entities.XDCCPack) {
	now := time.Now()
	entry := &cacheEntry{
		Packs:     packs,
		FetchedAt: now,
		ExpiresAt: now.Add(c.freshTTL),
		StaleAt:   now.Add(c.staleTTL),
	}

	c.mu.Lock()
	if c.entries[queryKey] == nil {
		c.entries[queryKey] = make(map[string]*cacheEntry)
	}
	c.entries[queryKey][provider] = entry
	c.mu.Unlock()

	// Persist to SQLite
	if c.enabled && c.st != nil {
		payload, err := json.Marshal(packs)
		if err != nil {
			return
		}
		_ = c.st.SetSearchCache(ctx, store.SearchCacheEntry{
			QueryKey:       queryKey,
			Provider:       provider,
			PayloadJSON:    string(payload),
			FetchedAt:      now,
			ExpiresAt:      entry.ExpiresAt,
			StaleExpiresAt: entry.StaleAt,
		})
	}
}

// getStale returns all entries for a query that are still within stale TTL.
// Used as fallback when all live providers fail.
func (c *searchCache) getStale(ctx context.Context, queryKey string) map[string]*cacheEntry {
	result := make(map[string]*cacheEntry)

	c.mu.RLock()
	for provider, entry := range c.entries[queryKey] {
		if entry.isStale() {
			result[provider] = entry
		}
	}
	c.mu.RUnlock()

	// Also check SQLite for stale entries not in memory
	if c.enabled && c.st != nil && len(result) == 0 {
		entries, err := c.st.GetSearchCacheByQuery(ctx, queryKey)
		if err == nil {
			for _, sqlEntry := range entries {
				if time.Now().Before(sqlEntry.StaleExpiresAt) {
					var packs []*entities.XDCCPack
					if err := json.Unmarshal([]byte(sqlEntry.PayloadJSON), &packs); err == nil {
						entry := &cacheEntry{
							Packs:     packs,
							FetchedAt: sqlEntry.FetchedAt,
							ExpiresAt: sqlEntry.ExpiresAt,
							StaleAt:   sqlEntry.StaleExpiresAt,
						}
						c.mu.Lock()
						if c.entries[queryKey] == nil {
							c.entries[queryKey] = make(map[string]*cacheEntry)
						}
						c.entries[queryKey][sqlEntry.Provider] = entry
						c.mu.Unlock()
						result[sqlEntry.Provider] = entry
					}
				}
			}
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// getFresh returns all entries for a query that are still fresh.
// Returns nil if no provider has fresh data.
func (c *searchCache) getFresh(ctx context.Context, queryKey string) map[string]*cacheEntry {
	result := make(map[string]*cacheEntry)

	c.mu.RLock()
	for provider, entry := range c.entries[queryKey] {
		if entry.isFresh() {
			result[provider] = entry
		}
	}
	c.mu.RUnlock()

	// Also check SQLite for fresh entries not in memory
	if c.enabled && c.st != nil && len(result) == 0 {
		// Use GetSearchCacheByQuery to fetch all entries in a single query,
		// avoiding nested queries that deadlock on single-connection SQLite.
		entries, err := c.st.GetSearchCacheByQuery(ctx, queryKey)
		if err == nil {
			for _, sqlEntry := range entries {
				if time.Now().Before(sqlEntry.ExpiresAt) {
					var packs []*entities.XDCCPack
					if err := json.Unmarshal([]byte(sqlEntry.PayloadJSON), &packs); err == nil {
						entry := &cacheEntry{
							Packs:     packs,
							FetchedAt: sqlEntry.FetchedAt,
							ExpiresAt: sqlEntry.ExpiresAt,
							StaleAt:   sqlEntry.StaleExpiresAt,
						}
						// Promote to memory
						c.mu.Lock()
						if c.entries[queryKey] == nil {
							c.entries[queryKey] = make(map[string]*cacheEntry)
						}
						c.entries[queryKey][sqlEntry.Provider] = entry
						c.mu.Unlock()
						if entry.isFresh() {
							result[sqlEntry.Provider] = entry
						}
					}
				}
			}
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}
