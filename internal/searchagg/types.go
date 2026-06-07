// Package searchagg aggregates XDCC pack searches across multiple providers,
// with caching, filtering, pagination, and provenance metadata.
package searchagg

import (
	"encoding/json"
	"time"

	"xdcc_server/internal/entities"
)

// ---------------------------------------------------------------------------
// SearchOptions
// ---------------------------------------------------------------------------

// SearchOptions holds parameters for an aggregated search request.
type SearchOptions struct {
	Query     string   `json:"query"`
	Prefix    string   `json:"prefix,omitempty"`     // -p: filename must start with this
	Bot       string   `json:"bot,omitempty"`        // -b: bot name substring filter
	Ext       []string `json:"ext,omitempty"`        // -x: allowed extensions
	Compact   bool     `json:"compact,omitempty"`    // -c: deduplicate by bot family
	VideoOnly bool     `json:"video_only,omitempty"` // keep only video file extensions
	AudioOnly bool     `json:"audio_only,omitempty"` // keep only audio file extensions
	BooksOnly bool     `json:"books_only,omitempty"` // keep only book file extensions
	ZipOnly   bool     `json:"zip_only,omitempty"`   // keep only zip/archive file extensions
	Providers []string `json:"providers,omitempty"`  // restrict search to these providers
	MinSize   string   `json:"min_size,omitempty"`   // minimum file size filter (e.g. "100MB")
	MaxSize   string   `json:"max_size,omitempty"`   // maximum file size filter (e.g. "4GB")
	Page      int      `json:"page"`                 // 1-based
	PageSize  int      `json:"page_size"`            // items per page
}

// ---------------------------------------------------------------------------
// SearchResult
// ---------------------------------------------------------------------------

// SearchResult is the aggregated response from a search request.
type SearchResult struct {
	Packs      []*entities.XDCCPack `json:"packs"`
	Total      int                  `json:"total"`
	Page       int                  `json:"page"`
	PageSize   int                  `json:"page_size"`
	TotalPages int                  `json:"total_pages"`

	// Provenance indicates the data source: "live", "cache_fresh", "cache_stale"
	Provenance string `json:"provenance"`

	// Providers carries per-provider status information.
	Providers []ProviderStatus `json:"providers"`

	// CacheAge holds the age of the cached data when served from cache.
	CacheAge *time.Duration `json:"cache_age,omitempty"`

	// Warnings carries non-fatal messages (e.g. partial results).
	Warnings []string `json:"warnings,omitempty"`
}

// ---------------------------------------------------------------------------
// ProviderStatus
// ---------------------------------------------------------------------------

// ProviderStatus summarizes the result of querying a single provider.
type ProviderStatus struct {
	Name        string `json:"name"`
	Status      string `json:"status"` // ok | timeout | failed | disabled | skipped_cache_hit
	LatencyMs   int64  `json:"latency_ms,omitempty"`
	ResultCount int    `json:"result_count,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Provider status constants.
const (
	ProviderStatusOK           = "ok"
	ProviderStatusTimeout      = "timeout"
	ProviderStatusFailed       = "failed"
	ProviderStatusDisabled     = "disabled"
	ProviderStatusSkippedCache = "skipped_cache_hit"
)

// ---------------------------------------------------------------------------
// FilterOptions — serializable filter configuration for presets/watchlists
// ---------------------------------------------------------------------------

// FilterOptions holds user-configured filter criteria for presets and
// watchlists. It is serialized to JSON and stored in the filters_json column.
type FilterOptions struct {
	Providers []string `json:"providers,omitempty"`
	MinSize   string   `json:"min_size,omitempty"`
	MaxSize   string   `json:"max_size,omitempty"`
}

// SerializeFilters marshals FilterOptions to a JSON string for storage.
func SerializeFilters(f FilterOptions) string {
	b, err := json.Marshal(f)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ParseFilters unmarshals a JSON string into FilterOptions.
// Returns an empty FilterOptions on parse failure.
func ParseFilters(s string) FilterOptions {
	var f FilterOptions
	if s == "" || s == "{}" {
		return f
	}
	if err := json.Unmarshal([]byte(s), &f); err != nil {
		return FilterOptions{}
	}
	return f
}

// ---------------------------------------------------------------------------
// Provenance constants
// ---------------------------------------------------------------------------

const (
	ProvenanceLive       = "live"
	ProvenanceCacheFresh = "cache_fresh"
	ProvenanceCacheStale = "cache_stale"
)
