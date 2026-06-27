package notifier

import (
	"context"
	"sync"
	"testing"

	"xdcc_server/internal/config"
	"xdcc_server/internal/logging"
	"xdcc_server/internal/queue"
)

// ---------------------------------------------------------------------------
// eventsFilter
// ---------------------------------------------------------------------------

func TestEventsFilter(t *testing.T) {
	f := eventsFilter([]string{"download_completed", "download_failed"})
	if len(f) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(f))
	}
	if !f[EventDownloadCompleted] {
		t.Error("expected download_completed to be in filter")
	}
	if !f[EventDownloadFailed] {
		t.Error("expected download_failed to be in filter")
	}
	if f[EventWatchlistNewResults] {
		t.Error("expected watchlist_new_results NOT to be in filter")
	}
}

func TestEventsFilterEmpty(t *testing.T) {
	f := eventsFilter([]string{})
	if len(f) != 0 {
		t.Fatalf("expected empty filter, got %d entries", len(f))
	}
}

// ---------------------------------------------------------------------------
// interested
// ---------------------------------------------------------------------------

func TestInterestedMatch(t *testing.T) {
	f := eventsFilter([]string{"download_completed"})
	if !interested(f, EventDownloadCompleted) {
		t.Error("expected interested=true for matching event")
	}
}

func TestInterestedNoMatch(t *testing.T) {
	f := eventsFilter([]string{"download_completed"})
	if interested(f, EventDownloadFailed) {
		t.Error("expected interested=false for non-matching event")
	}
}

func TestInterestedEmptyFilter(t *testing.T) {
	// Empty filter means "all events"
	f := eventsFilter([]string{})
	if !interested(f, EventDownloadCompleted) {
		t.Error("empty filter should match all events")
	}
	if !interested(f, EventWatchlistNewResults) {
		t.Error("empty filter should match all events")
	}
}

// ---------------------------------------------------------------------------
// formatDownloadMessage
// ---------------------------------------------------------------------------

func TestFormatDownloadMessageCompleted(t *testing.T) {
	evt := queue.Event{
		Type:     queue.EventDownloadCompleted,
		Filename: "test-file.mkv",
	}
	nm := formatDownloadMessage(evt)
	if nm.Title != "Download completato" {
		t.Errorf("unexpected title: %q", nm.Title)
	}
	if nm.Message != "✓ test-file.mkv completato" {
		t.Errorf("unexpected message: %q", nm.Message)
	}
}

func TestFormatDownloadMessageFailed(t *testing.T) {
	evt := queue.Event{
		Type:         queue.EventDownloadFailed,
		Filename:     "test-file.mkv",
		ErrorMessage: "connection refused",
	}
	nm := formatDownloadMessage(evt)
	if nm.Title != "Download fallito" {
		t.Errorf("unexpected title: %q", nm.Title)
	}
	expected := "✗ test-file.mkv fallito: connection refused"
	if nm.Message != expected {
		t.Errorf("unexpected message:\n got:  %q\n want: %q", nm.Message, expected)
	}
}

func TestFormatDownloadMessageFailedNoError(t *testing.T) {
	evt := queue.Event{
		Type:     queue.EventDownloadFailed,
		Filename: "test-file.mkv",
	}
	nm := formatDownloadMessage(evt)
	if nm.Message != "✗ test-file.mkv fallito" {
		t.Errorf("unexpected message: %q", nm.Message)
	}
}

func TestFormatDownloadMessageUnknown(t *testing.T) {
	evt := queue.Event{
		Type:     "unknown_event",
		Filename: "test-file.mkv",
	}
	nm := formatDownloadMessage(evt)
	if nm.Title != "unknown_event" {
		t.Errorf("unexpected title: %q", nm.Title)
	}
}

// ---------------------------------------------------------------------------
// formatWatchlistMessage
// ---------------------------------------------------------------------------

func TestFormatWatchlistMessageNoEnqueue(t *testing.T) {
	evt := WatchlistEvent{
		WatchlistName: "my-watchlist",
		NewPacksCount: 5,
		EnqueuedCount: 0,
	}
	nm := formatWatchlistMessage(evt)
	if nm.Title != "Watchlist aggiornata" {
		t.Errorf("unexpected title: %q", nm.Title)
	}
	if nm.Message != `Watchlist "my-watchlist": 5 nuovi pacchetti` {
		t.Errorf("unexpected message: %q", nm.Message)
	}
}

func TestFormatWatchlistMessageWithEnqueue(t *testing.T) {
	evt := WatchlistEvent{
		WatchlistName: "my-watchlist",
		NewPacksCount: 5,
		EnqueuedCount: 3,
	}
	nm := formatWatchlistMessage(evt)
	expected := `Watchlist "my-watchlist": 5 nuovi pacchetti (3 in coda)`
	if nm.Message != expected {
		t.Errorf("unexpected message: %q", nm.Message)
	}
}

// ---------------------------------------------------------------------------
// mapQueueEvent
// ---------------------------------------------------------------------------

func TestMapQueueEventCompleted(t *testing.T) {
	if mapQueueEvent(queue.EventDownloadCompleted) != EventDownloadCompleted {
		t.Error("expected download_completed")
	}
}

func TestMapQueueEventFailed(t *testing.T) {
	if mapQueueEvent(queue.EventDownloadFailed) != EventDownloadFailed {
		t.Error("expected download_failed")
	}
}

func TestMapQueueEventUnknown(t *testing.T) {
	if mapQueueEvent("unknown") != "" {
		t.Error("expected empty string for unknown event")
	}
}

// ---------------------------------------------------------------------------
// isEnabled
// ---------------------------------------------------------------------------

func TestIsEnabledNil(t *testing.T) {
	if !isEnabled(&config.NotificationConfig{}) {
		t.Error("nil Enabled should default to true")
	}
}

func TestIsEnabledTrue(t *testing.T) {
	enabled := true
	if !isEnabled(&config.NotificationConfig{Enabled: &enabled}) {
		t.Error("Enabled=true should be true")
	}
}

func TestIsEnabledFalse(t *testing.T) {
	enabled := false
	if isEnabled(&config.NotificationConfig{Enabled: &enabled}) {
		t.Error("Enabled=false should be false")
	}
}

// ---------------------------------------------------------------------------
// mockNotifier for dispatch tests
// ---------------------------------------------------------------------------

type mockNotifier struct {
	mu             sync.Mutex
	notifyCalls    []queue.Event
	watchlistCalls []WatchlistEvent
	notifyErr      error
	watchlistErr   error
}

func (m *mockNotifier) Notify(evt queue.Event) error {
	m.mu.Lock()
	m.notifyCalls = append(m.notifyCalls, evt)
	m.mu.Unlock()
	return m.notifyErr
}

func (m *mockNotifier) NotifyWatchlistResults(evt WatchlistEvent) error {
	m.mu.Lock()
	m.watchlistCalls = append(m.watchlistCalls, evt)
	m.mu.Unlock()
	return m.watchlistErr
}

func (m *mockNotifier) notifyCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.notifyCalls)
}

func (m *mockNotifier) watchlistCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.watchlistCalls)
}

// ---------------------------------------------------------------------------
// Manager dispatch tests
// ---------------------------------------------------------------------------

func TestManagerDispatch(t *testing.T) {
	mock := &mockNotifier{}
	logger := logging.New(logging.LevelDebug, "", 0)
	mgr := &Manager{
		notifiers: []Notifier{mock},
		logger:    logger,
	}

	evt := queue.Event{
		Type:       queue.EventDownloadCompleted,
		DownloadID: 42,
		Filename:   "test.mkv",
	}
	mgr.dispatch(evt)

	if mock.notifyCount() != 1 {
		t.Fatalf("expected 1 notify call, got %d", mock.notifyCount())
	}
	if mock.notifyCalls[0].DownloadID != 42 {
		t.Errorf("expected DownloadID=42, got %d", mock.notifyCalls[0].DownloadID)
	}
}

func TestManagerDispatchMultipleNotifiers(t *testing.T) {
	m1 := &mockNotifier{}
	m2 := &mockNotifier{}
	logger := logging.New(logging.LevelDebug, "", 0)
	mgr := &Manager{
		notifiers: []Notifier{m1, m2},
		logger:    logger,
	}

	evt := queue.Event{
		Type:       queue.EventDownloadFailed,
		DownloadID: 99,
		Filename:   "fail.mkv",
	}
	mgr.dispatch(evt)

	if m1.notifyCount() != 1 {
		t.Errorf("m1: expected 1 call, got %d", m1.notifyCount())
	}
	if m2.notifyCount() != 1 {
		t.Errorf("m2: expected 1 call, got %d", m2.notifyCount())
	}
}

func TestManagerDispatchNoNotifiers(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	mgr := &Manager{
		notifiers: nil,
		logger:    logger,
	}

	evt := queue.Event{Type: queue.EventDownloadCompleted}
	mgr.dispatch(evt) // should not panic
}

func TestManagerNotifyWatchlistResults(t *testing.T) {
	mock := &mockNotifier{}
	logger := logging.New(logging.LevelDebug, "", 0)
	mgr := &Manager{
		notifiers: []Notifier{mock},
		logger:    logger,
	}

	mgr.NotifyWatchlistResults("test-wl", 10, 5)

	if mock.watchlistCount() != 1 {
		t.Fatalf("expected 1 watchlist call, got %d", mock.watchlistCount())
	}
	call := mock.watchlistCalls[0]
	if call.WatchlistName != "test-wl" {
		t.Errorf("expected name 'test-wl', got %q", call.WatchlistName)
	}
	if call.NewPacksCount != 10 {
		t.Errorf("expected 10 new packs, got %d", call.NewPacksCount)
	}
	if call.EnqueuedCount != 5 {
		t.Errorf("expected 5 enqueued, got %d", call.EnqueuedCount)
	}
	if call.Timestamp == "" {
		t.Error("expected non-empty timestamp")
	}
}

func TestManagerNotifyWatchlistResultsNoNotifiers(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	mgr := &Manager{
		notifiers: nil,
		logger:    logger,
	}

	mgr.NotifyWatchlistResults("test", 1, 0) // should not panic
}

func TestManagerNotifiers(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	mgr := &Manager{
		notifiers: []Notifier{&mockNotifier{}},
		logger:    logger,
	}

	result := mgr.Notifiers()
	if len(result) != 1 {
		t.Fatalf("expected 1 notifier, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// NewManager integration test (basic smoke test)
// ---------------------------------------------------------------------------

func TestNewManagerEmptyConfig(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	mgr := NewManager(nil, logger)
	if mgr == nil {
		t.Fatal("NewManager returned nil")
	}
	if len(mgr.Notifiers()) != 0 {
		t.Errorf("expected 0 notifiers, got %d", len(mgr.Notifiers()))
	}
}

func TestNewManagerDisabledProvider(t *testing.T) {
	disabled := false
	cfgs := []config.NotificationConfig{
		{
			Type:    "webhook",
			Enabled: &disabled,
		},
	}
	logger := logging.New(logging.LevelDebug, "", 0)
	mgr := NewManager(cfgs, logger)
	if len(mgr.Notifiers()) != 0 {
		t.Errorf("expected 0 notifiers for disabled provider, got %d", len(mgr.Notifiers()))
	}
}

// ---------------------------------------------------------------------------
// Run drain events test (smoke test: drains without blocking)
// ---------------------------------------------------------------------------

func TestDrainEventsEmpty(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	mgr := &Manager{logger: logger}
	ch := make(chan queue.Event)
	close(ch)

	// Should drain without blocking
	mgr.drainEvents(ch)
}

func TestDrainEventsWithEvents(t *testing.T) {
	mock := &mockNotifier{}
	logger := logging.New(logging.LevelDebug, "", 0)
	mgr := &Manager{
		notifiers: []Notifier{mock},
		logger:    logger,
	}

	ch := make(chan queue.Event, 3)
	ch <- queue.Event{Type: queue.EventDownloadCompleted, DownloadID: 1}
	ch <- queue.Event{Type: queue.EventDownloadCompleted, DownloadID: 2}
	ch <- queue.Event{Type: queue.EventDownloadCompleted, DownloadID: 3}
	close(ch)

	mgr.drainEvents(ch)

	if mock.notifyCount() != 3 {
		t.Errorf("expected 3 notify calls, got %d", mock.notifyCount())
	}
}

// ---------------------------------------------------------------------------
// Run with context cancellation
// ---------------------------------------------------------------------------

func TestRunContextCancellation(t *testing.T) {
	mock := &mockNotifier{}
	logger := logging.New(logging.LevelDebug, "", 0)
	mgr := &Manager{
		notifiers: []Notifier{mock},
		logger:    logger,
	}

	eventCh := make(chan queue.Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	mgr.Run(ctx, eventCh, &wg)
	wg.Wait()

	// Should exit cleanly without panicking
}
