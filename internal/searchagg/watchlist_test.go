package searchagg

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xdcc_server/internal/config"
	"xdcc_server/internal/entities"
	"xdcc_server/internal/logging"
	"xdcc_server/internal/store"
)

// ===========================================================================
// Mock store for testing filterNewPacks
// ===========================================================================

type mockDownloadStore struct {
	existingFilenames map[string]bool
}

func (m *mockDownloadStore) FilenamesExist(ctx context.Context, filenames []string) (map[string]bool, error) {
	result := make(map[string]bool, len(filenames))
	for _, fn := range filenames {
		if m.existingFilenames[fn] {
			result[fn] = true
		} else {
			result[fn] = false
		}
	}
	return result, nil
}

// Stub methods to satisfy the DownloadStore interface
func (m *mockDownloadStore) EnqueueDownload(ctx context.Context, d store.DownloadRecord) (int64, error) {
	return 1, nil
}
func (m *mockDownloadStore) GetDownload(ctx context.Context, id int64) (*store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockDownloadStore) GetQueue(ctx context.Context) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockDownloadStore) GetQueueByChannel(ctx context.Context, channel string) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockDownloadStore) GetActiveDownloads(ctx context.Context) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockDownloadStore) GetPendingByChannel(ctx context.Context, channel string) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockDownloadStore) UpdateDownloadProgress(ctx context.Context, id int64, progressBytes int64, speedBPS int64) error {
	return nil
}
func (m *mockDownloadStore) MarkDownloadStarted(ctx context.Context, id int64) error { return nil }
func (m *mockDownloadStore) MarkDownloadCompleted(ctx context.Context, id int64, filename string, fileSize int64) error {
	return nil
}
func (m *mockDownloadStore) MarkDownloadFailed(ctx context.Context, id int64, errMsg string) error {
	return nil
}
func (m *mockDownloadStore) MarkDownloadSkipped(ctx context.Context, id int64) error { return nil }
func (m *mockDownloadStore) MarkDownloadPaused(ctx context.Context, id int64) error  { return nil }
func (m *mockDownloadStore) MarkDownloadRetry(ctx context.Context, id int64, newStatus string) error {
	return nil
}
func (m *mockDownloadStore) DeleteDownload(ctx context.Context, id int64) error { return nil }
func (m *mockDownloadStore) RetryDownload(ctx context.Context, id int64) error  { return nil }
func (m *mockDownloadStore) GetDownloadHistory(ctx context.Context, _, _ int, _ store.HistoryFilter) ([]store.DownloadRecord, int, error) {
	return nil, 0, nil
}
func (m *mockDownloadStore) GetTotalDownloadedBytes(ctx context.Context) (int64, error) {
	return 0, nil
}
func (m *mockDownloadStore) RecoverDownloadsOnStartup(ctx context.Context) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockDownloadStore) RequeueDownload(ctx context.Context, id int64) error { return nil }
func (m *mockDownloadStore) SetDownloadPriority(ctx context.Context, id int64, priority int) error {
	return nil
}
func (m *mockDownloadStore) IncrementDownloadRetry(ctx context.Context, id int64) error {
	return nil
}
func (m *mockDownloadStore) UpdateDownloadMetadata(ctx context.Context, id int64, filename string, fileSize int64) error {
	return nil
}

func (m *mockDownloadStore) UpdateChannelAvgSpeed(ctx context.Context, serverAddress, channelName string, lastSpeedBPS float64) error {
	return nil
}
func (m *mockDownloadStore) BulkActionDownloads(ctx context.Context, ids []int64, action string) (map[int64]string, error) {
	return nil, nil
}
func (m *mockDownloadStore) FindDuplicateDownload(ctx context.Context, bot, serverAddress string, packNumber int) (*store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockDownloadStore) GetDownloadByBotMessage(ctx context.Context, bot, packMessage string) (*store.DownloadRecord, error) {
	return nil, nil
}

// ===========================================================================
// computeFingerprint
// ===========================================================================

func TestComputeFingerprint_Empty(t *testing.T) {
	fp := computeFingerprint(nil)
	if fp != "" {
		t.Errorf("expected empty fingerprint for nil packs, got %q", fp)
	}

	fp = computeFingerprint([]*entities.XDCCPack{})
	if fp != "" {
		t.Errorf("expected empty fingerprint for empty packs, got %q", fp)
	}
}

func TestComputeFingerprint_Deterministic(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPackWithBot("file.mkv", 1000, "Bot", 1),
		mkPackWithBot("other.mkv", 2000, "OtherBot", 2),
	}

	fp1 := computeFingerprint(packs)
	fp2 := computeFingerprint(packs)
	if fp1 != fp2 {
		t.Errorf("fingerprint should be deterministic: %q != %q", fp1, fp2)
	}
	if fp1 == "" {
		t.Errorf("expected non-empty fingerprint")
	}
}

func TestComputeFingerprint_DifferentPacks(t *testing.T) {
	packs1 := []*entities.XDCCPack{
		mkPackWithBot("file.mkv", 1000, "Bot", 1),
	}
	packs2 := []*entities.XDCCPack{
		mkPackWithBot("different.mkv", 2000, "Bot", 2),
	}

	fp1 := computeFingerprint(packs1)
	fp2 := computeFingerprint(packs2)
	if fp1 == fp2 {
		t.Errorf("different packs should produce different fingerprints")
	}
}

func TestComputeFingerprint_OrderIndependent(t *testing.T) {
	packs1 := []*entities.XDCCPack{
		mkPackWithBot("a.mkv", 100, "Bot1", 1),
		mkPackWithBot("b.mkv", 200, "Bot2", 2),
	}
	packs2 := []*entities.XDCCPack{
		mkPackWithBot("b.mkv", 200, "Bot2", 2),
		mkPackWithBot("a.mkv", 100, "Bot1", 1),
	}

	fp1 := computeFingerprint(packs1)
	fp2 := computeFingerprint(packs2)
	if fp1 != fp2 {
		t.Errorf("fingerprint should be order-independent: %q != %q", fp1, fp2)
	}
}

func TestComputeFingerprint_MultiplePacks(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPackWithBot("a.mkv", 100, "Bot1", 1),
		mkPackWithBot("b.mkv", 200, "Bot2", 2),
		mkPackWithBot("c.mkv", 300, "Bot3", 3),
	}

	fp := computeFingerprint(packs)
	if fp == "" {
		t.Errorf("expected non-empty fingerprint for multiple packs")
	}
	if len(fp) != 64 { // SHA-256 hex is 64 chars
		t.Errorf("expected 64-char SHA-256 hex fingerprint, got %d chars", len(fp))
	}
}

// ===========================================================================
// filterNewPacks
// ===========================================================================

func TestFilterNewPacks_EmptyPacks(t *testing.T) {
	ms := &mockDownloadStore{}
	packs := filterNewPacks(context.Background(), ms, nil)
	if packs != nil {
		t.Errorf("expected nil for nil packs, got %d packs", len(packs))
	}

	packs = filterNewPacks(context.Background(), ms, []*entities.XDCCPack{})
	if packs != nil {
		t.Errorf("expected nil for empty packs, got %d packs", len(packs))
	}
}

func TestFilterNewPacks_AllNew(t *testing.T) {
	ms := &mockDownloadStore{existingFilenames: map[string]bool{}}
	packs := []*entities.XDCCPack{
		mkPackWithBot("a.mkv", 100, "Bot", 1),
		mkPackWithBot("b.mkv", 200, "Bot", 2),
	}

	newPacks := filterNewPacks(context.Background(), ms, packs)
	if len(newPacks) != 2 {
		t.Errorf("expected 2 new packs, got %d", len(newPacks))
	}
}

func TestFilterNewPacks_SomeAlreadyDownloaded(t *testing.T) {
	ms := &mockDownloadStore{existingFilenames: map[string]bool{"a.mkv": true}}
	packs := []*entities.XDCCPack{
		mkPackWithBot("a.mkv", 100, "Bot", 1),
		mkPackWithBot("b.mkv", 200, "Bot", 2),
	}

	newPacks := filterNewPacks(context.Background(), ms, packs)
	if len(newPacks) != 1 {
		t.Errorf("expected 1 new pack (b.mkv), got %d", len(newPacks))
	}
	if newPacks[0].GetFilename() != "b.mkv" {
		t.Errorf("expected b.mkv as new pack, got %s", newPacks[0].GetFilename())
	}
}

func TestFilterNewPacks_AllAlreadyDownloaded(t *testing.T) {
	ms := &mockDownloadStore{existingFilenames: map[string]bool{"a.mkv": true, "b.mkv": true}}
	packs := []*entities.XDCCPack{
		mkPackWithBot("a.mkv", 100, "Bot", 1),
		mkPackWithBot("b.mkv", 200, "Bot", 2),
	}

	newPacks := filterNewPacks(context.Background(), ms, packs)
	if len(newPacks) != 0 {
		t.Errorf("expected 0 new packs, got %d", len(newPacks))
	}
}

func TestFilterNewPacks_CaseInsensitive(t *testing.T) {
	ms := &mockDownloadStore{existingFilenames: map[string]bool{"a.mkv": true}}
	packs := []*entities.XDCCPack{
		mkPackWithBot("A.MKV", 100, "Bot", 1), // different case
	}

	// The mock store uses exact match (not LOWER), but the real SQLite uses LOWER()
	// The filterNewPacks function lowercases internally, so it will match
	newPacks := filterNewPacks(context.Background(), ms, packs)
	if len(newPacks) != 0 {
		t.Errorf("expected 0 new packs (case-insensitive match), got %d", len(newPacks))
	}
}

// ===========================================================================
// WatchlistRunResult
// ===========================================================================

func TestWatchlistRunResult_HasChanges(t *testing.T) {
	result := &WatchlistRunResult{
		HasChanges: true,
		NewPacks: []*entities.XDCCPack{
			mkPackWithBot("new.mkv", 100, "Bot", 1),
		},
	}

	if !result.HasChanges {
		t.Errorf("expected HasChanges=true")
	}
	if len(result.NewPacks) != 1 {
		t.Errorf("expected 1 new pack, got %d", len(result.NewPacks))
	}
}

func TestWatchlistRunResult_NoChanges(t *testing.T) {
	result := &WatchlistRunResult{
		HasChanges: false,
		NewPacks:   nil,
	}

	if result.HasChanges {
		t.Errorf("expected HasChanges=false")
	}
	if result.NewPacks != nil {
		t.Errorf("expected NewPacks=nil")
	}
}

// ===========================================================================
// Watchlist scheduler tests
// ===========================================================================

// trackedWatchlistStore wraps a simple watchlist-aware mock for scheduler tests.
type trackedWatchlistStore struct {
	mu             sync.Mutex
	enabledWLSets  []store.Watchlist
	checkedCalls   []int64 // IDs passed to SetWatchlistChecked
	getWLCallCount int
	notifiedIDs    []int64
}

func (m *trackedWatchlistStore) GetEnabledWatchlists(ctx context.Context) ([]store.Watchlist, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getWLCallCount++
	return m.enabledWLSets, nil
}

func (m *trackedWatchlistStore) SetWatchlistChecked(ctx context.Context, id int64, fp, resultsJSON string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkedCalls = append(m.checkedCalls, id)
	return nil
}

func (m *trackedWatchlistStore) SetWatchlistNotified(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notifiedIDs = append(m.notifiedIDs, id)
	return nil
}

// ---- Stub methods to satisfy store.Store ----
func (m *trackedWatchlistStore) Close() error                      { return nil }
func (m *trackedWatchlistStore) Migrate(ctx context.Context) error { return nil }
func (m *trackedWatchlistStore) CurrentSchemaVersion(ctx context.Context) (int, error) {
	return 0, nil
}
func (m *trackedWatchlistStore) AddServer(ctx context.Context, s store.ServerRecord) (int64, error) {
	return 0, nil
}
func (m *trackedWatchlistStore) GetServer(ctx context.Context, id int64) (*store.ServerRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) ListServers(ctx context.Context) ([]store.ServerRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) UpdateServer(ctx context.Context, s store.ServerRecord) error {
	return nil
}
func (m *trackedWatchlistStore) DeleteServer(ctx context.Context, id int64) error { return nil }
func (m *trackedWatchlistStore) SetServerStatus(ctx context.Context, id int64, status string) error {
	return nil
}
func (m *trackedWatchlistStore) SetServerConnected(ctx context.Context, id int64) error {
	return nil
}
func (m *trackedWatchlistStore) IncrementServerRetry(ctx context.Context, id int64) error {
	return nil
}
func (m *trackedWatchlistStore) ResetAllServerStatuses(ctx context.Context) error {
	return nil
}
func (m *trackedWatchlistStore) AddChannel(ctx context.Context, c store.ChannelRecord) (int64, error) {
	return 0, nil
}
func (m *trackedWatchlistStore) GetChannelsByServer(ctx context.Context, id int64) ([]store.ChannelRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) GetChannelsByServerAndName(ctx context.Context, id int64, name string) (*store.ChannelRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) UpdateChannel(ctx context.Context, c store.ChannelRecord) error {
	return nil
}
func (m *trackedWatchlistStore) DeleteChannel(ctx context.Context, id int64) error { return nil }
func (m *trackedWatchlistStore) SetChannelJoined(ctx context.Context, id int64, joined bool) error {
	return nil
}
func (m *trackedWatchlistStore) UpdateChannelTopic(ctx context.Context, id int64, topic string) error {
	return nil
}
func (m *trackedWatchlistStore) GetAutoJoinChannels(ctx context.Context) ([]store.ChannelRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) EnqueueDownload(ctx context.Context, d store.DownloadRecord) (int64, error) {
	return 0, nil
}
func (m *trackedWatchlistStore) GetDownload(ctx context.Context, id int64) (*store.DownloadRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) GetQueue(ctx context.Context) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) GetQueueByChannel(ctx context.Context, channel string) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) GetActiveDownloads(ctx context.Context) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) GetPendingByChannel(ctx context.Context, channel string) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) UpdateDownloadProgress(ctx context.Context, id int64, progressBytes int64, speedBPS int64) error {
	return nil
}
func (m *trackedWatchlistStore) MarkDownloadStarted(ctx context.Context, id int64) error {
	return nil
}
func (m *trackedWatchlistStore) MarkDownloadCompleted(ctx context.Context, id int64, filename string, fileSize int64) error {
	return nil
}
func (m *trackedWatchlistStore) MarkDownloadFailed(ctx context.Context, id int64, errMsg string) error {
	return nil
}
func (m *trackedWatchlistStore) MarkDownloadSkipped(ctx context.Context, id int64) error {
	return nil
}
func (m *trackedWatchlistStore) MarkDownloadPaused(ctx context.Context, id int64) error { return nil }
func (m *trackedWatchlistStore) MarkDownloadRetry(ctx context.Context, id int64, newStatus string) error {
	return nil
}
func (m *trackedWatchlistStore) DeleteDownload(ctx context.Context, id int64) error { return nil }
func (m *trackedWatchlistStore) RetryDownload(ctx context.Context, id int64) error  { return nil }
func (m *trackedWatchlistStore) GetDownloadHistory(ctx context.Context, _, _ int, _ store.HistoryFilter) ([]store.DownloadRecord, int, error) {
	return nil, 0, nil
}
func (m *trackedWatchlistStore) GetTotalDownloadedBytes(ctx context.Context) (int64, error) {
	return 0, nil
}
func (m *trackedWatchlistStore) RecoverDownloadsOnStartup(ctx context.Context) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) RequeueDownload(ctx context.Context, id int64) error { return nil }
func (m *trackedWatchlistStore) SetDownloadPriority(ctx context.Context, id int64, priority int) error {
	return nil
}
func (m *trackedWatchlistStore) IncrementDownloadRetry(ctx context.Context, id int64) error {
	return nil
}
func (m *trackedWatchlistStore) UpdateDownloadMetadata(ctx context.Context, id int64, filename string, fileSize int64) error {
	return nil
}
func (m *trackedWatchlistStore) UpdateChannelAvgSpeed(ctx context.Context, serverAddress, channelName string, lastSpeedBPS float64) error {
	return nil
}
func (m *trackedWatchlistStore) BulkActionDownloads(ctx context.Context, ids []int64, action string) (map[int64]string, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) FindDuplicateDownload(ctx context.Context, bot, serverAddress string, packNumber int) (*store.DownloadRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) GetDownloadByBotMessage(ctx context.Context, bot, packMessage string) (*store.DownloadRecord, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) FilenamesExist(ctx context.Context, filenames []string) (map[string]bool, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) SetSearchCache(ctx context.Context, e store.SearchCacheEntry) error {
	return nil
}
func (m *trackedWatchlistStore) GetSearchCache(ctx context.Context, query, provider string) (*store.SearchCacheEntry, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) GetSearchCacheByQuery(ctx context.Context, query string) ([]store.SearchCacheEntry, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) DeleteExpiredSearchCache(ctx context.Context, t time.Time) error {
	return nil
}
func (m *trackedWatchlistStore) AddSearchPreset(ctx context.Context, p store.SearchPreset) (int64, error) {
	return 0, nil
}
func (m *trackedWatchlistStore) GetSearchPreset(ctx context.Context, id int64) (*store.SearchPreset, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) ListSearchPresets(ctx context.Context) ([]store.SearchPreset, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) UpdateSearchPreset(ctx context.Context, p store.SearchPreset) error {
	return nil
}
func (m *trackedWatchlistStore) DeleteSearchPreset(ctx context.Context, id int64) error { return nil }
func (m *trackedWatchlistStore) SetDefaultSearchPreset(ctx context.Context, id int64) error {
	return nil
}
func (m *trackedWatchlistStore) AddWatchlist(ctx context.Context, w store.Watchlist) (int64, error) {
	return 0, nil
}
func (m *trackedWatchlistStore) GetWatchlist(ctx context.Context, id int64) (*store.Watchlist, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) ListWatchlists(ctx context.Context) ([]store.Watchlist, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) UpdateWatchlist(ctx context.Context, w store.Watchlist) error {
	return nil
}
func (m *trackedWatchlistStore) DeleteWatchlist(ctx context.Context, id int64) error { return nil }
func (m *trackedWatchlistStore) RecordProviderStats(ctx context.Context, s store.ProviderStats) error {
	return nil
}
func (m *trackedWatchlistStore) GetProviderStats(ctx context.Context, provider string, since time.Time) ([]store.ProviderStats, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) GetAllProviderStats(ctx context.Context, since time.Time) (map[string][]store.ProviderStats, error) {
	return nil, nil
}
func (m *trackedWatchlistStore) CleanupOldDownloads(ctx context.Context, retentionDays int) (int, error) {
	return 0, nil
}
func (m *trackedWatchlistStore) RunCleanup(ctx context.Context, retentionDays int, interval time.Duration) (chan struct{}, chan struct{}, error) {
	return nil, nil, nil
}
func (m *trackedWatchlistStore) Vacuum(ctx context.Context) error { return nil }
func (m *trackedWatchlistStore) ExportData(ctx context.Context) (*store.ExportData, error) {
	return &store.ExportData{}, nil
}
func (m *trackedWatchlistStore) ImportData(ctx context.Context, data *store.ExportData) error {
	return nil
}
func (m *trackedWatchlistStore) BackupDatabase(ctx context.Context, path string) error { return nil }

// ---- Helpers for setting up watchlist data ----

func (m *trackedWatchlistStore) setEnabledWatchlists(wls []store.Watchlist) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabledWLSets = wls
}

func (m *trackedWatchlistStore) checkedWatchlists() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]int64, len(m.checkedCalls))
	copy(out, m.checkedCalls)
	return out
}

func pointerTime(t time.Time) *time.Time { return &t }

// newTestAggregator creates an Aggregator with a tracked store and a
// completed channel that signals when RunWatchlist has finished.
func newTestAggregator(ms *trackedWatchlistStore) *Aggregator {
	cfg := config.DefaultConfig()
	cfg.Search.Cache.Enabled = false
	cfg.Search.EnabledProviders = []string{"__test_none__"} // block all real engines
	logger := logging.New(logging.LevelDebug, "", 0)
	agg := New(ms, &cfg.Search, logger)
	agg.ctx = context.Background()
	return agg
}

// TestWatchlistInFlight_Dedup verifies that the sync.Map-based in-flight
// tracker prevents concurrent runs of the same watchlist.
func TestWatchlistInFlight_Dedup(t *testing.T) {
	agg := &Aggregator{}

	wlID := int64(42)

	// First LoadOrStore: key not present, should succeed
	_, loaded := agg.watchlistInFlight.LoadOrStore(wlID, struct{}{})
	if loaded {
		t.Error("expected first LoadOrStore to return loaded=false (key was absent)")
	}

	// Second LoadOrStore: key already present, should report loaded
	_, loaded = agg.watchlistInFlight.LoadOrStore(wlID, struct{}{})
	if !loaded {
		t.Error("expected second LoadOrStore to return loaded=true (key already exists)")
	}

	// After Delete, key should be removable
	agg.watchlistInFlight.Delete(wlID)

	// Third LoadOrStore after Delete: key is gone, should succeed again
	_, loaded = agg.watchlistInFlight.LoadOrStore(wlID, struct{}{})
	if loaded {
		t.Error("expected LoadOrStore after Delete to return loaded=false")
	}

	// Cleanup
	agg.watchlistInFlight.Delete(wlID)
}

// ===========================================================================
// NotifyEnabled guard tests
// ===========================================================================

// TestRunWatchlist_NotifyDisabledSkipsCallback verifies that the external
// notification callback is NOT invoked when NotifyEnabled=false.
func TestRunWatchlist_NotifyDisabledSkipsCallback(t *testing.T) {
	ms := &trackedWatchlistStore{}
	agg := newTestAggregator(ms)

	var callbackCount int32
	agg.SetOnWatchlistResults(func(name string, newCount, enqueued int) {
		atomic.AddInt32(&callbackCount, 1)
	})

	wl := store.Watchlist{
		ID:              1,
		Name:            "test-wl",
		Query:           "anything",
		IntervalMinutes: 5,
		NotifyEnabled:   false,
		Enabled:         true,
		AutoEnqueue:     false,
	}

	_, err := agg.RunWatchlist(context.Background(), wl)
	if err != nil {
		t.Fatalf("RunWatchlist: %v", err)
	}
	if atomic.LoadInt32(&callbackCount) != 0 {
		t.Errorf("callback called %d times; expected 0 (NotifyEnabled=false)", callbackCount)
	}
}

// TestRunWatchlist_NotifyEnabledNoNewPacks verifies that the callback
// is NOT invoked when NotifyEnabled=true but there are no new packs.
// This confirms the guard condition works end-to-end: all conditions
// (NotifyEnabled, HasChanges, len(NewPacks)>0) must be satisfied.
func TestRunWatchlist_NotifyEnabledNoNewPacks(t *testing.T) {
	ms := &trackedWatchlistStore{}
	agg := newTestAggregator(ms)

	var callbackCount int32
	agg.SetOnWatchlistResults(func(name string, newCount, enqueued int) {
		atomic.AddInt32(&callbackCount, 1)
	})

	// Use a non-empty LastMatchFingerprint so HasChanges logic evaluates
	// the fingerprint-change path (else-if branch). With no search engines
	// configured, the search returns empty packs → NewPacks = 0.
	wl := store.Watchlist{
		ID:                   2,
		Name:                 "test-wl-2",
		Query:                "anything",
		IntervalMinutes:      5,
		NotifyEnabled:        true,
		Enabled:              true,
		AutoEnqueue:          false,
		LastMatchFingerprint: "old-fingerprint",
	}

	_, err := agg.RunWatchlist(context.Background(), wl)
	if err != nil {
		t.Fatalf("RunWatchlist: %v", err)
	}
	if atomic.LoadInt32(&callbackCount) != 0 {
		t.Errorf("callback called %d times; expected 0 (no new packs)", callbackCount)
	}
}

// TestWatchlistInFlight_ReentrancyGuard verifies the LoadOrStore/Delete
// reentrancy pattern used by runWatchlistSafely. The first call acquires
// the gate (loaded=false), and a second call while the first holds it
// correctly sees loaded=true — preventing concurrent execution.
func TestWatchlistInFlight_ReentrancyGuard(t *testing.T) {
	agg := &Aggregator{}
	wlID := int64(99)

	// Simulate runWatchlistSafely's gate pattern
	_, loaded := agg.watchlistInFlight.LoadOrStore(wlID, struct{}{})
	if loaded {
		t.Fatal("first LoadOrStore must return loaded=false")
	}

	// While the first "execution" holds the gate, a concurrent caller
	// should see loaded=true and skip. We simulate this by not deleting yet.
	_, loaded = agg.watchlistInFlight.LoadOrStore(wlID, struct{}{})
	if !loaded {
		t.Fatal("second LoadOrStore must return loaded=true (gate still held)")
	}

	// After the first execution completes, Delete releases the gate.
	agg.watchlistInFlight.Delete(wlID)

	// Now the next caller can acquire the gate again.
	_, loaded = agg.watchlistInFlight.LoadOrStore(wlID, struct{}{})
	if loaded {
		t.Fatal("LoadOrStore after Delete must return loaded=false (gate released)")
	}

	// Clean up
	agg.watchlistInFlight.Delete(wlID)
}

// TestRunDueWatchlists_FirstRun verifies that a watchlist with nil
// LastCheckedAt (never run before) is dispatched by the scheduler.
// Uses EnabledProviders=[] to avoid real search engine calls — the
// goroutine completes synchronously since RunWatchlist returns quickly.
func TestRunDueWatchlists_FirstRun(t *testing.T) {
	ms := &trackedWatchlistStore{}
	agg := newTestAggregator(ms)

	now := time.Now()
	ms.setEnabledWatchlists([]store.Watchlist{
		{ID: 1, Name: "first-run", Query: "test", IntervalMinutes: 10,
			Enabled: true, LastCheckedAt: nil},
		{ID: 2, Name: "recent", Query: "test", IntervalMinutes: 10,
			Enabled: true, LastCheckedAt: pointerTime(now.Add(-1 * time.Minute))},
	})

	agg.runDueWatchlists()

	// runDueWatchlists spawns goroutines — wait for them. With no providers
	// configured, search returns empty quickly. Use a short polling loop.
	deadline := time.Now().Add(2 * time.Second)
	var checked []int64
	for time.Now().Before(deadline) {
		checked = ms.checkedWatchlists()
		if len(checked) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	hasID1 := false
	hasID2 := false
	for _, id := range checked {
		if id == 1 {
			hasID1 = true
		}
		if id == 2 {
			hasID2 = true
		}
	}

	if !hasID1 {
		t.Error("expected watchlist ID=1 (first-run) to be dispatched by scheduler")
	}
	if hasID2 {
		t.Error("expected watchlist ID=2 (recently checked) to NOT be dispatched")
	}
}

// TestRunDueWatchlists_IntervalElapsed verifies that a watchlist whose
// interval has elapsed since last check is dispatched.
func TestRunDueWatchlists_IntervalElapsed(t *testing.T) {
	ms := &trackedWatchlistStore{}
	agg := newTestAggregator(ms)

	now := time.Now()
	ms.setEnabledWatchlists([]store.Watchlist{
		{ID: 1, Name: "overdue", Query: "test", IntervalMinutes: 5,
			Enabled: true, LastCheckedAt: pointerTime(now.Add(-60 * time.Minute))},
	})

	agg.runDueWatchlists()

	deadline := time.Now().Add(2 * time.Second)
	var checked []int64
	for time.Now().Before(deadline) {
		checked = ms.checkedWatchlists()
		if len(checked) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	found := false
	for _, id := range checked {
		if id == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected overdue watchlist (60 min since last check, 5 min interval) to be dispatched")
	}
}
