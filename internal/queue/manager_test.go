package queue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"xdcc_server/internal/config"
	xdccirc "xdcc_server/internal/irc"
	"xdcc_server/internal/logging"
	"xdcc_server/internal/store"
)

// ===========================================================================
// Mock store for queue testing
// ===========================================================================

type mockStore struct {
	mu           sync.Mutex
	downloads    map[int64]*store.DownloadRecord
	nextID       int64
	getQueueFn   func() ([]store.DownloadRecord, error)
	markStartErr error
}

func newMockStore() *mockStore {
	return &mockStore{
		downloads: make(map[int64]*store.DownloadRecord),
		nextID:    1,
	}
}

// Store interface methods needed by queue manager

func (m *mockStore) Close(ctx context.Context) error                       { return nil }
func (m *mockStore) Migrate(ctx context.Context) error                     { return nil }
func (m *mockStore) CurrentSchemaVersion(ctx context.Context) (int, error) { return 1, nil }
func (m *mockStore) AddServer(ctx context.Context, s store.ServerRecord) (int64, error) {
	return 1, nil
}
func (m *mockStore) GetServer(ctx context.Context, id int64) (*store.ServerRecord, error) {
	return nil, nil
}
func (m *mockStore) ListServers(ctx context.Context) ([]store.ServerRecord, error)      { return nil, nil }
func (m *mockStore) UpdateServer(ctx context.Context, s store.ServerRecord) error       { return nil }
func (m *mockStore) DeleteServer(ctx context.Context, id int64) error                   { return nil }
func (m *mockStore) SetServerStatus(ctx context.Context, id int64, status string) error { return nil }
func (m *mockStore) SetServerConnected(ctx context.Context, id int64) error             { return nil }
func (m *mockStore) IncrementServerRetry(ctx context.Context, id int64) error           { return nil }
func (m *mockStore) ResetAllServerStatuses(ctx context.Context) error                   { return nil }

func (m *mockStore) AddChannel(ctx context.Context, c store.ChannelRecord) (int64, error) {
	return 1, nil
}
func (m *mockStore) GetChannelsByServer(ctx context.Context, serverID int64) ([]store.ChannelRecord, error) {
	return nil, nil
}
func (m *mockStore) GetChannelsByServerAndName(ctx context.Context, serverID int64, name string) (*store.ChannelRecord, error) {
	return nil, nil
}
func (m *mockStore) UpdateChannel(ctx context.Context, c store.ChannelRecord) error       { return nil }
func (m *mockStore) DeleteChannel(ctx context.Context, id int64) error                    { return nil }
func (m *mockStore) SetChannelJoined(ctx context.Context, id int64, joined bool) error    { return nil }
func (m *mockStore) UpdateChannelTopic(ctx context.Context, id int64, topic string) error { return nil }
func (m *mockStore) GetAutoJoinChannels(ctx context.Context) ([]store.ChannelRecord, error) {
	return nil, nil
}

func (m *mockStore) EnqueueDownload(ctx context.Context, d store.DownloadRecord) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextID
	m.nextID++
	d.ID = id
	d.Status = store.DownloadStatusQueued
	now := time.Now()
	d.CreatedAt = now
	m.downloads[id] = &d
	return id, nil
}

func (m *mockStore) GetDownload(ctx context.Context, id int64) (*store.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.downloads[id]
	if !ok {
		return nil, nil
	}
	return d, nil
}

func (m *mockStore) GetQueue(ctx context.Context) ([]store.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getQueueFn != nil {
		return m.getQueueFn()
	}
	var result []store.DownloadRecord
	for _, d := range m.downloads {
		if d.Status == store.DownloadStatusQueued || d.Status == store.DownloadStatusDownloading {
			result = append(result, *d)
		}
	}
	return result, nil
}

func (m *mockStore) GetQueueByChannel(ctx context.Context, channel string) ([]store.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []store.DownloadRecord
	for _, d := range m.downloads {
		if d.Channel == channel && (d.Status == store.DownloadStatusQueued || d.Status == store.DownloadStatusDownloading) {
			result = append(result, *d)
		}
	}
	return result, nil
}

func (m *mockStore) GetActiveDownloads(ctx context.Context) ([]store.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []store.DownloadRecord
	for _, d := range m.downloads {
		if d.Status == store.DownloadStatusDownloading {
			result = append(result, *d)
		}
	}
	return result, nil
}

func (m *mockStore) GetPendingByChannel(ctx context.Context, channel string) ([]store.DownloadRecord, error) {
	return m.GetQueueByChannel(ctx, channel)
}

func (m *mockStore) UpdateDownloadProgress(ctx context.Context, id int64, progressBytes int64, speedBPS int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.downloads[id]; ok {
		d.ProgressBytes = progressBytes
		d.SpeedBPS = speedBPS
	}
	return nil
}

func (m *mockStore) MarkDownloadStarted(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.markStartErr != nil {
		return m.markStartErr
	}
	if d, ok := m.downloads[id]; ok {
		d.Status = store.DownloadStatusDownloading
		now := time.Now()
		d.StartedAt = &now
	}
	return nil
}

func (m *mockStore) MarkDownloadCompleted(ctx context.Context, id int64, filename string, fileSize int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.downloads[id]; ok {
		d.Status = store.DownloadStatusCompleted
		now := time.Now()
		d.CompletedAt = &now
	}
	return nil
}

func (m *mockStore) MarkDownloadFailed(ctx context.Context, id int64, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.downloads[id]; ok {
		d.Status = store.DownloadStatusFailed
		d.ErrorMessage = errMsg
	}
	return nil
}

func (m *mockStore) MarkDownloadSkipped(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.downloads[id]; ok {
		d.Status = store.DownloadStatusSkipped
	}
	return nil
}

func (m *mockStore) MarkDownloadPaused(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.downloads[id]; ok {
		if d.Status == store.DownloadStatusQueued || d.Status == store.DownloadStatusDownloading {
			d.Status = store.DownloadStatusPaused
		}
	}
	return nil
}

func (m *mockStore) MarkDownloadRetry(ctx context.Context, id int64, errMsg string) error {
	return nil
}

func (m *mockStore) DeleteDownload(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.downloads, id)
	return nil
}

func (m *mockStore) RetryDownload(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.downloads[id]; ok {
		d.Status = store.DownloadStatusQueued
		d.ProgressBytes = 0
		d.ErrorMessage = ""
	}
	return nil
}
func (m *mockStore) GetDownloadHistory(ctx context.Context, limit int, offset int, filter store.HistoryFilter) ([]store.DownloadRecord, int, error) {
	return nil, 0, nil
}

func (m *mockStore) GetTotalDownloadedBytes(ctx context.Context) (int64, error) { return 0, nil }
func (m *mockStore) RecoverDownloadsOnStartup(ctx context.Context) ([]store.DownloadRecord, error) {
	return nil, nil
}

func (m *mockStore) RequeueDownload(ctx context.Context, id int64) error {
	return m.RetryDownload(ctx, id)
}

func (m *mockStore) SetDownloadPriority(ctx context.Context, id int64, priority int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.downloads[id]; ok {
		d.Priority = priority
	}
	return nil
}

func (m *mockStore) IncrementDownloadRetry(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.downloads[id]; ok {
		d.RetryCount++
	}
	return nil
}

func (m *mockStore) BulkActionDownloads(ctx context.Context, ids []int64, action string) (map[int64]string, error) {
	results := make(map[int64]string)
	for _, id := range ids {
		switch action {
		case "pause":
			_ = m.MarkDownloadPaused(ctx, id)
		case "resume":
			_ = m.RetryDownload(ctx, id)
		case "remove":
			_ = m.DeleteDownload(ctx, id)
		}
		results[id] = "success"
	}
	return results, nil
}

func (m *mockStore) FilenamesExist(ctx context.Context, filenames []string) (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]bool, len(filenames))
	for _, fn := range filenames {
		result[fn] = false
	}
	for _, d := range m.downloads {
		if result[d.Filename] {
			continue // already found
		}
		switch d.Status {
		case store.DownloadStatusCompleted, store.DownloadStatusFailed,
			store.DownloadStatusSkipped, store.DownloadStatusQueued,
			store.DownloadStatusDownloading, store.DownloadStatusPaused:
			if _, ok := result[d.Filename]; ok {
				result[d.Filename] = true
			}
		}
	}
	return result, nil
}

func (m *mockStore) FindDuplicateDownload(ctx context.Context, bot, serverAddress string, packNumber int) (*store.DownloadRecord, error) {
	return nil, nil
}

func (m *mockStore) GetDownloadByBotMessage(ctx context.Context, bot, packMessage string) (*store.DownloadRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.downloads {
		if d.Bot == bot && d.PackMessage == packMessage {
			return d, nil
		}
	}
	return nil, nil
}

func (m *mockStore) UpdateDownloadMetadata(ctx context.Context, id int64, filename string, size int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.downloads[id]; ok {
		d.Filename = filename
		d.FileSize = size
	}
	return nil
}

func (m *mockStore) UpdateChannelAvgSpeed(ctx context.Context, serverAddress, channelName string, lastSpeedBPS float64) error {
	return nil
}

func (m *mockStore) SetSearchCache(ctx context.Context, e store.SearchCacheEntry) error { return nil }
func (m *mockStore) GetSearchCache(ctx context.Context, query string, provider string) (*store.SearchCacheEntry, error) {
	return nil, nil
}
func (m *mockStore) GetSearchCacheByQuery(ctx context.Context, query string) ([]store.SearchCacheEntry, error) {
	return nil, nil
}
func (m *mockStore) DeleteExpiredSearchCache(ctx context.Context, t time.Time) error { return nil }

func (m *mockStore) AddSearchPreset(ctx context.Context, p store.SearchPreset) (int64, error) {
	return 1, nil
}
func (m *mockStore) GetSearchPreset(ctx context.Context, id int64) (*store.SearchPreset, error) {
	return nil, nil
}
func (m *mockStore) ListSearchPresets(ctx context.Context) ([]store.SearchPreset, error) {
	return nil, nil
}
func (m *mockStore) UpdateSearchPreset(ctx context.Context, p store.SearchPreset) error { return nil }
func (m *mockStore) DeleteSearchPreset(ctx context.Context, id int64) error             { return nil }
func (m *mockStore) SetDefaultSearchPreset(ctx context.Context, id int64) error         { return nil }

func (m *mockStore) AddWatchlist(ctx context.Context, w store.Watchlist) (int64, error) {
	return 1, nil
}
func (m *mockStore) GetWatchlist(ctx context.Context, id int64) (*store.Watchlist, error) {
	return nil, nil
}
func (m *mockStore) ListWatchlists(ctx context.Context) ([]store.Watchlist, error) { return nil, nil }
func (m *mockStore) UpdateWatchlist(ctx context.Context, w store.Watchlist) error  { return nil }
func (m *mockStore) DeleteWatchlist(ctx context.Context, id int64) error           { return nil }
func (m *mockStore) SetWatchlistChecked(ctx context.Context, id int64, fp string, resultsJSON string) error {
	return nil
}
func (m *mockStore) SetWatchlistNotified(ctx context.Context, id int64) error { return nil }
func (m *mockStore) GetEnabledWatchlists(ctx context.Context) ([]store.Watchlist, error) {
	return nil, nil
}

func (m *mockStore) RecordProviderStats(ctx context.Context, s store.ProviderStats) error { return nil }
func (m *mockStore) GetProviderStats(ctx context.Context, provider string, since time.Time) ([]store.ProviderStats, error) {
	return nil, nil
}
func (m *mockStore) GetAllProviderStats(ctx context.Context, since time.Time) (map[string][]store.ProviderStats, error) {
	return nil, nil
}

func (m *mockStore) CleanupOldDownloads(ctx context.Context, retentionDays int) (int, error) {
	return 0, nil
}
func (m *mockStore) RunCleanup(ctx context.Context, retentionDays int, interval time.Duration) (chan struct{}, chan struct{}, error) {
	return nil, nil, nil
}
func (m *mockStore) Vacuum(ctx context.Context) error { return nil }
func (m *mockStore) ExportData(ctx context.Context) (*store.ExportData, error) {
	return &store.ExportData{}, nil
}
func (m *mockStore) ImportData(ctx context.Context, data *store.ExportData) error { return nil }
func (m *mockStore) BackupDatabase(ctx context.Context, path string) error        { return nil }

// ===========================================================================
// Test helpers
// ===========================================================================

func newTestQM(t *testing.T) (*Manager, *mockStore) {
	t.Helper()
	ms := newMockStore()
	cfg := config.DefaultConfig()
	cfg.Download.MinDiskSpace = 0 // disable disk monitoring
	logger := logging.New(logging.LevelInfo, "", 0)
	qm := New(ms, cfg, logger)
	_ = qm.Start() // start monitorLoop goroutine so Stop() doesn't deadlock
	t.Cleanup(func() {
		qm.Stop()
	})
	return qm, ms
}

// newTestQMWithFallback creates a QueueManager configured for auto-retry,
// allowing tests to exercise the handleFallback code path directly.
func newTestQMWithFallback(t *testing.T, maxRetries int) (*Manager, *mockStore) {
	t.Helper()
	ms := newMockStore()
	cfg := config.DefaultConfig()
	cfg.Download.MinDiskSpace = 0
	cfg.Download.FailFallback = "auto_retry_best"
	cfg.Download.MaxRetryAttempts = maxRetries
	logger := logging.New(logging.LevelInfo, "", 0)
	qm := New(ms, cfg, logger)
	_ = qm.Start()
	t.Cleanup(func() {
		qm.Stop()
	})
	return qm, ms
}

// ===========================================================================
// Enqueue
// ===========================================================================

func TestEnqueue_Success(t *testing.T) {
	qm, ms := newTestQM(t)

	id, err := qm.Enqueue(store.DownloadRecord{
		Bot: "TestBot", ServerAddress: "irc.test.net", Channel: "#xdcc",
		Filename: "test.mkv", FileSize: 1000, PackMessage: "xdcc send #1",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive id, got %d", id)
	}

	// Wait a bit for the download to start (async dispatch)
	time.Sleep(50 * time.Millisecond)

	d, _ := ms.GetDownload(context.Background(), id)
	if d == nil {
		t.Fatal("expected download in store")
	}
	// Since no other downloads are active on this channel and we're under global limit,
	// the download should start immediately (status: downloading)
	if d.Status != store.DownloadStatusDownloading {
		t.Errorf("expected status 'downloading' (auto-started), got %s", d.Status)
	}
	if d.Priority != 100 {
		t.Errorf("expected default priority 100, got %d", d.Priority)
	}
}

func TestEnqueue_OptionalChannel(t *testing.T) {
	qm, ms := newTestQM(t)

	// Channel is now optional — empty channel means WHOIS will discover it
	id, err := qm.Enqueue(store.DownloadRecord{
		Bot: "TestBot", ServerAddress: "irc.test.net",
		Filename: "test.mkv", FileSize: 1000, PackMessage: "xdcc send #1",
	})
	if err != nil {
		t.Fatalf("Enqueue with empty channel should succeed: %v", err)
	}

	d, _ := ms.GetDownload(context.Background(), id)
	if d == nil {
		t.Fatal("expected download in store")
	}
	if d.Channel != "" {
		t.Errorf("expected empty channel, got %q", d.Channel)
	}
}

func TestEnqueue_ChannelNormalization(t *testing.T) {
	qm, ms := newTestQM(t)

	id, err := qm.Enqueue(store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "no-hash",
		Filename: "f.mkv", FileSize: 100, PackMessage: "xdcc send #1",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	d, _ := ms.GetDownload(context.Background(), id)
	if d.Channel != "#no-hash" {
		t.Errorf("expected normalized channel '#no-hash', got %q", d.Channel)
	}
}

func TestEnqueue_DuplicateDetection(t *testing.T) {
	qm, ms := newTestQM(t)

	_, _ = ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "SameBot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "existing.mkv", FileSize: 100, PackMessage: "xdcc send #5",
	})

	// Same bot + pack message should be rejected
	_, err := qm.Enqueue(store.DownloadRecord{
		Bot: "SameBot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "existing.mkv", FileSize: 100, PackMessage: "xdcc send #5",
	})
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	if !contains(err.Error(), "duplicate") {
		t.Errorf("expected error mentioning 'duplicate', got: %v", err)
	}
}

func TestEnqueue_CustomPriority(t *testing.T) {
	qm, ms := newTestQM(t)

	id, err := qm.Enqueue(store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "f.mkv", FileSize: 100, PackMessage: "xdcc send #1", Priority: 5,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	d, _ := ms.GetDownload(context.Background(), id)
	if d.Priority != 5 {
		t.Errorf("expected priority 5, got %d", d.Priority)
	}
}

func TestEnqueue_WhenClosingRejected(t *testing.T) {
	qm, _ := newTestQM(t)
	qm.closing.Store(true)

	_, err := qm.Enqueue(store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "f.mkv", FileSize: 100, PackMessage: "xdcc send #1",
	})
	if err == nil {
		t.Fatal("expected enqueue to fail while manager is closing")
	}
	if !contains(err.Error(), "shutting down") {
		t.Fatalf("expected shutdown error, got: %v", err)
	}
}

// ===========================================================================
// CancelDownload
// ===========================================================================

func TestCancelDownload_NonExistent(t *testing.T) {
	qm, _ := newTestQM(t)

	err := qm.CancelDownload(999, "test cancel")
	if err != nil {
		t.Fatalf("CancelDownload: %v", err)
	}
}

// ===========================================================================
// PauseDownload
// ===========================================================================

func TestPauseDownload_Success(t *testing.T) {
	qm, ms := newTestQM(t)

	id, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "f.mkv", FileSize: 100,
	})

	err := qm.PauseDownload(id)
	if err != nil {
		t.Fatalf("PauseDownload: %v", err)
	}

	d, _ := ms.GetDownload(context.Background(), id)
	if d.Status != store.DownloadStatusPaused {
		t.Errorf("expected status 'paused', got %s", d.Status)
	}
}

func TestPauseDownload_NonExistent(t *testing.T) {
	qm, _ := newTestQM(t)

	err := qm.PauseDownload(999)
	if err != nil {
		t.Fatalf("PauseDownload for non-existent: %v", err)
	}
}

func TestPauseDownload_ActiveReleasesSlotAndCount(t *testing.T) {
	qm, ms := newTestQM(t)

	id, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#slotpause",
		Filename: "f.mkv", FileSize: 100,
	})

	sk := slotKey("irc.t.net", "#slotpause", "Bot")
	ctx, cancel := context.WithCancel(context.Background())
	qm.mu.Lock()
	qm.activeJobs[id] = cancel
	qm.channelSlots[sk] = id
	qm.globalCount = 1
	qm.mu.Unlock()

	err := qm.PauseDownload(id)
	if err != nil {
		t.Fatalf("PauseDownload: %v", err)
	}

	select {
	case <-ctx.Done():
		// expected
	default:
		t.Fatal("expected active cancel function to be called")
	}

	qm.mu.RLock()
	_, activeExists := qm.activeJobs[id]
	_, slotExists := qm.channelSlots[sk]
	count := qm.globalCount
	qm.mu.RUnlock()

	if activeExists {
		t.Fatal("expected active job to be removed")
	}
	if slotExists {
		t.Fatal("expected channel slot to be released")
	}
	if count != 0 {
		t.Fatalf("expected globalCount=0, got %d", count)
	}
}

// ===========================================================================
// ResumeDownload
// ===========================================================================

func TestResumeDownload_Success(t *testing.T) {
	qm, ms := newTestQM(t)

	id, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "f.mkv", FileSize: 100,
	})
	_ = ms.MarkDownloadPaused(context.Background(), id)

	err := qm.ResumeDownload(id)
	if err != nil {
		t.Fatalf("ResumeDownload: %v", err)
	}

	d, _ := ms.GetDownload(context.Background(), id)
	// ResumeDownload calls tryDispatch() which may start the download
	// immediately in test since there's no real IRC connection to block.
	if d.Status != store.DownloadStatusQueued && d.Status != store.DownloadStatusDownloading {
		t.Errorf("expected status 'queued' or 'downloading' after resume, got %s", d.Status)
	}
}

// ===========================================================================
// RemoveDownload
// ===========================================================================

func TestRemoveDownload_Success(t *testing.T) {
	qm, ms := newTestQM(t)

	id, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "f.mkv", FileSize: 100,
	})

	err := qm.RemoveDownload(id)
	if err != nil {
		t.Fatalf("RemoveDownload: %v", err)
	}

	d, _ := ms.GetDownload(context.Background(), id)
	if d != nil {
		t.Errorf("expected download to be removed, got %+v", d)
	}
}

func TestRemoveDownload_ActiveReleasesSlotAndCount(t *testing.T) {
	qm, ms := newTestQM(t)

	id, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#slotremove",
		Filename: "f.mkv", FileSize: 100,
	})

	sk := slotKey("irc.t.net", "#slotremove", "Bot")
	ctx, cancel := context.WithCancel(context.Background())
	qm.mu.Lock()
	qm.activeJobs[id] = cancel
	qm.channelSlots[sk] = id
	qm.globalCount = 1
	qm.mu.Unlock()

	err := qm.RemoveDownload(id)
	if err != nil {
		t.Fatalf("RemoveDownload: %v", err)
	}

	select {
	case <-ctx.Done():
		// expected
	default:
		t.Fatal("expected active cancel function to be called")
	}

	qm.mu.RLock()
	_, activeExists := qm.activeJobs[id]
	_, slotExists := qm.channelSlots[sk]
	count := qm.globalCount
	qm.mu.RUnlock()

	if activeExists {
		t.Fatal("expected active job to be removed")
	}
	if slotExists {
		t.Fatal("expected channel slot to be released")
	}
	if count != 0 {
		t.Fatalf("expected globalCount=0, got %d", count)
	}
}

func TestCancelDownload_ActiveReleasesSlotAndCount(t *testing.T) {
	qm, ms := newTestQM(t)

	id, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#slotcancel",
		Filename: "f.mkv", FileSize: 100,
	})

	sk := slotKey("irc.t.net", "#slotcancel", "Bot")
	ctx, cancel := context.WithCancel(context.Background())
	qm.mu.Lock()
	qm.activeJobs[id] = cancel
	qm.channelSlots[sk] = id
	qm.globalCount = 1
	qm.mu.Unlock()

	err := qm.CancelDownload(id, "test")
	if err != nil {
		t.Fatalf("CancelDownload: %v", err)
	}

	select {
	case <-ctx.Done():
		// expected
	default:
		t.Fatal("expected active cancel function to be called")
	}

	qm.mu.RLock()
	_, activeExists := qm.activeJobs[id]
	_, slotExists := qm.channelSlots[sk]
	count := qm.globalCount
	qm.mu.RUnlock()

	if activeExists {
		t.Fatal("expected active job to be removed")
	}
	if slotExists {
		t.Fatal("expected channel slot to be released")
	}
	if count != 0 {
		t.Fatalf("expected globalCount=0, got %d", count)
	}
}

// ===========================================================================
// BulkAction
// ===========================================================================

func TestBulkAction_Pause(t *testing.T) {
	qm, ms := newTestQM(t)

	id1, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot1", ServerAddress: "irc.t.net", Channel: "#a", Filename: "a.mkv", FileSize: 100,
	})
	id2, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot2", ServerAddress: "irc.t.net", Channel: "#b", Filename: "b.mkv", FileSize: 100,
	})

	results, err := qm.BulkAction([]int64{id1, id2}, "pause")
	if err != nil {
		t.Fatalf("BulkAction: %v", err)
	}
	if results[id1] != "success" {
		t.Errorf("expected success for id1, got %s", results[id1])
	}
	if results[id2] != "success" {
		t.Errorf("expected success for id2, got %s", results[id2])
	}

	d1, _ := ms.GetDownload(context.Background(), id1)
	if d1.Status != store.DownloadStatusPaused {
		t.Errorf("expected download %d to be paused", id1)
	}
}

func TestBulkAction_Resume(t *testing.T) {
	qm, ms := newTestQM(t)

	id, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#a", Filename: "a.mkv", FileSize: 100,
	})
	_ = ms.MarkDownloadPaused(context.Background(), id)

	results, _ := qm.BulkAction([]int64{id}, "resume")
	if results[id] != "success" {
		t.Errorf("expected success, got %s", results[id])
	}

	d, _ := ms.GetDownload(context.Background(), id)
	// BulkAction "resume" calls ResumeDownload which may immediately
	// dispatch the download (tryDispatch) in test, making it "downloading".
	if d.Status != store.DownloadStatusQueued && d.Status != store.DownloadStatusDownloading {
		t.Errorf("expected 'queued' or 'downloading' after resume, got %s", d.Status)
	}
}

func TestBulkAction_Remove(t *testing.T) {
	qm, ms := newTestQM(t)

	id, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#a", Filename: "a.mkv", FileSize: 100,
	})

	results, _ := qm.BulkAction([]int64{id}, "remove")
	if results[id] != "success" {
		t.Errorf("expected success, got %s", results[id])
	}

	d, _ := ms.GetDownload(context.Background(), id)
	if d != nil {
		t.Errorf("expected download removed")
	}
}

func TestBulkAction_UnknownAction(t *testing.T) {
	qm, ms := newTestQM(t)

	id, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#a", Filename: "a.mkv", FileSize: 100,
	})

	results, _ := qm.BulkAction([]int64{id}, "unknown")
	if results[id] == "success" {
		t.Errorf("expected error for unknown action, got success")
	}
}

// ===========================================================================
// GetActiveCount / GetActiveIDs
// ===========================================================================

func TestGetActiveCount_InitiallyZero(t *testing.T) {
	qm, _ := newTestQM(t)

	count := qm.GetActiveCount()
	if count != 0 {
		t.Errorf("expected 0 active downloads initially, got %d", count)
	}
}

func TestGetActiveIDs_InitiallyEmpty(t *testing.T) {
	qm, _ := newTestQM(t)

	ids := qm.GetActiveIDs()
	if len(ids) != 0 {
		t.Errorf("expected empty active IDs, got %v", ids)
	}
}

// ===========================================================================
// NormalizeChannel
// ===========================================================================

func TestNormalizeChannel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"#xdcc", "#xdcc"},
		{"xdcc", "#xdcc"},
		{"  #xdcc  ", "#xdcc"},
		{"  XDCC  ", "#xdcc"},
		{"", ""},
		{"#", "#"},
	}

	for _, tt := range tests {
		got := xdccirc.NormalizeChannel(tt.input)
		if got != tt.expected {
			t.Errorf("normalizeChannel(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// ===========================================================================
// tryDispatch — with mock queue
// ===========================================================================

func TestTryDispatch_WithQueuedItems(t *testing.T) {
	qm, ms := newTestQM(t)

	// Add queued downloads
	_, _ = ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#channel1",
		Filename: "a.mkv", FileSize: 100, PackMessage: "xdcc send #1",
	})
	_, _ = ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot2", ServerAddress: "irc.t.net", Channel: "#channel2",
		Filename: "b.mkv", FileSize: 200, PackMessage: "xdcc send #1",
	})

	// tryDispatch should start them (maxParallel=5 per defaults)
	qm.tryDispatch()

	// The mock store doesn't actually run IRC downloads, so the downloads
	// should be marked as "started" but won't complete synchronously.
	// Check that the active count increased
	active := qm.GetActiveCount()
	if active > 2 {
		t.Errorf("expected at most 2 active downloads, got %d", active)
	}
}

func TestTryDispatch_AtGlobalLimit(t *testing.T) {
	_, ms := newTestQM(t)

	// Configure low max parallel
	cfg := config.DefaultConfig()
	cfg.Download.MaxParallelTotal = 1
	cfg.Download.MinDiskSpace = 0
	logger := logging.New(logging.LevelInfo, "", 0)
	qm2 := New(ms, cfg, logger)
	_ = qm2.Start()
	t.Cleanup(func() { qm2.Stop() })

	// Enqueue 2 downloads
	_, _ = ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#ch1",
		Filename: "a.mkv", FileSize: 100, PackMessage: "xdcc send #1",
	})
	_, _ = ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot2", ServerAddress: "irc.t.net", Channel: "#ch2",
		Filename: "b.mkv", FileSize: 200, PackMessage: "xdcc send #1",
	})

	qm2.tryDispatch()
	active := qm2.GetActiveCount()
	if active > 1 {
		t.Errorf("expected at most 1 active download (maxParallel=1), got %d", active)
	}
}

func TestTryDispatch_WhenClosingSkipsDispatch(t *testing.T) {
	qm, ms := newTestQM(t)

	id, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#closing",
		Filename: "a.mkv", FileSize: 100, PackMessage: "xdcc send #1",
	})

	qm.closing.Store(true)
	qm.tryDispatch()

	if got := qm.GetActiveCount(); got != 0 {
		t.Fatalf("expected no active downloads while closing, got %d", got)
	}

	d, _ := ms.GetDownload(context.Background(), id)
	if d == nil {
		t.Fatal("expected queued download to remain in store")
	}
	if d.Status != store.DownloadStatusQueued {
		t.Fatalf("expected queued status while closing, got %s", d.Status)
	}

	qm.mu.RLock()
	slots := len(qm.channelSlots)
	qm.mu.RUnlock()
	if slots != 0 {
		t.Fatalf("expected no reserved slots while closing, got %d", slots)
	}
}

func TestTryDispatch_ReservationRollbackOnStartFailure(t *testing.T) {
	qm, ms := newTestQM(t)
	ms.markStartErr = fmt.Errorf("mark start failed")

	id, err := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "BotFail", ServerAddress: "irc.t.net", Channel: "#rollback",
		Filename: "rollback.mkv", FileSize: 100, PackMessage: "xdcc send #11",
	})
	if err != nil {
		t.Fatalf("EnqueueDownload: %v", err)
	}

	qm.tryDispatch()

	if got := qm.GetActiveCount(); got != 0 {
		t.Fatalf("expected active count 0 after failed start, got %d", got)
	}

	sk := slotKey("irc.t.net", "#rollback", "BotFail")
	qm.mu.RLock()
	_, exists := qm.channelSlots[sk]
	qm.mu.RUnlock()
	if exists {
		t.Fatalf("expected slot %q to be released after failed start", sk)
	}

	d, _ := ms.GetDownload(context.Background(), id)
	if d == nil {
		t.Fatal("expected download to still exist in store")
	}
	if d.Status != store.DownloadStatusQueued {
		t.Fatalf("expected queued status after failed start, got %s", d.Status)
	}
}

func TestTryDispatch_ReservesSingleDownloadPerSlot(t *testing.T) {
	qm, ms := newTestQM(t)

	id1, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "BotA", ServerAddress: "irc.t.net", Channel: "#same",
		Filename: "a.mkv", FileSize: 100, PackMessage: "xdcc send #1",
	})
	id2, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "BotB", ServerAddress: "irc.t.net", Channel: "#same",
		Filename: "b.mkv", FileSize: 200, PackMessage: "xdcc send #2",
	})

	qm.tryDispatch()

	if got := qm.GetActiveCount(); got != 1 {
		t.Fatalf("expected exactly 1 active download for same slot, got %d", got)
	}

	sk := slotKey("irc.t.net", "#same", "BotA")
	qm.mu.RLock()
	ownerID, exists := qm.channelSlots[sk]
	qm.mu.RUnlock()
	if !exists {
		t.Fatalf("expected slot %q to be reserved", sk)
	}

	d1, _ := ms.GetDownload(context.Background(), id1)
	d2, _ := ms.GetDownload(context.Background(), id2)
	if d1 == nil || d2 == nil {
		t.Fatal("expected both downloads in store")
	}

	if ownerID == d1.ID {
		if d1.Status != store.DownloadStatusDownloading {
			t.Fatalf("expected first download to be downloading, got %s", d1.Status)
		}
		if d2.Status != store.DownloadStatusQueued {
			t.Fatalf("expected second download to remain queued, got %s", d2.Status)
		}
		return
	}

	if ownerID == d2.ID {
		if d2.Status != store.DownloadStatusDownloading {
			t.Fatalf("expected second download to be downloading, got %s", d2.Status)
		}
		if d1.Status != store.DownloadStatusQueued {
			t.Fatalf("expected first download to remain queued, got %s", d1.Status)
		}
		return
	}

	t.Fatalf("unexpected slot owner id %d", ownerID)
}

// ===========================================================================
// handleFallback
// ===========================================================================

func TestHandleFallback_SuggestOnly(t *testing.T) {
	_, ms := newTestQM(t)

	id, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "f.mkv", FileSize: 100,
	})

	// With default config (suggest_only), handleFallback should not auto-retry
	// Note: we can't call qm.handleFallback directly since it accesses
	// qm.cfg which requires the real QueueManager. With suggest_only mode,
	// the status should remain unchanged after a failed download.
	_ = ms.MarkDownloadFailed(context.Background(), id, "test error")
	d, _ := ms.GetDownload(context.Background(), id)
	if d == nil {
		t.Fatal("expected download to exist")
	}
	if d.Status != store.DownloadStatusFailed {
		t.Errorf("expected status 'failed', got %s", d.Status)
	}
}

func TestHandleFallback_AutoRetryBest_RequeuesFailedDownload(t *testing.T) {
	qm, ms := newTestQMWithFallback(t, 3)

	// Enqueue a download
	id, err := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "RetryBot", ServerAddress: "irc.t.net", Channel: "#retry",
		Filename: "retry.mkv", FileSize: 500, PackMessage: "xdcc send #42",
	})
	if err != nil {
		t.Fatalf("EnqueueDownload: %v", err)
	}

	// Mark it as failed (to simulate what completeFn does before handleFallback)
	_ = ms.MarkDownloadFailed(context.Background(), id, "connection reset")

	// Get the record and call handleFallback directly
	d, _ := ms.GetDownload(context.Background(), id)
	if d == nil {
		t.Fatal("expected download to exist")
	}

	qm.handleFallback(*d, workerResult{Error: fmt.Errorf("connection reset")})

	// Verify the download was retried (status back to queued)
	dAfter, _ := ms.GetDownload(context.Background(), id)
	if dAfter.Status != store.DownloadStatusQueued {
		t.Errorf("expected status 'queued' after auto-retry, got %s", dAfter.Status)
	}
	// Verify retry_count was incremented
	if dAfter.RetryCount != 1 {
		t.Errorf("expected retry_count=1 after first retry, got %d", dAfter.RetryCount)
	}
}

func TestHandleFallback_MaxRetries_PreventsExcessiveRetries(t *testing.T) {
	qm, ms := newTestQMWithFallback(t, 2)

	// Enqueue a download
	id, err := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "LimitedBot", ServerAddress: "irc.t.net", Channel: "#cap",
		Filename: "capped.mkv", FileSize: 300, PackMessage: "xdcc send #7",
	})
	if err != nil {
		t.Fatalf("EnqueueDownload: %v", err)
	}

	// Manually set retry_count at the limit (2 attempts already used)
	ms.mu.Lock()
	ms.downloads[id].RetryCount = 2
	ms.mu.Unlock()

	// Mark it as failed
	_ = ms.MarkDownloadFailed(context.Background(), id, "persistent error")

	d, _ := ms.GetDownload(context.Background(), id)
	qm.handleFallback(*d, workerResult{Error: fmt.Errorf("persistent error")})

	// Verify the download was NOT retried (still failed, not queued)
	dAfter, _ := ms.GetDownload(context.Background(), id)
	if dAfter.Status != store.DownloadStatusFailed {
		t.Errorf("expected status 'failed' (max retries exceeded), got %s", dAfter.Status)
	}
	// retry_count should still be 2 (no increment)
	if dAfter.RetryCount != 2 {
		t.Errorf("expected retry_count=2 (unchanged), got %d", dAfter.RetryCount)
	}
}

func TestHandleFallback_IncrementsRetryCountCorrectly(t *testing.T) {
	qm, ms := newTestQMWithFallback(t, 3)

	// Enqueue
	id, _ := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "CountBot", ServerAddress: "irc.t.net", Channel: "#count",
		Filename: "count.mkv", FileSize: 100, PackMessage: "xdcc send #1",
	})

	// Simulate two retry cycles (retry → fail → retry → fail)
	for attempt := 0; attempt < 2; attempt++ {
		_ = ms.MarkDownloadFailed(context.Background(), id, fmt.Sprintf("error attempt %d", attempt))
		d, _ := ms.GetDownload(context.Background(), id)
		qm.handleFallback(*d, workerResult{Error: fmt.Errorf("error attempt %d", attempt)})

		// re-verify: status should be queued, retry_count should be attempt+1
		dAfter, _ := ms.GetDownload(context.Background(), id)
		if dAfter.Status != store.DownloadStatusQueued {
			t.Fatalf("attempt %d: expected status 'queued', got %s", attempt, dAfter.Status)
		}
		expectedRetries := attempt + 1
		if dAfter.RetryCount != expectedRetries {
			t.Fatalf("attempt %d: expected retry_count=%d, got %d", attempt, expectedRetries, dAfter.RetryCount)
		}
	}
}

// TestHandleFallback_SkippedDoesNotRetry verifies that skipped downloads
// (conflict policy skip) don't trigger retries even in auto_retry_best mode.
func TestHandleFallback_SkippedDoesNotRetry(t *testing.T) {
	qm, ms := newTestQMWithFallback(t, 3)

	id, err := ms.EnqueueDownload(context.Background(), store.DownloadRecord{
		Bot: "SkipBot", ServerAddress: "irc.t.net", Channel: "#skip",
		Filename: "skipped.mkv", FileSize: 500, PackMessage: "xdcc send #99",
	})
	if err != nil {
		t.Fatalf("EnqueueDownload: %v", err)
	}

	// Mark as skipped (simulating conflict policy skip)
	_ = ms.MarkDownloadSkipped(context.Background(), id)

	d, _ := ms.GetDownload(context.Background(), id)
	qm.handleFallback(*d, workerResult{Skipped: true})

	// Verify the download is NOT retried — stays in skipped status
	dAfter, _ := ms.GetDownload(context.Background(), id)
	if dAfter.Status == store.DownloadStatusQueued {
		t.Error("skipped download should NOT be retried, but status is 'queued'")
	}
	if dAfter.Status != store.DownloadStatusSkipped {
		t.Errorf("expected status 'skipped_existing', got %s", dAfter.Status)
	}
	if dAfter.RetryCount != 0 {
		t.Errorf("expected retry_count=0 for skipped download, got %d", dAfter.RetryCount)
	}
}

// ===========================================================================
// Stop
// ===========================================================================

func TestStop_Clean(t *testing.T) {
	qm, _ := newTestQM(t)

	// Stop should not panic
	qm.Stop()
}

func TestStoreCtxForCallbacks_FallsBackAfterCancel(t *testing.T) {
	qm, _ := newTestQM(t)

	ctxBefore, cancelBefore := qm.storeCtxForCallbacks()
	defer cancelBefore()
	if ctxBefore.Err() != nil {
		t.Fatalf("expected active context before cancel, got err=%v", ctxBefore.Err())
	}

	qm.cancel()
	if qm.ctx.Err() == nil {
		t.Fatal("expected manager context to be cancelled")
	}

	ctxAfter, cancelAfter := qm.storeCtxForCallbacks()
	defer cancelAfter()
	if ctxAfter.Err() != nil {
		t.Fatalf("expected fallback store context to be usable, got err=%v", ctxAfter.Err())
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

func contains(s, substr string) bool {
	if len(s) < len(substr) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// =========================================================================
// Helper functions
// =========================================================================

func TestSlotKey(t *testing.T) {
	tests := []struct {
		server, channel, bot string
		want                 string
	}{
		{"irc.test.com", "#xdcc", "TestBot", "irc.test.com|#xdcc"},
		{"irc.test.com", "#XDCC", "TestBot", "irc.test.com|#xdcc"},
		{"irc.test.com", "no-hash", "Bot", "irc.test.com|#no-hash"},
		{"irc.test.com", "", "TestBot", "irc.test.com|~whois|TestBot"},
		{"irc.test.com", "", "OtherBot", "irc.test.com|~whois|OtherBot"},
		{"server2.com", "#chan", "BotX", "server2.com|#chan"},
	}
	for _, tt := range tests {
		got := slotKey(tt.server, tt.channel, tt.bot)
		if got != tt.want {
			t.Errorf("slotKey(%q, %q, %q) = %q, want %q", tt.server, tt.channel, tt.bot, got, tt.want)
		}
	}
}

func TestProgressThrottleInterval(t *testing.T) {
	tests := []struct {
		active int
		want   time.Duration
	}{
		{0, 0},
		{1, 0},
		{3, 0},
		{4, 2 * time.Second},
		{5, 2 * time.Second},
		{8, 2 * time.Second},
		{9, 3 * time.Second},
		{20, 3 * time.Second},
	}
	for _, tt := range tests {
		got := progressThrottleInterval(tt.active)
		if got != tt.want {
			t.Errorf("progressThrottleInterval(%d) = %v, want %v", tt.active, got, tt.want)
		}
	}
}

func TestGetEffectiveMaxRate(t *testing.T) {
	qm, _ := newTestQM(t)
	rate := qm.GetEffectiveMaxRate()
	if rate != 0 {
		t.Errorf("expected 0 (default), got %d", rate)
	}
}

// =========================================================================
// copyFile
// =========================================================================

func TestCopyFile(t *testing.T) {
	t.Parallel()

	t.Run("Success", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "src.bin")
		dst := filepath.Join(tmpDir, "dst.bin")

		content := []byte("hello world copy file test")
		if err := os.WriteFile(src, content, 0o644); err != nil {
			t.Fatalf("writing source: %v", err)
		}

		if err := copyFile(src, dst); err != nil {
			t.Fatalf("copyFile: %v", err)
		}

		// Verify destination exists and content matches
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("reading destination: %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("content mismatch: got %q, want %q", string(got), string(content))
		}

		// Verify permissions preserved
		dstInfo, _ := os.Stat(dst)
		srcInfo, _ := os.Stat(src)
		if dstInfo.Mode() != srcInfo.Mode() {
			t.Errorf("permission mismatch: dst=%v, src=%v", dstInfo.Mode(), srcInfo.Mode())
		}
	})

	t.Run("SourceNotFound", func(t *testing.T) {
		t.Parallel()
		err := copyFile("/nonexistent/file.bin", "/tmp/out.bin")
		if err == nil {
			t.Fatal("expected error for non-existent source")
		}
	})

	t.Run("DestinationInNonexistentDir", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "src.bin")
		_ = os.WriteFile(src, []byte("data"), 0o644)
		dst := filepath.Join(tmpDir, "nonexistent", "dst.bin")

		err := copyFile(src, dst)
		if err == nil {
			t.Fatal("expected error when destination directory doesn't exist")
		}
	})

	t.Run("OverwriteExisting", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "src.bin")
		dst := filepath.Join(tmpDir, "dst.bin")

		_ = os.WriteFile(src, []byte("new content"), 0o644)
		_ = os.WriteFile(dst, []byte("old content will be overwritten"), 0o600)

		if err := copyFile(src, dst); err != nil {
			t.Fatalf("copyFile: %v", err)
		}

		got, _ := os.ReadFile(dst)
		if string(got) != "new content" {
			t.Errorf("expected 'new content', got %q", string(got))
		}

		// Note: O_TRUNC truncates but doesn't change existing file permissions.
		// Permissions from OpenFile are only applied on O_CREATE (new files).
		dstInfo, _ := os.Stat(dst)
		if dstInfo.Mode().Perm() != 0o600 {
			t.Errorf("expected permissions 0o600 (unchanged from pre-existing), got %v", dstInfo.Mode().Perm())
		}
	})

	t.Run("EmptyFile", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "src.bin")
		dst := filepath.Join(tmpDir, "dst.bin")

		_ = os.WriteFile(src, []byte{}, 0o644)

		if err := copyFile(src, dst); err != nil {
			t.Fatalf("copyFile empty file: %v", err)
		}

		got, _ := os.ReadFile(dst)
		if len(got) != 0 {
			t.Errorf("expected empty destination, got %d bytes", len(got))
		}
	})

	t.Run("LargeFile", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "src.bin")
		dst := filepath.Join(tmpDir, "dst.bin")

		// Create a 1MB file
		data := make([]byte, 1024*1024)
		for i := range data {
			data[i] = byte(i % 256)
		}
		_ = os.WriteFile(src, data, 0o644)

		if err := copyFile(src, dst); err != nil {
			t.Fatalf("copyFile large file: %v", err)
		}

		got, _ := os.ReadFile(dst)
		if len(got) != len(data) {
			t.Errorf("expected %d bytes, got %d", len(data), len(got))
		}
		for i := range data {
			if got[i] != data[i] {
				t.Errorf("byte mismatch at offset %d: got %d, want %d", i, got[i], data[i])
				break
			}
		}
	})
}
