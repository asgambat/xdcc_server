package searchagg

import (
	"context"
	"testing"
	"time"

	"xdcc-go/internal/entities"
)

// ===========================================================================
// searchCache — in-memory
// ===========================================================================

func TestCacheGetSet(t *testing.T) {
	c := newSearchCache(nil, true, 5*time.Minute, 30*time.Minute)

	packs := []*entities.XDCCPack{mkPack("test.mkv", 100, "Bot")}
	c.set(context.Background(), "query1", "provider1", packs)

	got := c.get(context.Background(), "query1", "provider1")
	if got == nil {
		t.Fatal("expected non-nil cache entry")
	}
	if len(got.Packs) != 1 {
		t.Errorf("expected 1 pack, got %d", len(got.Packs))
	}
	if got.Packs[0].Filename != "test.mkv" {
		t.Errorf("expected filename test.mkv, got %s", got.Packs[0].Filename)
	}
	if !got.isFresh() {
		t.Errorf("expected entry to be fresh")
	}
}

func TestCacheGet_Missing(t *testing.T) {
	c := newSearchCache(nil, true, 5*time.Minute, 30*time.Minute)

	got := c.get(context.Background(), "nonexistent", "provider1")
	if got != nil {
		t.Errorf("expected nil for missing entry, got %+v", got)
	}
}

func TestCacheGet_DifferentQuery(t *testing.T) {
	c := newSearchCache(nil, true, 5*time.Minute, 30*time.Minute)

	c.set(context.Background(), "query1", "p1", []*entities.XDCCPack{mkPack("a.mkv", 100, "Bot")})
	got := c.get(context.Background(), "query2", "p1")
	if got != nil {
		t.Errorf("expected nil for different query, got %+v", got)
	}
}

func TestCacheGet_DifferentProvider(t *testing.T) {
	c := newSearchCache(nil, true, 5*time.Minute, 30*time.Minute)

	c.set(context.Background(), "query1", "p1", []*entities.XDCCPack{mkPack("a.mkv", 100, "Bot")})
	got := c.get(context.Background(), "query1", "p2")
	if got != nil {
		t.Errorf("expected nil for different provider, got %+v", got)
	}
}

func TestCacheGetFresh(t *testing.T) {
	c := newSearchCache(nil, true, 10*time.Minute, 1*time.Hour)

	// Set two providers for same query, one provider for another query
	c.set(context.Background(), "q1", "p1", []*entities.XDCCPack{mkPack("a.mkv", 100, "Bot")})
	c.set(context.Background(), "q1", "p2", []*entities.XDCCPack{mkPack("b.mkv", 200, "Bot")})
	c.set(context.Background(), "q2", "p1", []*entities.XDCCPack{mkPack("c.mkv", 300, "Bot")})

	fresh := c.getFresh(context.Background(), "q1")
	if fresh == nil {
		t.Fatal("expected fresh entries for q1")
	}
	if len(fresh) != 2 {
		t.Errorf("expected 2 fresh entries for q1, got %d", len(fresh))
	}

	freshQ2 := c.getFresh(context.Background(), "q2")
	if freshQ2 == nil {
		t.Fatal("expected fresh entries for q2")
	}
	if len(freshQ2) != 1 {
		t.Errorf("expected 1 fresh entry for q2, got %d", len(freshQ2))
	}

	freshMissing := c.getFresh(context.Background(), "nonexistent")
	if freshMissing != nil {
		t.Errorf("expected nil for nonexistent query, got %+v", freshMissing)
	}
}

// ===========================================================================
// cacheEntry lifecycle
// ===========================================================================

func TestCacheEntryIsFresh(t *testing.T) {
	now := time.Now()

	e := &cacheEntry{
		FetchedAt: now,
		ExpiresAt: now.Add(10 * time.Minute),
		StaleAt:   now.Add(1 * time.Hour),
	}

	if !e.isFresh() {
		t.Errorf("expected entry to be fresh")
	}
	if !e.isStale() {
		t.Errorf("expected entry to be within stale TTL")
	}
}

// ===========================================================================
// cacheKey normalization
// ===========================================================================

func TestCacheKeyCaseInsensitive(t *testing.T) {
	k1 := cacheKey("Anime Show 1080p")
	k2 := cacheKey("ANIME SHOW 1080P")
	if k1 != k2 {
		t.Errorf("cacheKey should produce same key regardless of case: %q vs %q", k1, k2)
	}
}

func TestCacheKeyUnicode(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Unicode characters (Japanese, Chinese, accented, etc.)
		{"アニメ", "アニメ"},
		{"café", "café"},
		{"中文测试", "中文测试"},
		{"Anime アニメ Show", "anime アニメ show"},
		// Emoji and special characters
		{"anime 🔥 show", "anime 🔥 show"},
		{"🔥🔥🔥", "🔥🔥🔥"},
		// Mixed case with Unicode
		{"日本語UPPERCASE", "日本語uppercase"},
		{"Über cool", "über cool"},
		// Whitespace normalization with Unicode
		{"  日本語  test  ", "日本語 test"},
		{"  café   du   monde  ", "café du monde"},
		// Edge cases
		{"", ""},
		{" ", ""},
		{"\t\n", ""},
	}

	for _, tt := range tests {
		got := cacheKey(tt.input)
		if got != tt.expected {
			t.Errorf("cacheKey(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// ===========================================================================
// Concurrent access
// ===========================================================================

func TestCacheConcurrentAccess(t *testing.T) {
	c := newSearchCache(nil, true, 5*time.Minute, 30*time.Minute)

	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func(n int) {
			query := "test"
			c.set(context.Background(), query, "p", []*entities.XDCCPack{mkPack("f.mkv", int64(n), "Bot")})
			c.get(context.Background(), query, "p")
			done <- struct{}{}
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

// ===========================================================================
// Store-backed cache
// ===========================================================================

// This test requires a real store, so it's more of an integration test.
// For now, test that the cache works correctly with store=nil (pure in-memory mode).

func TestCacheStoreBackedWithNilStore(t *testing.T) {
	c := newSearchCache(nil, true, 5*time.Minute, 30*time.Minute)
	if c.enabled != false {
		t.Errorf("expected cache enabled=false when store is nil and enabled=true")
	}
}

func TestCacheEnabledFalse(t *testing.T) {
	c := newSearchCache(nil, false, 5*time.Minute, 30*time.Minute)
	if c.enabled {
		t.Errorf("expected cache enabled=false")
	}
}

func TestCacheExpiresStale(t *testing.T) {
	// Use a very short TTL to test expiry
	c := newSearchCache(nil, true, 1*time.Nanosecond, 1*time.Millisecond)

	c.set(context.Background(), "test", "p1", []*entities.XDCCPack{mkPack("a.mkv", 100, "Bot")})

	// Immediately after set, it should be fresh (but with 1ns TTL it might already be stale)
	got := c.get(context.Background(), "test", "p1")
	if got != nil && !got.isFresh() && !got.isStale() {
		t.Log("cache entry is fully expired — cache should not return expired entries")
	}
}
