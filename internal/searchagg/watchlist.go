package searchagg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"xdcc_server/internal/entities"
	"xdcc_server/internal/store"
)

// ---------------------------------------------------------------------------
// WatchlistRunResult
// ---------------------------------------------------------------------------

// WatchlistRunResult holds the outcome of executing a watchlist.
type WatchlistRunResult struct {
	WatchlistID   int64  `json:"watchlist_id"`
	WatchlistName string `json:"watchlist_name"`

	// AllPacks are all packs found by the watchlist search.
	AllPacks []*entities.XDCCPack `json:"all_packs"`

	// NewPacks are packs not present in the previous run.
	NewPacks []*entities.XDCCPack `json:"new_packs"`

	// Enqueued counts how many new packs were auto-enqueued.
	Enqueued int `json:"enqueued"`

	// PreviousFingerprint is the fingerprint from the last check.
	PreviousFingerprint string `json:"previous_fingerprint"`

	// NewFingerprint is the fingerprint computed from this run.
	NewFingerprint string `json:"new_fingerprint"`

	// HasChanges is true when new packs were found.
	HasChanges bool `json:"has_changes"`
}

// ---------------------------------------------------------------------------
// RunWatchlist
// ---------------------------------------------------------------------------

// RunWatchlist executes a single watchlist: performs the search, compares
// results with the previous run's fingerprint, and optionally auto-enqueues
// new matches.
func (a *Aggregator) RunWatchlist(ctx context.Context, w store.Watchlist) (*WatchlistRunResult, error) {
	a.log.Infof("[WATCHLIST_RUN] ====== Starting run for watchlist %q (id=%d) ======", w.Name, w.ID)
	a.log.Infof("[WATCHLIST_RUN] Watchlist config: query=%q, interval=%dm, enabled=%v, auto_enqueue=%v",
		w.Query, w.IntervalMinutes, w.Enabled, w.AutoEnqueue)
	a.log.Infof("[WATCHLIST_RUN] Previous fingerprint: %q", w.LastMatchFingerprint)

	opts := SearchOptions{
		Query:    w.Query,
		PageSize: 500, // get many results for fingerprint comparison
	}

	// Parse filters from saved filters_json (providers, min_size, max_size)
	f := ParseFilters(w.FiltersJSON)
	opts.Providers = f.Providers
	opts.MinSize = f.MinSize
	opts.MaxSize = f.MaxSize

	a.log.Infof("[WATCHLIST_RUN] Applied filters: providers=%v min_size=%q max_size=%q",
		f.Providers, f.MinSize, f.MaxSize)
	a.log.Infof("[WATCHLIST_RUN] Calling Aggregator.Search with PageSize=%d", opts.PageSize)
	result, err := a.Search(ctx, opts)
	if err != nil {
		a.log.Infof("[WATCHLIST_RUN] Search FAILED: %v", err)
		return nil, fmt.Errorf("watchlist search failed: %w", err)
	}

	a.log.Infof("[WATCHLIST_RUN] Search returned %d packs total (provenance: %s)", len(result.Packs), result.Provenance)
	for i, p := range result.Packs {
		a.log.Infof("[WATCHLIST_RUN]   Pack %d: bot=%q filename=%q size=%d pack#=%d",
			i+1, p.Bot, p.Filename, p.Size, p.PackNumber)
	}

	// Log provider statuses
	for _, ps := range result.Providers {
		a.log.Infof("[WATCHLIST_RUN] Provider %q: status=%s results=%d latency=%dms err=%q",
			ps.Name, ps.Status, ps.ResultCount, ps.LatencyMs, ps.Error)
	}

	// Build fingerprint from all results
	fingerprint := computeFingerprint(result.Packs)
	prevFingerprint := w.LastMatchFingerprint
	a.log.Infof("[WATCHLIST_RUN] Fingerprint: previous=%q new=%q", prevFingerprint, fingerprint)

	wr := &WatchlistRunResult{
		WatchlistID:         w.ID,
		WatchlistName:       w.Name,
		AllPacks:            result.Packs,
		PreviousFingerprint: prevFingerprint,
		NewFingerprint:      fingerprint,
	}

	// Determine new packs
	if prevFingerprint == "" {
		// First run — no previous data to compare, but still filter against download history
		wr.HasChanges = true
		wr.NewPacks = filterNewPacks(ctx, a.store, result.Packs)
		a.log.Infof("[WATCHLIST_RUN] First run — %d packs after deduplication against download history", len(wr.NewPacks))
	} else if fingerprint != prevFingerprint {
		wr.HasChanges = true
		a.log.Infof("[WATCHLIST_RUN] Fingerprint changed (was %q → %q) — deduplicating against download history (%d packs)",
			prevFingerprint, fingerprint, len(result.Packs))
		// Filter packs against download history to find truly new ones
		wr.NewPacks = filterNewPacks(ctx, a.store, result.Packs)
	} else {
		a.log.Infof("[WATCHLIST_RUN] Fingerprint unchanged (%q) — no new packs, HasChanges stays false", fingerprint)
	}

	a.log.Infof("[WATCHLIST_RUN] HasChanges=%v | NewPacks count=%d | AllPacks count=%d",
		wr.HasChanges, len(wr.NewPacks), len(wr.AllPacks))

	// Auto-enqueue new packs
	if wr.HasChanges && w.AutoEnqueue && len(wr.NewPacks) > 0 {
		a.log.Infof("[WATCHLIST_RUN] Auto-enqueue enabled, enqueueing %d new packs", len(wr.NewPacks))
		enqueued, err := a.enqueueNewPacks(ctx, wr.NewPacks)
		if err != nil {
			a.log.Warnf("watchlist %d: auto-enqueue error: %v", w.ID, err)
		}
		wr.Enqueued = enqueued
		a.log.Infof("[WATCHLIST_RUN] Auto-enqueued %d packs", enqueued)
	} else {
		a.log.Infof("[WATCHLIST_RUN] Auto-enqueue skipped: hasChanges=%v autoEnqueue=%v newPacks=%d",
			wr.HasChanges, w.AutoEnqueue, len(wr.NewPacks))
	}

	// Persist last run results as JSON — only when there are new packs.
	// Passing an empty string preserves the previous results, preventing
	// the UI from losing information when the fingerprint is unchanged.
	var resultsJSON string
	if len(wr.NewPacks) > 0 {
		resultsJSON = serializeWatchlistResults(wr.NewPacks)
		a.log.Infof("[WATCHLIST_RUN] Serialized %d new packs to results JSON (%d bytes)", len(wr.NewPacks), len(resultsJSON))
	} else {
		a.log.Infof("[WATCHLIST_RUN] No new packs — preserving previous results JSON")
	}

	// Update the watchlist in the store
	a.log.Infof("[WATCHLIST_RUN] Updating watchlist store: SetWatchlistChecked(id=%d, fingerprint=%q)", w.ID, fingerprint)
	_ = a.store.SetWatchlistChecked(ctx, w.ID, fingerprint, resultsJSON)
	if wr.HasChanges && !w.AutoEnqueue {
		// Mark as needing notification
		a.log.Infof("[WATCHLIST_RUN] Marking watchlist as needing notification")
		_ = a.store.SetWatchlistNotified(ctx, w.ID)
	}

	a.log.Infof("[WATCHLIST_RUN] ====== Run complete for %q — %d new packs ======", w.Name, len(wr.NewPacks))

	// Fire external notification callback (ntfy/pushover/webhook) — called for both
	// scheduler-triggered and API-triggered runs, so we handle it here in RunWatchlist
	// rather than in runWatchlistSafely to avoid double-calling.
	if a.onWatchlistResults != nil && wr.HasChanges && len(wr.NewPacks) > 0 {
		a.onWatchlistResults(w.Name, len(wr.NewPacks), wr.Enqueued)
	}

	return wr, nil
}

// RunAllWatchlists executes all enabled watchlists.
func (a *Aggregator) RunAllWatchlists(ctx context.Context) ([]*WatchlistRunResult, error) {
	watchlists, err := a.store.GetEnabledWatchlists(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting enabled watchlists: %w", err)
	}

	var results []*WatchlistRunResult
	for _, w := range watchlists {
		r, err := a.RunWatchlist(ctx, w)
		if err != nil {
			a.log.Warnf("watchlist %d (%s) failed: %v", w.ID, w.Name, err)
			continue
		}
		results = append(results, r)
	}

	return results, nil
}

// ---------------------------------------------------------------------------
// Fingerprinting
// ---------------------------------------------------------------------------

// computeFingerprint creates a hash of the entire result set for change detection.
// The fingerprint uses (bot, pack_number, filename, server_address) for each pack.
func computeFingerprint(packs []*entities.XDCCPack) string {
	if len(packs) == 0 {
		return ""
	}

	// Create a stable, sorted representation
	entries := make([]string, 0, len(packs))
	for _, p := range packs {
		entries = append(entries, fmt.Sprintf("%s|%d|%s|%s",
			strings.ToLower(p.Bot),
			p.PackNumber,
			strings.ToLower(p.Filename),
			strings.ToLower(p.Server.Address),
		))
	}
	sort.Strings(entries)

	h := sha256.New()
	for _, e := range entries {
		h.Write([]byte(e))
		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil))
}

// ---------------------------------------------------------------------------
// Watchlist result persistence
// ---------------------------------------------------------------------------

// WatchlistResultItem is a lightweight representation of a watchlist result pack
// for storage and display. It contains only the fields needed to display results
// and enqueue downloads.
type WatchlistResultItem struct {
	Bot           string `json:"bot"`
	PackNumber    int    `json:"pack_number"`
	PackMessage   string `json:"pack_message"`
	ServerAddress string `json:"server_address"`
	Channel       string `json:"channel,omitempty"`
	Filename      string `json:"filename"`
	Size          int64  `json:"size"`
}

// serializeWatchlistResults converts packs to a JSON string for persistence.
// Returns "[]" if there are no packs.
func serializeWatchlistResults(packs []*entities.XDCCPack) string {
	if len(packs) == 0 {
		return "[]"
	}
	items := make([]WatchlistResultItem, 0, len(packs))
	for _, p := range packs {
		item := WatchlistResultItem{
			Bot:           p.Bot,
			PackNumber:    p.PackNumber,
			PackMessage:   fmt.Sprintf("xdcc send #%d", p.PackNumber),
			ServerAddress: p.Server.Address,
			Channel:       p.Channel,
			Filename:      p.Filename,
			Size:          p.Size,
		}
		items = append(items, item)
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// filterNewPacks filters packs by deduplicating against already-downloaded (or in-progress) filenames.
// It queries the store for filenames that either exist in download history (completed/failed/skipped)
// or are currently in the queue (queued/downloading/paused). Only packs with filenames NOT
// found in any of those states are returned as "new".
func filterNewPacks(ctx context.Context, st store.DownloadStore, packs []*entities.XDCCPack) []*entities.XDCCPack {
	if len(packs) == 0 {
		return nil
	}

	// Collect unique filenames
	filenames := make([]string, 0, len(packs))
	seen := make(map[string]struct{}, len(packs))
	for _, p := range packs {
		fn := strings.ToLower(p.Filename)
		if _, ok := seen[fn]; !ok {
			seen[fn] = struct{}{}
			filenames = append(filenames, fn)
		}
	}

	// Check which filenames already exist in the store
	existing, err := st.FilenamesExist(ctx, filenames)
	if err != nil {
		// If the DB query fails, fall back to returning all packs (safe default)
		return packs
	}

	// Filter: keep packs whose filename is NOT already downloaded/in-queue
	var newPacks []*entities.XDCCPack
	for _, p := range packs {
		if !existing[strings.ToLower(p.Filename)] {
			newPacks = append(newPacks, p)
		}
	}

	return newPacks
}

// ---------------------------------------------------------------------------
// Auto-enqueue
// ---------------------------------------------------------------------------

// enqueueNewPacks automatically enqueues packs from a watchlist result.
func (a *Aggregator) enqueueNewPacks(ctx context.Context, packs []*entities.XDCCPack) (int, error) {
	enqueued := 0
	for _, p := range packs {
		// Build a pack message
		packMsg := fmt.Sprintf("xdcc send #%d", p.PackNumber)

		// Determine channel from the server (use a default for now)
		channel := "#xdcc"
		if p.Server.Address != "" {
			channel = "#xdcc" // Most XDCC channels are #xdcc
		}

		// Create download record
		d := store.DownloadRecord{
			PackMessage:   packMsg,
			Bot:           p.Bot,
			ServerAddress: p.Server.Address,
			Channel:       channel,
			Filename:      p.Filename,
			FileSize:      p.Size,
			Priority:      100,
		}

		_, err := a.store.EnqueueDownload(ctx, d)
		if err != nil {
			// Skip duplicates and other errors
			continue
		}
		enqueued++
	}
	return enqueued, nil
}
