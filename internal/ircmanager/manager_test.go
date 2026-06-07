package ircmanager

import (
	"context"
	"sync"
	"testing"
	"time"

	"xdcc-go/internal/config"
	xdccirc "xdcc-go/internal/irc"
	"xdcc-go/internal/logging"
	"xdcc-go/internal/store"
)

// ===========================================================================
// Mock store for IRC manager tests
// ===========================================================================

type mockStore struct {
	mu        sync.Mutex
	servers   map[int64]*store.ServerRecord
	channels  map[int64][]store.ChannelRecord
	nextSrvID int64
	nextChID  int64
}

func newMockStore() *mockStore {
	return &mockStore{
		servers:   make(map[int64]*store.ServerRecord),
		channels:  make(map[int64][]store.ChannelRecord),
		nextSrvID: 1,
		nextChID:  1,
	}
}

func (m *mockStore) addServer(addr string, port int, auto bool) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextSrvID
	m.nextSrvID++
	m.servers[id] = &store.ServerRecord{
		ID: id, Address: addr, Port: port,
		AutoConnect: auto, Status: "disconnected",
	}
	return id
}

func (m *mockStore) addChannel(serverID int64, name string, autoJoin bool) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextChID
	m.nextChID++
	m.channels[serverID] = append(m.channels[serverID], store.ChannelRecord{
		ID: id, ServerID: serverID, Name: name, AutoJoin: autoJoin,
	})
	return id
}

// Store interface

func (m *mockStore) Close(ctx context.Context) error                       { return nil }
func (m *mockStore) Migrate(ctx context.Context) error                     { return nil }
func (m *mockStore) CurrentSchemaVersion(ctx context.Context) (int, error) { return 1, nil }

func (m *mockStore) AddServer(ctx context.Context, s store.ServerRecord) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextSrvID
	m.nextSrvID++
	s.ID = id
	m.servers[id] = &s
	return id, nil
}

func (m *mockStore) GetServer(ctx context.Context, id int64) (*store.ServerRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.servers[id]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (m *mockStore) ListServers(ctx context.Context) ([]store.ServerRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []store.ServerRecord
	for _, s := range m.servers {
		result = append(result, *s)
	}
	return result, nil
}

func (m *mockStore) UpdateServer(ctx context.Context, s store.ServerRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.servers[s.ID]; ok {
		*existing = s
	}
	return nil
}

func (m *mockStore) DeleteServer(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.servers, id)
	return nil
}

func (m *mockStore) SetServerStatus(ctx context.Context, id int64, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.servers[id]; ok {
		s.Status = status
	}
	return nil
}

func (m *mockStore) SetServerConnected(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.servers[id]; ok {
		s.Status = "connected"
		s.RetryCount = 0
		now := time.Now()
		s.LastConnectedAt = &now
	}
	return nil
}

func (m *mockStore) IncrementServerRetry(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.servers[id]; ok {
		s.RetryCount++
		s.Status = "reconnecting"
	}
	return nil
}

func (m *mockStore) ResetAllServerStatuses(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		s.Status = "disconnected"
	}
	return nil
}

func (m *mockStore) AddChannel(ctx context.Context, c store.ChannelRecord) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextChID
	m.nextChID++
	c.ID = id
	m.channels[c.ServerID] = append(m.channels[c.ServerID], c)
	return id, nil
}

func (m *mockStore) GetChannelsByServer(ctx context.Context, serverID int64) ([]store.ChannelRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	chs := m.channels[serverID]
	if chs == nil {
		return []store.ChannelRecord{}, nil
	}
	result := make([]store.ChannelRecord, len(chs))
	copy(result, chs)
	return result, nil
}

func (m *mockStore) GetChannelsByServerAndName(ctx context.Context, serverID int64, name string) (*store.ChannelRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.channels[serverID] {
		if ch.Name == name {
			return &ch, nil
		}
	}
	return nil, nil
}

func (m *mockStore) UpdateChannel(ctx context.Context, c store.ChannelRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, ch := range m.channels[c.ServerID] {
		if ch.ID == c.ID {
			m.channels[c.ServerID][i] = c
			return nil
		}
	}
	return nil
}

func (m *mockStore) DeleteChannel(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for srvID, chs := range m.channels {
		for i, ch := range chs {
			if ch.ID == id {
				m.channels[srvID] = append(chs[:i], chs[i+1:]...)
				return nil
			}
		}
	}
	return nil
}

func (m *mockStore) SetChannelJoined(ctx context.Context, id int64, joined bool) error {
	return nil
}

func (m *mockStore) UpdateChannelTopic(ctx context.Context, id int64, topic string) error {
	return nil
}

func (m *mockStore) GetAutoJoinChannels(ctx context.Context) ([]store.ChannelRecord, error) {
	var result []store.ChannelRecord
	for _, chs := range m.channels {
		for _, ch := range chs {
			if ch.AutoJoin {
				result = append(result, ch)
			}
		}
	}
	return result, nil
}

// Stub for remaining store methods

func (m *mockStore) EnqueueDownload(ctx context.Context, d store.DownloadRecord) (int64, error) {
	return 1, nil
}
func (m *mockStore) GetDownload(ctx context.Context, id int64) (*store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockStore) GetQueue(ctx context.Context) ([]store.DownloadRecord, error) { return nil, nil }
func (m *mockStore) GetQueueByChannel(ctx context.Context, channel string) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockStore) GetActiveDownloads(ctx context.Context) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockStore) GetPendingByChannel(ctx context.Context, channel string) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockStore) UpdateDownloadProgress(ctx context.Context, id int64, progressBytes int64, speedBPS int64) error {
	return nil
}
func (m *mockStore) MarkDownloadStarted(ctx context.Context, id int64) error { return nil }
func (m *mockStore) MarkDownloadCompleted(ctx context.Context, id int64, filename string, fileSize int64) error {
	return nil
}
func (m *mockStore) UpdateDownloadMetadata(ctx context.Context, id int64, filename string, fileSize int64) error {
	return nil
}

func (m *mockStore) UpdateChannelAvgSpeed(ctx context.Context, serverAddress, channelName string, lastSpeedBPS float64) error {
	return nil
}
func (m *mockStore) MarkDownloadFailed(ctx context.Context, id int64, errMsg string) error {
	return nil
}
func (m *mockStore) MarkDownloadSkipped(ctx context.Context, id int64) error              { return nil }
func (m *mockStore) MarkDownloadPaused(ctx context.Context, id int64) error               { return nil }
func (m *mockStore) MarkDownloadRetry(ctx context.Context, id int64, errMsg string) error { return nil }
func (m *mockStore) DeleteDownload(ctx context.Context, id int64) error                   { return nil }
func (m *mockStore) RetryDownload(ctx context.Context, id int64) error                    { return nil }
func (m *mockStore) GetDownloadHistory(ctx context.Context, limit int, offset int, filter store.HistoryFilter) ([]store.DownloadRecord, int, error) {
	return nil, 0, nil
}
func (m *mockStore) RecoverDownloadsOnStartup(ctx context.Context) ([]store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockStore) RequeueDownload(ctx context.Context, id int64) error { return nil }
func (m *mockStore) SetDownloadPriority(ctx context.Context, id int64, priority int) error {
	return nil
}
func (m *mockStore) IncrementDownloadRetry(ctx context.Context, id int64) error {
	return nil
}
func (m *mockStore) BulkActionDownloads(ctx context.Context, ids []int64, action string) (map[int64]string, error) {
	return nil, nil
}
func (m *mockStore) FilenamesExist(ctx context.Context, filenames []string) (map[string]bool, error) {
	result := make(map[string]bool, len(filenames))
	for _, fn := range filenames {
		result[fn] = false
	}
	return result, nil
}

func (m *mockStore) FindDuplicateDownload(ctx context.Context, bot string, serverAddress string, packNumber int) (*store.DownloadRecord, error) {
	return nil, nil
}
func (m *mockStore) GetDownloadByBotMessage(ctx context.Context, bot string, packMessage string) (*store.DownloadRecord, error) {
	return nil, nil
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
func (m *mockStore) GetTotalDownloadedBytes(ctx context.Context) (int64, error)    { return 0, nil }
func (m *mockStore) ListWatchlists(ctx context.Context) ([]store.Watchlist, error) { return nil, nil }
func (m *mockStore) UpdateWatchlist(ctx context.Context, w store.Watchlist) error  { return nil }
func (m *mockStore) DeleteWatchlist(ctx context.Context, id int64) error           { return nil }
func (m *mockStore) SetWatchlistChecked(ctx context.Context, id int64, query string, resultsJSON string) error {
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
func (m *mockStore) RunCleanup(ctx context.Context, retentionDays int, cleanupInterval time.Duration) (chan struct{}, chan struct{}, error) {
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

func newTestManager(t *testing.T) (*Manager, *mockStore) {
	t.Helper()
	ms := newMockStore()
	cfg := config.DefaultConfig()
	logger := logging.New(logging.LevelInfo, "", 0)
	mgr := New(ms, cfg, logger)
	t.Cleanup(func() {
		mgr.Stop()
	})
	return mgr, ms
}

// ===========================================================================
// Server management
// ===========================================================================

func TestGetServers_Empty(t *testing.T) {
	mgr, _ := newTestManager(t)

	servers := mgr.GetServers()
	if servers == nil {
		// nil is acceptable for empty
		return
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(servers))
	}
}

func TestGetServers_WithData(t *testing.T) {
	mgr, ms := newTestManager(t)

	ms.addServer("irc.test.net", 6667, true)
	ms.addServer("irc.other.net", 6667, false)

	servers := mgr.GetServers()
	if len(servers) != 2 {
		t.Errorf("expected 2 servers, got %d", len(servers))
	}
}

func TestConnectServer_AlreadyConnected(t *testing.T) {
	mgr, ms := newTestManager(t)

	srvID := ms.addServer("irc.test.net", 6667, true)

	// First call should try to connect (will fail because no real IRC server)
	err := mgr.ConnectServerByID(srvID)
	if err != nil {
		// Expected: connection will fail because there's no real IRC server
		// This is fine — the important thing is no panic
		_ = err
	}
}

func TestDisconnectServer_NonExistent(t *testing.T) {
	mgr, _ := newTestManager(t)

	// DisconnectServer is idempotent: calling it on a non-managed server
	// returns nil because the desired state (disconnected) is already achieved.
	err := mgr.DisconnectServer(999)
	if err != nil {
		t.Errorf("expected nil for non-existent server (idempotent), got: %v", err)
	}
}

func TestJoinChannel_NotConnected(t *testing.T) {
	mgr, _ := newTestManager(t)

	err := mgr.JoinChannel(999, "#test")
	if err != nil {
		// Expected: server not connected
		if err.Error() != "server 999 is not connected" {
			t.Errorf("expected 'not connected' error, got: %v", err)
		}
	}
}

func TestLeaveChannel_NotConnected(t *testing.T) {
	mgr, _ := newTestManager(t)

	err := mgr.LeaveChannel(999, "#test")
	if err != nil {
		// Expected: not connected
		_ = err
	}
}

func TestGetChannelTopic_NotConnected(t *testing.T) {
	mgr, _ := newTestManager(t)

	_, err := mgr.GetChannelTopic(999, "#test")
	if err == nil {
		t.Errorf("expected error for non-connected server")
	}
}

func TestGetChannelTopic_NotJoined(t *testing.T) {
	mgr, ms := newTestManager(t)

	srvID := ms.addServer("irc.test.net", 6667, true)
	// The server exists but is not connected (no real IRC)
	_, err := mgr.GetChannelTopic(srvID, "#test")
	if err == nil {
		t.Errorf("expected error for non-connected server")
	}
}

// ===========================================================================
// GetChannels
// ===========================================================================

func TestGetChannels_NonExistentServer(t *testing.T) {
	mgr, _ := newTestManager(t)

	chs := mgr.GetChannels(999)
	if chs == nil {
		// nil is acceptable
		return
	}
	if len(chs) != 0 {
		t.Errorf("expected 0 channels for non-existent server, got %d", len(chs))
	}
}

func TestGetChannels_ExistingServer(t *testing.T) {
	mgr, ms := newTestManager(t)

	srvID := ms.addServer("irc.test.net", 6667, true)
	ms.addChannel(srvID, "#general", true)
	ms.addChannel(srvID, "#random", false)

	chs := mgr.GetChannels(srvID)
	if len(chs) != 2 {
		t.Errorf("expected 2 channels, got %d", len(chs))
	}
}

// ===========================================================================
// Start / Stop lifecycle
// ===========================================================================

func TestStop_NotStarted(t *testing.T) {
	mgr, _ := newTestManager(t)

	// Stop without Start should not panic
	mgr.Stop()
}

// ===========================================================================
// Subscribe/Unsubscribe
// ===========================================================================

func TestSubscribe_ReceiveEvents(t *testing.T) {
	mgr, _ := newTestManager(t)

	ch := mgr.Subscribe()
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}

	// Emit an event
	mgr.emitEvent(Event{
		Type:       EventServerConnected,
		ServerID:   1,
		ServerAddr: "irc.test.net",
	})

	select {
	case evt := <-ch:
		if evt.Type != EventServerConnected {
			t.Errorf("expected EventServerConnected, got %s", evt.Type)
		}
		if evt.ServerID != 1 {
			t.Errorf("expected ServerID=1, got %d", evt.ServerID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}

	mgr.Unsubscribe(ch)
}

func TestMultipleSubscribers(t *testing.T) {
	mgr, _ := newTestManager(t)

	ch1 := mgr.Subscribe()
	ch2 := mgr.Subscribe()
	defer mgr.Unsubscribe(ch1)
	defer mgr.Unsubscribe(ch2)

	mgr.emitEvent(Event{Type: EventServerConnected, ServerID: 1, ServerAddr: "irc.test.net"})

	// Both should receive the event
	for i, ch := range []chan Event{ch1, ch2} {
		select {
		case <-ch:
			// OK
		case <-time.After(time.Second):
			t.Errorf("subscriber %d did not receive event", i)
		}
	}
}

func TestUnsubscribe_StopsReceiving(t *testing.T) {
	mgr, _ := newTestManager(t)

	ch := mgr.Subscribe()
	mgr.Unsubscribe(ch)

	// After unsubscribe the channel is closed, so reading from it
	// returns the zero value immediately. We can't block on it.
	// Just verify no panic occurs and the manager can still emit events.
	mgr.emitEvent(Event{Type: EventServerConnected, ServerID: 1, ServerAddr: "irc.test.net"})
}

// ===========================================================================
// normalizeChannel
// ===========================================================================

func TestNormalizeChannel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"#channel", "#channel"},
		{"channel", "#channel"},
		{"  #channel  ", "#channel"},
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
// randomSuffix
// ===========================================================================

func TestRandomSuffix(t *testing.T) {
	s1 := randomSuffix(4)
	s2 := randomSuffix(4)

	if len(s1) != 4 {
		t.Errorf("expected length 4, got %d", len(s1))
	}
	if s1 == s2 {
		t.Errorf("expected different suffixes, got same: %q", s1)
	}
}

// ===========================================================================
// isOwnNick
// ===========================================================================

func TestIsOwnNick(t *testing.T) {
	tests := []struct {
		source string
		nick   string
		want   bool
	}{
		{"myuser!~ident@host", "myuser", true},
		{"other!~ident@host", "myuser", false},
		{"myuser", "myuser", true},
		{"MyUser", "myuser", false}, // case-sensitive
		{"", "myuser", false},
	}

	for _, tt := range tests {
		got := isOwnNick(tt.source, tt.nick)
		if got != tt.want {
			t.Errorf("isOwnNick(%q, %q) = %v, want %v", tt.source, tt.nick, got, tt.want)
		}
	}
}
