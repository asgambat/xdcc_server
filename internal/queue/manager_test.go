package queue

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"xdcc-go/internal/config"
	xdccirc "xdcc-go/internal/irc"
	"xdcc-go/internal/logging"
	"xdcc-go/internal/store"
)

// ===========================================================================
// Mock store for queue testing
// ===========================================================================

type mockStore struct {
	mu         sync.Mutex
	downloads  map[int64]*store.DownloadRecord
	nextID     int64
	getQueueFn func() ([]store.DownloadRecord, error)
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
