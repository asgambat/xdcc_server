package searchagg

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"xdcc_server/internal/config"
	"xdcc_server/internal/entities"
	"xdcc_server/internal/logging"
	"xdcc_server/internal/metrics"
	srch "xdcc_server/internal/search"
	"xdcc_server/internal/store"
)

// ---------------------------------------------------------------------------
// Aggregator
// ---------------------------------------------------------------------------

const (
	// statsChBuffer is the capacity of the asynchronous provider-stats channel.
	statsChBuffer = 200
	// statsFlushInterval controls how often buffered stats are flushed to DB.
	statsFlushInterval = 5 * time.Second
	// statsBatchSize triggers an early flush when this many records accumulate.
	statsBatchSize = 50
)

// Aggregator runs parallel searches across multiple XDCC providers, caches
// results, applies filters, and returns paginated, deduplicated results.
type Aggregator struct {
	store          store.Store
	cfg            *config.SearchConfig
	log            *logging.Logger
	cache          *searchCache
	disabled       map[string]bool // runtime-disabled providers
	runtimeEnabled map[string]bool // runtime-enabled providers (overrides config allowlist)
	mu             sync.RWMutex
	metrics        *metrics.Collector // optional runtime metrics collector

	// Cleanup goroutine lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	// Asynchronous provider-stats writer. Each search result enqueues a stat
	// record into statsCh; a background goroutine flushes them periodically
	// to SQLite, removing DB write latency from the search hot path.
	statsCh        chan store.ProviderStats
	statsFlushDone chan struct{}

	// onWatchlistResults is called when a watchlist finds new packs.
	// Arguments: watchlist name, new packs count, enqueued count.
	onWatchlistResults func(name string, newPacks, enqueued int)

	// watchlistInFlight tracks which watchlist IDs are currently being
	// executed, preventing concurrent runs of the same watchlist.
	watchlistInFlight sync.Map
}

// New creates a new search Aggregator.
func New(st store.Store, cfg *config.SearchConfig, logger *logging.Logger) *Aggregator {
	return &Aggregator{
		store:          st,
		cfg:            cfg,
		log:            logger,
		cache:          newSearchCache(st, cfg.Cache.Enabled, time.Duration(cfg.Cache.FreshTTL), time.Duration(cfg.Cache.StaleTTL)),
		disabled:       make(map[string]bool),
		runtimeEnabled: make(map[string]bool),
		done:           make(chan struct{}),
	}
}

// SetMetrics attaches a metrics collector to the aggregator for provider
// timeout and request tracking.
func (a *Aggregator) SetMetrics(met *metrics.Collector) {
	a.metrics = met
}

// SetOnWatchlistResults sets a callback invoked when a watchlist finds new packs.
// The callback receives (watchlistName string, newPacksCount, enqueuedCount int).
func (a *Aggregator) SetOnWatchlistResults(fn func(name string, newPacks, enqueued int)) {
	a.onWatchlistResults = fn
}

// Start begins the cache cleanup, stats-flush, and watchlist scheduler goroutines.
func (a *Aggregator) Start(ctx context.Context) error {
	a.ctx, a.cancel = context.WithCancel(ctx)
	a.statsCh = make(chan store.ProviderStats, statsChBuffer)
	a.statsFlushDone = make(chan struct{})

	// Wire stats channel depth into metrics collector.
	if a.metrics != nil {
		a.metrics.SetStatsQueueDepthFn(func() int {
			return len(a.statsCh)
		})
	}

	go a.cleanupLoop()
	go a.statsFlushLoop()
	go a.watchlistSchedulerLoop()
	return nil
}

// Stop stops the cache cleanup and stats-flush goroutines.
func (a *Aggregator) Stop() {
	if a.cancel != nil {
		a.cancel()
		<-a.done           // wait for cleanupLoop
		close(a.statsCh)   // signal statsFlushLoop to drain and exit
		<-a.statsFlushDone // wait for final flush
	}
}

// cleanupLoop periodically removes stale cache entries.
func (a *Aggregator) cleanupLoop() {
	defer close(a.done)

	// Run cleanup every 6 hours
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.cleanupStaleEntries()
		}
	}
}

// watchlistSchedulerLoop periodically checks enabled watchlists and runs
// those whose interval has elapsed since their last check.
func (a *Aggregator) watchlistSchedulerLoop() {
	// Check every minute to see if any watchlist needs to run
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.runDueWatchlists()
		}
	}
}

// runDueWatchlists finds all enabled watchlists whose interval has elapsed
// since their last check, and runs them.
func (a *Aggregator) runDueWatchlists() {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	watchlists, err := a.store.GetEnabledWatchlists(ctx)
	if err != nil {
		a.log.Warnf("watchlist scheduler: failed to get enabled watchlists: %v", err)
		return
	}

	now := time.Now()
	for _, wl := range watchlists {
		interval := time.Duration(wl.IntervalMinutes) * time.Minute
		if interval < 5*time.Minute {
			interval = 5 * time.Minute // enforce minimum
		}

		// Determine if this watchlist is due to run
		lastChecked := wl.LastCheckedAt
		if lastChecked == nil {
			// Never run or first run — run now
			a.log.Infof("watchlist scheduler: running %q (first run or never run)", wl.Name)
			go a.runWatchlistSafely(wl)
			continue
		}

		elapsed := now.Sub(*lastChecked)
		if elapsed >= interval {
			a.log.Infof("watchlist scheduler: running %q (elapsed %v since last run, interval %v)",
				wl.Name, elapsed.Round(time.Second), interval.Round(time.Second))
			go a.runWatchlistSafely(wl)
		} else {
			a.log.Debugf("watchlist scheduler: %q not due (elapsed %v, interval %v)",
				wl.Name, elapsed.Round(time.Second), interval.Round(time.Second))
		}
	}
}

// runWatchlistSafely runs a watchlist and logs any errors, ensuring it doesn't
// crash the scheduler loop. It uses an in-flight tracker to prevent concurrent
// runs of the same watchlist.
func (a *Aggregator) runWatchlistSafely(wl store.Watchlist) {
	// Check and mark in-flight — skip if already running
	if _, loaded := a.watchlistInFlight.LoadOrStore(wl.ID, struct{}{}); loaded {
		a.log.Debugf("watchlist %q already in-flight, skipping concurrent run", wl.Name)
		return
	}
	defer a.watchlistInFlight.Delete(wl.ID)

	ctx := context.Background()
	result, err := a.RunWatchlist(ctx, wl)
	if err != nil {
		a.log.Warnf("watchlist %q failed: %v", wl.Name, err)
		return
	}
	if result.HasChanges {
		newPacks := len(result.NewPacks)
		enqueued := result.Enqueued
		a.log.Infof("watchlist %q: found %d new packs (enqueued %d)",
			wl.Name, newPacks, enqueued)

		// Notify via SSE about new enqueued downloads (respect notify_enabled).
		if enqueued > 0 && wl.AutoEnqueue && wl.NotifyEnabled {
			a.notifyWatchlistResults(wl.Name, result.NewPacks)
		}

		// External notification (webhook, etc.) is now handled in RunWatchlist to cover
		// both scheduler-triggered and API-triggered runs — do NOT call it here to avoid double invocation.
		// (Scheduler: runWatchlistSafely → RunWatchlist → callback. API: RunWatchlist → callback directly.)
	} else {
		a.log.Debugf("watchlist %q: no changes since last run", wl.Name)
	}
}

// notifyWatchlistResults sends an SSE event for watchlist new results.
// This is a placeholder — the actual SSE notification is handled by the
// pubsub/broadcaster system if connected.
func (a *Aggregator) notifyWatchlistResults(watchlistName string, packs []*entities.XDCCPack) {
	// This would typically emit a pubsub event that the SSE handler
	// picks up to notify connected web clients. For now, just log.
	a.log.Infof("watchlist %q: %d new packs found — notification sent",
		watchlistName, len(packs))
}

// cleanupStaleEntries removes cache entries beyond stale TTL.
func (a *Aggregator) cleanupStaleEntries() {
	now := time.Now()

	// Cleanup in-memory cache
	a.cache.mu.Lock()
	for queryKey, providers := range a.cache.entries {
		for provider, entry := range providers {
			if now.After(entry.StaleAt) {
				delete(providers, provider)
			}
		}
		if len(providers) == 0 {
			delete(a.cache.entries, queryKey)
		}
	}
	a.cache.mu.Unlock()

	// Cleanup SQLite cache if enabled
	if a.cache.enabled && a.cache.st != nil {
		// Type-assert to SQLiteStore to access CleanupSearchCache method
		if sqlStore, ok := a.store.(*store.SQLiteStore); ok {
			deleted, err := sqlStore.CleanupSearchCache(a.ctx)
			if err != nil {
				a.log.Warnf("cache cleanup failed: %v", err)
			} else if deleted > 0 {
				a.log.Infof("cache cleanup: removed %d stale entries", deleted)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Provider enable/disable at runtime
// ---------------------------------------------------------------------------

// IsProviderEnabled returns whether a provider is enabled.
func (a *Aggregator) IsProviderEnabled(name string) bool {
	name = strings.ToLower(name)
	a.mu.RLock()
	disabled := a.disabled[name]
	runtimeEnabled := a.runtimeEnabled[name]
	a.mu.RUnlock()
	if disabled {
		return false
	}
	// Runtime enable overrides the config allowlist
	if runtimeEnabled {
		return true
	}
	// If configured with an allow-list, check it
	if len(a.cfg.EnabledProviders) > 0 {
		for _, ep := range a.cfg.EnabledProviders {
			if strings.EqualFold(ep, name) {
				return true
			}
		}
		return false
	}
	// Also check it's a known provider
	return srch.EngineByName(name, false) != nil
}

// EnableProvider enables a provider at runtime.
func (a *Aggregator) EnableProvider(name string) {
	name = strings.ToLower(name)
	a.mu.Lock()
	delete(a.disabled, name)
	a.runtimeEnabled[name] = true
	a.mu.Unlock()
	a.log.Infof("search provider %q enabled", name)
}

// DisableProvider disables a provider at runtime.
func (a *Aggregator) DisableProvider(name string) {
	name = strings.ToLower(name)
	a.mu.Lock()
	delete(a.runtimeEnabled, name)
	a.disabled[name] = true
	a.mu.Unlock()
	a.log.Infof("search provider %q disabled", name)
}

// GetProviderStates returns the current state of all known providers.
func (a *Aggregator) GetProviderStates(ctx context.Context) []ProviderStatus {
	engines := srch.AvailableEngines()
	result := make([]ProviderStatus, 0, len(engines))
	for _, name := range engines {
		ps := ProviderStatus{
			Name:   name,
			Status: ProviderStatusOK,
		}
		if !a.IsProviderEnabled(name) {
			a.mu.RLock()
			ps.Status = ProviderStatusDisabled
			if a.disabled[name] {
				ps.Error = "disabled at runtime"
			} else {
				ps.Error = "disabled in config"
			}
			a.mu.RUnlock()
			result = append(result, ps)
			continue
		}
		// Get stats from store
		if a.store != nil {
			stats, err := a.store.GetProviderStats(ctx, name, time.Now().Add(-24*time.Hour))
			if err == nil && len(stats) > 0 {
				latest := stats[0]
				ps.LatencyMs = int64(latest.AvgLatencyMs)
				if latest.Failures > latest.Successes {
					ps.Status = ProviderStatusFailed
					ps.Error = fmt.Sprintf("%d failures in last 24h", latest.Failures)
				}
			}
		}
		result = append(result, ps)
	}
	return result
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// Search performs an aggregated search across all enabled providers.
func (a *Aggregator) Search(ctx context.Context, opts SearchOptions) (*SearchResult, error) {
	a.log.Infof("[SEARCH] ====== Search request: query=%q page=%d pageSize=%d providers=%v ======",
		opts.Query, opts.Page, opts.PageSize, opts.Providers)

	// Normalise query
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		a.log.Infof("[SEARCH] Empty query — returning empty result")
		return &SearchResult{
			Packs:      []*entities.XDCCPack{},
			Providers:  a.GetProviderStates(ctx),
			Provenance: ProvenanceLive,
		}, nil
	}

	// Check cache first
	if a.cfg.Cache.Enabled {
		key := cacheKey(query)
		a.log.Infof("[SEARCH] Cache enabled, checking fresh cache for key=%q", key)
		freshEntries := a.cache.getFresh(ctx, key)
		if freshEntries != nil {
			providers := make([]string, 0, len(freshEntries))
			for p := range freshEntries {
				providers = append(providers, p)
			}
			a.log.Infof("[SEARCH] FRESH CACHE HIT for providers: %v — returning cached results", providers)
			// All providers have fresh cache — return combined results
			return a.buildResultFromCache(freshEntries, opts, ProvenanceCacheFresh, key), nil
		}
		a.log.Infof("[SEARCH] No fresh cache available — searching live")
	}

	// Run parallel searches
	allPacks, providerStatuses, hadSuccess := a.searchLive(ctx, query, opts.Providers)
	if !hadSuccess {
		a.log.Infof("[SEARCH] All providers failed — trying stale cache for key=%q", cacheKey(query))
		// All providers failed — try stale cache (all providers)
		key := cacheKey(query)
		staleEntries := a.cache.getStale(ctx, key)
		if staleEntries != nil {
			providers := make([]string, 0, len(staleEntries))
			for p := range staleEntries {
				providers = append(providers, p)
			}
			a.log.Infof("[SEARCH] STALE CACHE HIT for providers: %v — returning cached (possibly outdated) results", providers)
			return a.buildResultFromCache(staleEntries, opts, ProvenanceCacheStale, key), nil
		}

		a.log.Infof("[SEARCH] No stale cache available either — returning empty result with warnings")
		// No stale cache either
		return &SearchResult{
			Packs:      []*entities.XDCCPack{},
			Providers:  a.GetProviderStates(ctx),
			Provenance: ProvenanceLive,
			Warnings:   []string{"All providers failed and no cached data available"},
		}, nil
	}

	return a.buildResultFromLive(allPacks, opts, providerStatuses, query), nil
}

// searchLive runs parallel searches across all enabled providers,
// restricted to providers if non-empty.
// Returns the raw packs, per-provider statuses, and whether any succeeded.
func (a *Aggregator) searchLive(ctx context.Context, query string, providers []string) ([]*entities.XDCCPack, []ProviderStatus, bool) {
	engines := srch.AvailableEngines()
	a.log.Infof("[SEARCH_LIVE] Available engines: %v", engines)

	// Log enabled/disabled state for each engine
	for _, eng := range engines {
		enabled := a.IsProviderEnabled(eng)
		a.log.Infof("[SEARCH_LIVE]   Engine %q: enabled=%v", eng, enabled)
	}

	timeout := time.Duration(a.cfg.ProviderTimeout) * time.Second
	if timeout < 1*time.Second {
		timeout = 5 * time.Second
	}
	a.log.Infof("[SEARCH_LIVE] Provider timeout=%v, maxConcurrent=%d", timeout, 3)

	// Build a set of requested providers (empty = all enabled)
	providerSet := make(map[string]bool, len(providers))
	hasProviderFilter := len(providers) > 0
	for _, p := range providers {
		providerSet[strings.ToLower(p)] = true
	}

	type engineResult struct {
		name    string
		packs   []*entities.XDCCPack
		err     error
		latency time.Duration
	}

	const maxConcurrentSearches = 3
	sem := make(chan struct{}, maxConcurrentSearches)

	results := make(chan engineResult, len(engines))
	var wg sync.WaitGroup

	for _, engName := range engines {
		if !a.IsProviderEnabled(engName) {
			continue
		}
		// If the user selected specific providers, only search those
		if hasProviderFilter && !providerSet[strings.ToLower(engName)] {
			continue
		}

		sem <- struct{}{} // acquire semaphore slot (blocks if maxConcurrentSearches are running)
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			defer func() { <-sem }() // release semaphore slot

			engine := srch.EngineByName(name, false)
			if engine == nil {
				results <- engineResult{name: name, err: fmt.Errorf("unknown engine %q", name)}
				return
			}

			// Check in-memory fresh cache first
			if a.cfg.Cache.Enabled {
				key := cacheKey(query)
				entry := a.cache.get(ctx, key, name)
				if entry != nil && entry.isFresh() {
					results <- engineResult{
						name:    name,
						packs:   entry.Packs,
						latency: 0,
					}
					return
				}
			}

			// Run with timeout using context for proper cancellation.
			// The engine.Search call respects context cancellation, so we call
			// it directly in this goroutine rather than spawning a second one.
			searchCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			start := time.Now()
			packs, err := engine.Search(searchCtx, query)
			latency := time.Since(start)

			if err != nil {
				results <- engineResult{name: name, err: err, latency: latency}
				return
			}

			// Cache results
			if a.cfg.Cache.Enabled {
				a.cache.set(ctx, cacheKey(query), name, packs)
			}
			results <- engineResult{name: name, packs: packs, latency: latency}
		}(engName)
	}

	wg.Wait()
	close(results)

	// Collect results
	var allPacks []*entities.XDCCPack
	var providerStatuses []ProviderStatus
	hadSuccess := false

	for r := range results {
		ps := ProviderStatus{
			Name:      r.name,
			LatencyMs: r.latency.Milliseconds(),
		}

		if r.err != nil {
			errStr := r.err.Error()
			isTimeout := strings.Contains(errStr, "timeout")
			if isTimeout {
				ps.Status = ProviderStatusTimeout
				a.log.Infof("[SEARCH_LIVE] Provider %q: TIMEOUT after %v — %v", r.name, r.latency, r.err)
				if a.metrics != nil {
					a.metrics.RecordProviderTimeout(r.name)
				}
			} else {
				ps.Status = ProviderStatusFailed
				a.log.Infof("[SEARCH_LIVE] Provider %q: FAILED after %v — %v", r.name, r.latency, r.err)
				// Non-timeout failures counter is separate from timeouts.
				if a.metrics != nil {
					a.metrics.RecordProviderFailure(r.name)
				}
			}
			ps.Error = errStr

			// Record failure in store
			a.recordProviderResult(r.name, false, r.latency)
		} else {
			ps.Status = ProviderStatusOK
			ps.ResultCount = len(r.packs)
			a.log.Infof("[SEARCH_LIVE] Provider %q: SUCCESS — %d packs in %v", r.name, len(r.packs), r.latency)
			allPacks = append(allPacks, r.packs...)
			hadSuccess = true

			// Record success in store
			a.recordProviderResult(r.name, true, r.latency)
		}

		if a.metrics != nil {
			a.metrics.RecordProviderRequest(r.name)
		}

		providerStatuses = append(providerStatuses, ps)
	}

	a.log.Infof("[SEARCH_LIVE] Collected: %d total packs across all providers, hadSuccess=%v", len(allPacks), hadSuccess)
	return allPacks, providerStatuses, hadSuccess
}

// buildResultFromLive creates a SearchResult from live data with filtering.
func (a *Aggregator) buildResultFromLive(
	packs []*entities.XDCCPack,
	opts SearchOptions,
	providerStatuses []ProviderStatus,
	query string,
) *SearchResult {
	// Apply user filters
	filtered := filterPacks(packs, opts)
	sortPacks(filtered, query)
	paged, total := paginatePacks(filtered, opts.Page, opts.PageSize)

	totalPages := (total + opts.PageSize - 1) / opts.PageSize
	if opts.PageSize < 1 {
		opts.PageSize = 50
		opts.Page = 1
	}

	return &SearchResult{
		Packs:      paged,
		Total:      total,
		Page:       opts.Page,
		PageSize:   opts.PageSize,
		TotalPages: totalPages,
		Provenance: ProvenanceLive,
		Providers:  providerStatuses,
	}
}

// buildResultFromCache creates a SearchResult from cached data.
func (a *Aggregator) buildResultFromCache(
	entries map[string]*cacheEntry,
	opts SearchOptions,
	provenance string,
	queryKey string,
) *SearchResult {
	// Build provider filter set (same as searchLive)
	providerSet := make(map[string]bool, len(opts.Providers))
	hasProviderFilter := len(opts.Providers) > 0
	for _, p := range opts.Providers {
		providerSet[strings.ToLower(p)] = true
	}

	var allPacks []*entities.XDCCPack
	var providerStatuses []ProviderStatus
	var cacheAge time.Duration

	for provider, entry := range entries {
		// If user selected specific providers, skip non-selected ones
		if hasProviderFilter && !providerSet[strings.ToLower(provider)] {
			continue
		}
		allPacks = append(allPacks, entry.Packs...)
		age := time.Since(entry.FetchedAt)
		if age > cacheAge {
			cacheAge = age
		}

		ps := ProviderStatus{
			Name:        provider,
			Status:      ProviderStatusSkippedCache,
			ResultCount: len(entry.Packs),
		}
		providerStatuses = append(providerStatuses, ps)
	}

	// Apply filters
	filtered := filterPacks(allPacks, opts)
	sortPacks(filtered, opts.Query)
	paged, total := paginatePacks(filtered, opts.Page, opts.PageSize)

	totalPages := (total + opts.PageSize - 1) / opts.PageSize
	if opts.PageSize < 1 {
		opts.PageSize = 50
	}

	warnings := []string{}
	if provenance == ProvenanceCacheFresh {
		warnings = append(warnings, "Results from fresh cache")
	}

	return &SearchResult{
		Packs:      paged,
		Total:      total,
		Page:       opts.Page,
		PageSize:   opts.PageSize,
		TotalPages: totalPages,
		Provenance: provenance,
		Providers:  providerStatuses,
		CacheAge:   &cacheAge,
		Warnings:   warnings,
	}
}

// ---------------------------------------------------------------------------
// Provider stats recording (asynchronous, batched)
// ---------------------------------------------------------------------------

// recordProviderResult enqueues a provider stat for batch writing.
// The actual DB write happens in a background goroutine, so this method
// returns immediately and does not add latency to the search hot path.
func (a *Aggregator) recordProviderResult(name string, success bool, latency time.Duration) {
	if a.store == nil {
		return
	}
	now := time.Now()
	windowStart := now.Truncate(1 * time.Hour)
	windowEnd := windowStart.Add(1 * time.Hour)

	stats := store.ProviderStats{
		Provider:     name,
		WindowStart:  windowStart,
		WindowEnd:    windowEnd,
		Requests:     1,
		AvgLatencyMs: float64(latency.Milliseconds()),
		UpdatedAt:    now,
	}
	if success {
		stats.Successes = 1
	} else {
		stats.Failures = 1
	}

	// Non-blocking send; drop the stat if the buffer is full to avoid
	// blocking the search path during a burst of results.
	select {
	case a.statsCh <- stats:
	default:
		a.log.Debugf("dropping provider stat for %s (channel full)", name)
	}
}

// statsFlushLoop reads stats from the channel and writes them to SQLite
// in batches. It flushes periodically or when a batch threshold is reached.
func (a *Aggregator) statsFlushLoop() {
	defer close(a.statsFlushDone)

	ticker := time.NewTicker(statsFlushInterval)
	defer ticker.Stop()

	var pending []store.ProviderStats

	// flush writes pending stats to the store using the given context.
	// During normal operation ctx is a.ctx; during the final drain after
	// a.ctx.Done() it must be context.Background() to avoid silently
	// dropping stats when the context is already cancelled.
	flush := func(ctx context.Context) {
		if len(pending) == 0 {
			return
		}
		for _, s := range pending {
			_ = a.store.RecordProviderStats(ctx, s)
		}
		pending = pending[:0]
	}

	for {
		select {
		case <-a.ctx.Done():
			// Drain all remaining stats before exiting.
			// Use context.Background() because a.ctx is already cancelled.
			for s := range a.statsCh {
				pending = append(pending, s)
			}
			flush(context.Background())
			return

		case <-ticker.C:
			flush(a.ctx)

		case s, ok := <-a.statsCh:
			if !ok {
				flush(a.ctx)
				return
			}
			pending = append(pending, s)
			if len(pending) >= statsBatchSize {
				flush(a.ctx)
			}
		}
	}
}
