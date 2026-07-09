package notifier

import (
	"bytes"
	"context"
	"strings"
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

func TestFormatWatchlistMessageWithSearchURL(t *testing.T) {
	evt := WatchlistEvent{
		WatchlistName: "my-watchlist",
		NewPacksCount: 5,
		EnqueuedCount: 3,
		SearchURL:     "https://example.com/#search?q=test+query",
	}
	nm := formatWatchlistMessage(evt)
	expected := "Watchlist \"my-watchlist\": 5 nuovi pacchetti (3 in coda)\nCerca: https://example.com/#search?q=test+query"
	if nm.Message != expected {
		t.Errorf("unexpected message:\n got:  %q\n want: %q", nm.Message, expected)
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
		baseURL:   "https://example.com",
	}

	mgr.NotifyWatchlistResults("test-wl", 10, 5, "my query")

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
	if call.Query != "my query" {
		t.Errorf("expected query 'my query', got %q", call.Query)
	}
	if call.SearchURL != "https://example.com/#search?q=my+query" {
		t.Errorf("expected search URL, got %q", call.SearchURL)
	}
}

func TestManagerNotifyWatchlistResultsNoNotifiers(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	mgr := &Manager{
		notifiers: nil,
		logger:    logger,
	}

	mgr.NotifyWatchlistResults("test", 1, 0, "") // should not panic
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
	mgr := NewManager(nil, "", logger)
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
	mgr := NewManager(cfgs, "", logger)
	if len(mgr.Notifiers()) != 0 {
		t.Errorf("expected 0 notifiers for disabled provider, got %d", len(mgr.Notifiers()))
	}
}

// ---------------------------------------------------------------------------
// Startup warning: empty base_url with watchlist provider configured
// ---------------------------------------------------------------------------

func TestNewManagerWarnsWhenBaseURLEmptyAndWatchlistEnabled(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.LevelDebug, "", 0)
	logger.AddWriter(&buf)

	cfgs := []config.NotificationConfig{
		{
			Type:         "ntfy",
			NtfyEndpoint: "https://ntfy.sh/test",
			Events:       []string{"watchlist_new_results"},
		},
	}
	NewManager(cfgs, "", logger)

	if !strings.Contains(buf.String(), "http.base_url is empty") {
		t.Errorf("expected startup warning about empty base_url when watchlist provider is configured; got:\n%s", buf.String())
	}
}

func TestNewManagerNoWarningWhenBaseURLIsSetAndWatchlistEnabled(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.LevelDebug, "", 0)
	logger.AddWriter(&buf)

	cfgs := []config.NotificationConfig{
		{
			Type:         "ntfy",
			NtfyEndpoint: "https://ntfy.sh/test",
			Events:       []string{"watchlist_new_results"},
		},
	}
	NewManager(cfgs, "https://example.com", logger)

	if strings.Contains(buf.String(), "http.base_url is empty") {
		t.Errorf("did not expect empty-base_url warning when baseURL is set; got:\n%s", buf.String())
	}
}

func TestNewManagerNoWarningWhenNoWatchlistProvider(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.LevelDebug, "", 0)
	logger.AddWriter(&buf)

	cfgs := []config.NotificationConfig{
		{
			Type:            "webhook",
			WebhookEndpoint: "https://example.com/wh",
			Events:          []string{"download_completed", "download_failed"},
		},
	}
	NewManager(cfgs, "", logger)

	if strings.Contains(buf.String(), "http.base_url is empty") {
		t.Errorf("did not expect empty-base_url warning when no watchlist provider configured; got:\n%s", buf.String())
	}
}

func TestNewManagerWarnsWhenEmptyEventsListAndBaseURLEmpty(t *testing.T) {
	// Empty events list means "all events" (including watchlist_new_results).
	// If base_url is empty, the operator should still be warned that watchlist
	// links won't be included.
	var buf bytes.Buffer
	logger := logging.New(logging.LevelDebug, "", 0)
	logger.AddWriter(&buf)

	cfgs := []config.NotificationConfig{
		{
			Type:         "ntfy",
			NtfyEndpoint: "https://ntfy.sh/test",
			// Events omitted → all events
		},
	}
	NewManager(cfgs, "", logger)

	if !strings.Contains(buf.String(), "http.base_url is empty") {
		t.Errorf("expected startup warning when empty events list implies watchlist coverage; got:\n%s", buf.String())
	}
}

func TestNewManagerNoWarningWhenNotifierConstructionFails(t *testing.T) {
	// Regression: if the notifier constructor returns nil (e.g. empty endpoint),
	// the operator should NOT see a misleading base_url warning, since no provider
	// is actually configured to send anything.
	var buf bytes.Buffer
	logger := logging.New(logging.LevelDebug, "", 0)
	logger.AddWriter(&buf)

	cfgs := []config.NotificationConfig{
		{
			Type:         "ntfy",
			NtfyEndpoint: "", // empty → NewNtfyNotifier returns nil
			Events:       []string{"watchlist_new_results"},
		},
		{
			Type:            "webhook",
			WebhookEndpoint: "", // empty → NewWebhookNotifier returns nil
			Events:          []string{"watchlist_new_results"},
		},
	}
	mgr := NewManager(cfgs, "", logger)

	if len(mgr.Notifiers()) != 0 {
		t.Errorf("expected 0 notifiers (constructors returned nil); got %d", len(mgr.Notifiers()))
	}
	if strings.Contains(buf.String(), "http.base_url is empty") {
		t.Errorf("did not expect base_url warning when no notifier was constructed; got:\n%s", buf.String())
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

// ---------------------------------------------------------------------------
// NewWebhookNotifier
// ---------------------------------------------------------------------------

func TestNewWebhookNotifier_Valid(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:            "webhook",
		WebhookEndpoint: "https://hooks.example.com/xdcc",
		WebhookToken:    "BearerSecret",
		Events:          []string{"download_completed"},
	}
	w := NewWebhookNotifier(cfg)
	if w == nil {
		t.Fatal("expected non-nil WebhookNotifier")
	}
	if w.endpoint != "https://hooks.example.com/xdcc" {
		t.Errorf("expected endpoint, got %q", w.endpoint)
	}
	if w.token != "BearerSecret" {
		t.Errorf("expected token, got %q", w.token)
	}
	if len(w.events) != 1 {
		t.Errorf("expected 1 event, got %d", len(w.events))
	}
}

func TestNewWebhookNotifier_WrongType(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:            "ntfy",
		WebhookEndpoint: "https://hooks.example.com",
	}
	w := NewWebhookNotifier(cfg)
	if w != nil {
		t.Fatal("expected nil for wrong type")
	}
}

func TestNewWebhookNotifier_EmptyEndpoint(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:            "webhook",
		WebhookEndpoint: "",
	}
	w := NewWebhookNotifier(cfg)
	if w != nil {
		t.Fatal("expected nil for empty endpoint")
	}
}

func TestNewWebhookNotifier_NoToken(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:            "webhook",
		WebhookEndpoint: "https://hooks.example.com",
	}
	w := NewWebhookNotifier(cfg)
	if w == nil {
		t.Fatal("expected non-nil WebhookNotifier even without token")
	}
	if w.token != "" {
		t.Errorf("expected empty token, got %q", w.token)
	}
}

// ---------------------------------------------------------------------------
// NewNtfyNotifier
// ---------------------------------------------------------------------------

func TestNewNtfyNotifier_Valid(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:         "ntfy",
		NtfyEndpoint: "https://ntfy.sh/mytopic",
		NtfyToken:    "tk_abc123",
		Events:       []string{"download_completed", "download_failed"},
	}
	n := NewNtfyNotifier(cfg)
	if n == nil {
		t.Fatal("expected non-nil NtfyNotifier")
	}
	if n.endpoint != "https://ntfy.sh/mytopic" {
		t.Errorf("expected endpoint, got %q", n.endpoint)
	}
	if n.token != "tk_abc123" {
		t.Errorf("expected token, got %q", n.token)
	}
	if len(n.events) != 2 {
		t.Errorf("expected 2 events, got %d", len(n.events))
	}
}

func TestNewNtfyNotifier_WrongType(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:         "webhook",
		NtfyEndpoint: "https://ntfy.sh/topic",
	}
	n := NewNtfyNotifier(cfg)
	if n != nil {
		t.Fatal("expected nil for wrong type")
	}
}

func TestNewNtfyNotifier_EmptyEndpoint(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:         "ntfy",
		NtfyEndpoint: "",
	}
	n := NewNtfyNotifier(cfg)
	if n != nil {
		t.Fatal("expected nil for empty endpoint")
	}
}

// ---------------------------------------------------------------------------
// NewPushoverNotifier
// ---------------------------------------------------------------------------

func TestNewPushoverNotifier_Valid(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:          "pushover",
		PushoverToken: "abc123",
		PushoverUser:  "userkey",
		Events:        []string{"download_completed"},
	}
	p := NewPushoverNotifier(cfg)
	if p == nil {
		t.Fatal("expected non-nil PushoverNotifier")
	}
	if p.token != "abc123" {
		t.Errorf("expected token, got %q", p.token)
	}
	if p.user != "userkey" {
		t.Errorf("expected user, got %q", p.user)
	}
	if len(p.events) != 1 {
		t.Errorf("expected 1 event, got %d", len(p.events))
	}
}

func TestNewPushoverNotifier_WrongType(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:          "webhook",
		PushoverToken: "abc",
		PushoverUser:  "user",
	}
	p := NewPushoverNotifier(cfg)
	if p != nil {
		t.Fatal("expected nil for wrong type")
	}
}

func TestNewPushoverNotifier_MissingToken(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:         "pushover",
		PushoverUser: "userkey",
	}
	p := NewPushoverNotifier(cfg)
	if p != nil {
		t.Fatal("expected nil when token is empty")
	}
}

func TestNewPushoverNotifier_MissingUser(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:          "pushover",
		PushoverToken: "abc123",
	}
	p := NewPushoverNotifier(cfg)
	if p != nil {
		t.Fatal("expected nil when user is empty")
	}
}

func TestNewPushoverNotifier_DefaultEndpoint(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:          "pushover",
		PushoverToken: "abc",
		PushoverUser:  "user",
		// PushoverEndpoint not set → should default to api.pushover.net
	}
	p := NewPushoverNotifier(cfg)
	if p == nil {
		t.Fatal("expected non-nil PushoverNotifier")
	}
	if p.endpoint != "https://api.pushover.net/1/messages.json" {
		t.Errorf("expected default Pushover endpoint, got %q", p.endpoint)
	}
}

func TestNewPushoverNotifier_CustomEndpoint(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:             "pushover",
		PushoverToken:    "abc",
		PushoverUser:     "user",
		PushoverEndpoint: "https://custom.pushover.example.com",
	}
	p := NewPushoverNotifier(cfg)
	if p == nil {
		t.Fatal("expected non-nil PushoverNotifier")
	}
	if p.endpoint != "https://custom.pushover.example.com" {
		t.Errorf("expected custom endpoint, got %q", p.endpoint)
	}
}

// ---------------------------------------------------------------------------
// NewEmailNotifier
// ---------------------------------------------------------------------------

func TestNewEmailNotifier_Valid(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:     "email",
		SMTPHost: "smtp.example.com",
		SMTPPort: 587,
		SMTPFrom: "bot@example.com",
		SMTPTo:   "admin@example.com",
		Events:   []string{"download_completed"},
	}
	e := NewEmailNotifier(cfg)
	if e == nil {
		t.Fatal("expected non-nil EmailNotifier")
	}
	if e.host != "smtp.example.com" {
		t.Errorf("expected host, got %q", e.host)
	}
	if e.port != 587 {
		t.Errorf("expected port 587, got %d", e.port)
	}
	if e.from != "bot@example.com" {
		t.Errorf("expected from, got %q", e.from)
	}
	if len(e.to) != 1 || e.to[0] != "admin@example.com" {
		t.Errorf("expected to=[admin@example.com], got %v", e.to)
	}
	if len(e.events) != 1 {
		t.Errorf("expected 1 event, got %d", len(e.events))
	}
}

func TestNewEmailNotifier_WrongType(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:     "webhook",
		SMTPHost: "smtp.example.com",
		SMTPFrom: "bot@example.com",
		SMTPTo:   "admin@example.com",
	}
	e := NewEmailNotifier(cfg)
	if e != nil {
		t.Fatal("expected nil for wrong type")
	}
}

func TestNewEmailNotifier_MissingHost(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:     "email",
		SMTPFrom: "bot@example.com",
		SMTPTo:   "admin@example.com",
	}
	e := NewEmailNotifier(cfg)
	if e != nil {
		t.Fatal("expected nil when host is empty")
	}
}

func TestNewEmailNotifier_MissingFrom(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:     "email",
		SMTPHost: "smtp.example.com",
		SMTPTo:   "admin@example.com",
	}
	e := NewEmailNotifier(cfg)
	if e != nil {
		t.Fatal("expected nil when from is empty")
	}
}

func TestNewEmailNotifier_MissingTo(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:     "email",
		SMTPHost: "smtp.example.com",
		SMTPFrom: "bot@example.com",
	}
	e := NewEmailNotifier(cfg)
	if e != nil {
		t.Fatal("expected nil when to is empty")
	}
}

func TestNewEmailNotifier_DefaultPort(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:     "email",
		SMTPHost: "smtp.example.com",
		SMTPFrom: "bot@example.com",
		SMTPTo:   "admin@example.com",
		// SMTPPort not set
	}
	e := NewEmailNotifier(cfg)
	if e == nil {
		t.Fatal("expected non-nil EmailNotifier")
	}
	if e.port != 587 {
		t.Errorf("expected default port 587, got %d", e.port)
	}
}

func TestNewEmailNotifier_DefaultTLSMode(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:     "email",
		SMTPHost: "smtp.example.com",
		SMTPFrom: "bot@example.com",
		SMTPTo:   "admin@example.com",
		// SMTPTLS not set
	}
	e := NewEmailNotifier(cfg)
	if e == nil {
		t.Fatal("expected non-nil EmailNotifier")
	}
	if e.tlsMode != "starttls" {
		t.Errorf("expected default tlsMode 'starttls', got %q", e.tlsMode)
	}
}

func TestNewEmailNotifier_RecipientParsing(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:     "email",
		SMTPHost: "smtp.example.com",
		SMTPFrom: "bot@example.com",
		SMTPTo:   "admin@example.com, , user@example.com,",
	}
	e := NewEmailNotifier(cfg)
	if e == nil {
		t.Fatal("expected non-nil EmailNotifier")
	}
	if len(e.to) != 2 {
		t.Errorf("expected 2 parsed recipients (blank trimmed), got %d: %v", len(e.to), e.to)
	}
}

func TestNewEmailNotifier_WithUsername(t *testing.T) {
	cfg := config.NotificationConfig{
		Type:         "email",
		SMTPHost:     "smtp.example.com",
		SMTPPort:     465,
		SMTPUsername: "user",
		SMTPPassword: "pass",
		SMTPFrom:     "bot@example.com",
		SMTPTo:       "admin@example.com",
		SMTPTLS:      "ssl",
	}
	e := NewEmailNotifier(cfg)
	if e == nil {
		t.Fatal("expected non-nil EmailNotifier")
	}
	if e.username != "user" {
		t.Errorf("expected username 'user', got %q", e.username)
	}
	if e.password != "pass" {
		t.Errorf("expected password 'pass', got %q", e.password)
	}
	if e.port != 465 {
		t.Errorf("expected port 465, got %d", e.port)
	}
	if e.tlsMode != "ssl" {
		t.Errorf("expected tlsMode 'ssl', got %q", e.tlsMode)
	}
}

// ---------------------------------------------------------------------------
// smtpAuth
// ---------------------------------------------------------------------------

func TestSmtpAuth_NoUsername(t *testing.T) {
	e := &EmailNotifier{username: ""}
	auth := e.smtpAuth()
	if auth != nil {
		t.Fatal("expected nil auth when no username")
	}
}

func TestSmtpAuth_StartTLS(t *testing.T) {
	e := &EmailNotifier{
		username: "user",
		password: "pass",
		host:     "smtp.example.com",
		tlsMode:  "starttls",
	}
	auth := e.smtpAuth()
	if auth == nil {
		t.Fatal("expected non-nil auth for starttls")
	}
}

func TestSmtpAuth_NonStartTLS(t *testing.T) {
	// SSL mode uses allowAnyTLS instead of PlainAuth
	e := &EmailNotifier{
		username: "user",
		password: "pass",
		host:     "smtp.example.com",
		tlsMode:  "ssl",
	}
	auth := e.smtpAuth()
	if auth == nil {
		t.Fatal("expected non-nil auth for ssl mode")
	}
	// Should be allowAnyTLS, not smtp.PlainAuth
	_, isAllowAny := auth.(*allowAnyTLS)
	if !isAllowAny {
		t.Errorf("expected *allowAnyTLS for non-starttls mode, got %T", auth)
	}
}
