package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"xdcc_server/internal/logging"
)

// newTestStore creates a SQLiteStore backed by a copy of the pre-migrated
// template database. This avoids running Migrate() (with expensive VACUUM INTO
// backups) for every test — the template is created once in TestMain.
func newTestStore(tb testing.TB) *SQLiteStore {
	tb.Helper()
	dir := tb.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	if templateDBPath == "" {
		tb.Fatal("templateDBPath is empty — did TestMain run?")
	}
	if err := copyFile(templateDBPath, dbPath); err != nil {
		tb.Fatalf("copying template DB: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		tb.Fatalf("opening test DB: %v", err)
	}

	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(3)

	// Batch all PRAGMAs into a single Exec to reduce round-trips.
	// foreign_keys is a connection-level setting; synchronous=OFF and
	// journal_mode=MEMORY disable durability guarantees (not needed in tests).
	if _, err := db.Exec("PRAGMA foreign_keys=ON; PRAGMA synchronous=OFF; PRAGMA journal_mode=MEMORY"); err != nil {
		db.Close()
		tb.Fatalf("setting PRAGMAs: %v", err)
	}

	testLog := logging.New(logging.LevelDebug, "", 0)
	s := &SQLiteStore{db: db, dbPath: dbPath, log: testLog}
	tb.Cleanup(func() {
		if err := s.Close(); err != nil {
			tb.Errorf("Close: %v", err)
		}
	})
	return s
}

// ===========================================================================
// Server CRUD
// ===========================================================================

func TestAddServer(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, err := s.AddServer(context.Background(), ServerRecord{
		Address:     "irc.test.net",
		Port:        6667,
		AutoConnect: true,
		Status:      "disconnected",
	})
	if err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive id, got %d", id)
	}
}

func TestGetServer_NotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	srv, err := s.GetServer(context.Background(), 999)
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if srv != nil {
		t.Errorf("expected nil for missing server, got %+v", srv)
	}
}

func TestGetServer_Found(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.AddServer(context.Background(), ServerRecord{Address: "irc.example.com", Port: 6667, Status: "disconnected"})
	srv, err := s.GetServer(context.Background(), id)
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if srv == nil {
		t.Fatal("expected non-nil server")
	}
	if srv.Address != "irc.example.com" {
		t.Errorf("expected address irc.example.com, got %s", srv.Address)
	}
	if srv.Port != 6667 {
		t.Errorf("expected port 6667, got %d", srv.Port)
	}
	if srv.ID != id {
		t.Errorf("expected id %d, got %d", id, srv.ID)
	}
}

func TestIncrementServerRetry(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.AddServer(context.Background(), ServerRecord{Address: "irc.retry.net", Port: 6667, Status: "connected"})
	err := s.IncrementServerRetry(context.Background(), id)
	if err != nil {
		t.Fatalf("IncrementServerRetry: %v", err)
	}

	srv, _ := s.GetServer(context.Background(), id)
	if srv.RetryCount != 1 {
		t.Errorf("expected retry_count 1, got %d", srv.RetryCount)
	}
	if srv.Status != "reconnecting" {
		t.Errorf("expected status 'reconnecting', got %s", srv.Status)
	}
}

// ===========================================================================
// Channel CRUD
// ===========================================================================

func TestAddChannel(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	srvID, _ := s.AddServer(context.Background(), ServerRecord{Address: "irc.chan.net", Port: 6667})
	chID, err := s.AddChannel(context.Background(), ChannelRecord{
		ServerID: srvID,
		Name:     "#test",
		AutoJoin: true,
	})
	if err != nil {
		t.Fatalf("AddChannel: %v", err)
	}
	if chID <= 0 {
		t.Errorf("expected positive channel id, got %d", chID)
	}
}

func TestGetChannelsByServer(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	srvID, _ := s.AddServer(context.Background(), ServerRecord{Address: "irc.chan2.net", Port: 6667})
	_, _ = s.AddChannel(context.Background(), ChannelRecord{ServerID: srvID, Name: "#alpha"})
	_, _ = s.AddChannel(context.Background(), ChannelRecord{ServerID: srvID, Name: "#beta"})

	channels, err := s.GetChannelsByServer(context.Background(), srvID)
	if err != nil {
		t.Fatalf("GetChannelsByServer: %v", err)
	}
	if len(channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(channels))
	}

	// Wrong server ID
	empty, _ := s.GetChannelsByServer(context.Background(), 999)
	if len(empty) != 0 {
		t.Errorf("expected 0 channels for non-existent server, got %d", len(empty))
	}
}

func TestGetChannelsByServerAndName(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	srvID, _ := s.AddServer(context.Background(), ServerRecord{Address: "irc.channelookup.net", Port: 6667})
	chID, _ := s.AddChannel(context.Background(), ChannelRecord{ServerID: srvID, Name: "#lookup"})

	ch, err := s.GetChannelsByServerAndName(context.Background(), srvID, "#lookup")
	if err != nil {
		t.Fatalf("GetChannelsByServerAndName: %v", err)
	}
	if ch == nil {
		t.Fatal("expected channel, got nil")
	}
	if ch.ID != chID {
		t.Errorf("expected id %d, got %d", chID, ch.ID)
	}

	// Not found
	missing, _ := s.GetChannelsByServerAndName(context.Background(), srvID, "#missing")
	if missing != nil {
		t.Errorf("expected nil for missing channel, got %+v", missing)
	}
}

func TestUpdateChannel(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	srvID, _ := s.AddServer(context.Background(), ServerRecord{Address: "irc.updchan.net", Port: 6667})
	chID, _ := s.AddChannel(context.Background(), ChannelRecord{ServerID: srvID, Name: "#old", AutoJoin: true})

	err := s.UpdateChannel(context.Background(), ChannelRecord{ID: chID, ServerID: srvID, Name: "#old", Topic: "new topic", AutoJoin: true, Joined: true})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}

	ch, _ := s.GetChannelsByServerAndName(context.Background(), srvID, "#old")
	if ch.Topic != "new topic" {
		t.Errorf("expected topic 'new topic', got %s", ch.Topic)
	}
	if !ch.Joined {
		t.Errorf("expected joined=true")
	}
}

func TestSetChannelJoined(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	srvID, _ := s.AddServer(context.Background(), ServerRecord{Address: "irc.joined.net", Port: 6667})
	chID, _ := s.AddChannel(context.Background(), ChannelRecord{ServerID: srvID, Name: "#joinedtest"})

	_ = s.SetChannelJoined(context.Background(), chID, true)
	ch, _ := s.GetChannelsByServerAndName(context.Background(), srvID, "#joinedtest")
	if !ch.Joined {
		t.Errorf("expected joined=true after SetChannelJoined")
	}
}

func TestDeleteChannel(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	srvID, _ := s.AddServer(context.Background(), ServerRecord{Address: "irc.delchan.net", Port: 6667})
	chID, _ := s.AddChannel(context.Background(), ChannelRecord{ServerID: srvID, Name: "#delme"})

	_ = s.DeleteChannel(context.Background(), chID)
	channels, _ := s.GetChannelsByServer(context.Background(), srvID)
	if len(channels) != 0 {
		t.Errorf("expected 0 channels after delete, got %d", len(channels))
	}
}

func TestGetAutoJoinChannels(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	// Add a server with auto_connect=true
	srvID, _ := s.AddServer(context.Background(), ServerRecord{
		Address: "irc.auto.net", Port: 6667, AutoConnect: true, Status: "disconnected",
	})
	_, _ = s.AddChannel(context.Background(), ChannelRecord{ServerID: srvID, Name: "#auto1", AutoJoin: true})
	_, _ = s.AddChannel(context.Background(), ChannelRecord{ServerID: srvID, Name: "#auto2", AutoJoin: false})

	// Add another server with auto_connect=false — its channels should NOT be returned
	srvID2, _ := s.AddServer(context.Background(), ServerRecord{
		Address: "irc.manual.net", Port: 6667, AutoConnect: false,
	})
	_, _ = s.AddChannel(context.Background(), ChannelRecord{ServerID: srvID2, Name: "#manual", AutoJoin: true})

	autoChs, err := s.GetAutoJoinChannels(context.Background())
	if err != nil {
		t.Fatalf("GetAutoJoinChannels: %v", err)
	}
	if len(autoChs) != 1 {
		t.Errorf("expected 1 auto-join channel, got %d", len(autoChs))
	}
	if len(autoChs) > 0 && autoChs[0].Name != "#auto1" {
		t.Errorf("expected #auto1, got %s", autoChs[0].Name)
	}
}

// ===========================================================================
// Download CRUD
// ===========================================================================

func TestEnqueueAndGetDownload(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, err := s.EnqueueDownload(context.Background(), DownloadRecord{
		PackMessage:   "xdcc send #1",
		Bot:           "TestBot",
		ServerAddress: "irc.test.net",
		Channel:       "#test",
		Filename:      "testfile.mkv",
		FileSize:      1000000,
	})
	if err != nil {
		t.Fatalf("EnqueueDownload: %v", err)
	}

	d, err := s.GetDownload(context.Background(), id)
	if err != nil {
		t.Fatalf("GetDownload: %v", err)
	}
	if d.Status != DownloadStatusQueued {
		t.Errorf("expected status 'queued', got %s", d.Status)
	}
	if d.Bot != "TestBot" {
		t.Errorf("expected bot TestBot, got %s", d.Bot)
	}
	if d.Filename != "testfile.mkv" {
		t.Errorf("expected filename testfile.mkv, got %s", d.Filename)
	}
}

func TestGetQueue(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	// Enqueue 3 downloads
	_, _ = s.EnqueueDownload(context.Background(), DownloadRecord{Bot: "Bot1", ServerAddress: "irc.t.net", Channel: "#a", Filename: "a.mkv", FileSize: 100})
	_, _ = s.EnqueueDownload(context.Background(), DownloadRecord{Bot: "Bot2", ServerAddress: "irc.t.net", Channel: "#b", Filename: "b.mkv", FileSize: 200})
	_, _ = s.EnqueueDownload(context.Background(), DownloadRecord{Bot: "Bot3", ServerAddress: "irc.t.net", Channel: "#c", Filename: "c.mkv", FileSize: 300})

	queue, err := s.GetQueue(context.Background())
	if err != nil {
		t.Fatalf("GetQueue: %v", err)
	}
	if len(queue) != 3 {
		t.Errorf("expected 3 items in queue, got %d", len(queue))
	}
}

func TestGetQueueByChannel(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	_, _ = s.EnqueueDownload(context.Background(), DownloadRecord{Bot: "Bot1", ServerAddress: "irc.t.net", Channel: "#xdcc", Filename: "a.mkv", FileSize: 100})
	_, _ = s.EnqueueDownload(context.Background(), DownloadRecord{Bot: "Bot2", ServerAddress: "irc.t.net", Channel: "#other", Filename: "b.mkv", FileSize: 100})
	_, _ = s.EnqueueDownload(context.Background(), DownloadRecord{Bot: "Bot3", ServerAddress: "irc.t.net", Channel: "#xdcc", Filename: "c.mkv", FileSize: 100})

	queue, _ := s.GetQueueByChannel(context.Background(), "#xdcc")
	if len(queue) != 2 {
		t.Errorf("expected 2 items for #xdcc, got %d", len(queue))
	}

	other, _ := s.GetQueueByChannel(context.Background(), "#nonexistent")
	if len(other) != 0 {
		t.Errorf("expected 0 items for nonexistent channel, got %d", len(other))
	}
}

func TestMarkDownloadStarted(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "f.mkv", FileSize: 100,
	})
	err := s.MarkDownloadStarted(context.Background(), id)
	if err != nil {
		t.Fatalf("MarkDownloadStarted: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Status != DownloadStatusDownloading {
		t.Errorf("expected status 'downloading', got %s", d.Status)
	}
	if d.StartedAt == nil {
		t.Errorf("expected started_at to be set")
	}
}

func TestUpdateDownloadProgress(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "f.mkv", FileSize: 1000,
	})

	_ = s.UpdateDownloadProgress(context.Background(), id, 500, 100)
	d, _ := s.GetDownload(context.Background(), id)
	if d.ProgressBytes != 500 {
		t.Errorf("expected progress 500, got %d", d.ProgressBytes)
	}
	if d.SpeedBPS != 100 {
		t.Errorf("expected speed 100, got %d", d.SpeedBPS)
	}
}

func TestMarkDownloadCompleted(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "f.mkv", FileSize: 1000,
	})
	_ = s.MarkDownloadStarted(context.Background(), id)
	_ = s.UpdateDownloadProgress(context.Background(), id, 1000, 0)

	err := s.MarkDownloadCompleted(context.Background(), id, "", 0)
	if err != nil {
		t.Fatalf("MarkDownloadCompleted: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Status != DownloadStatusCompleted {
		t.Errorf("expected status 'completed', got %s", d.Status)
	}
	if d.CompletedAt == nil {
		t.Errorf("expected completed_at to be set")
	}
}

func TestMarkDownloadFailed(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "f.mkv", FileSize: 1000,
	})
	err := s.MarkDownloadFailed(context.Background(), id, "connection timeout")
	if err != nil {
		t.Fatalf("MarkDownloadFailed: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Status != DownloadStatusFailed {
		t.Errorf("expected status 'failed', got %s", d.Status)
	}
	if d.ErrorMessage != "connection timeout" {
		t.Errorf("expected error message 'connection timeout', got %s", d.ErrorMessage)
	}
}

func TestMarkDownloadSkipped(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "f.mkv", FileSize: 1000,
	})
	_ = s.MarkDownloadStarted(context.Background(), id)

	err := s.MarkDownloadSkipped(context.Background(), id)
	if err != nil {
		t.Fatalf("MarkDownloadSkipped: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Status != DownloadStatusSkipped {
		t.Errorf("expected status 'skipped_existing', got %s", d.Status)
	}
}

func TestMarkDownloadPaused(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "f.mkv", FileSize: 1000,
	})
	_ = s.MarkDownloadStarted(context.Background(), id)

	err := s.MarkDownloadPaused(context.Background(), id)
	if err != nil {
		t.Fatalf("MarkDownloadPaused: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Status != DownloadStatusPaused {
		t.Errorf("expected status 'paused', got %s", d.Status)
	}
}

func TestMarkDownloadPaused_OnlyQueuedOrDownloading(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "f.mkv", FileSize: 1000,
	})
	_ = s.MarkDownloadCompleted(context.Background(), id, "", 0)

	// Pausing a completed download should be a no-op (no rows affected, but no error)
	err := s.MarkDownloadPaused(context.Background(), id)
	if err != nil {
		t.Fatalf("MarkDownloadPaused on completed: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Status != DownloadStatusCompleted {
		t.Errorf("expected status still 'completed', got %s", d.Status)
	}
}

func TestRetryDownload(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "f.mkv", FileSize: 1000,
	})
	_ = s.MarkDownloadFailed(context.Background(), id, "some error")

	err := s.RetryDownload(context.Background(), id)
	if err != nil {
		t.Fatalf("RetryDownload: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Status != DownloadStatusQueued {
		t.Errorf("expected status 'queued' after retry, got %s", d.Status)
	}
	if d.ProgressBytes != 0 {
		t.Errorf("expected progress_bytes reset to 0, got %d", d.ProgressBytes)
	}
	if d.ErrorMessage != "" {
		t.Errorf("expected error_message cleared, got %s", d.ErrorMessage)
	}
}

func TestDeleteDownload(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "f.mkv", FileSize: 1000,
	})
	_ = s.DeleteDownload(context.Background(), id)

	d, _ := s.GetDownload(context.Background(), id)
	if d != nil {
		t.Errorf("expected deleted download to be nil, got %+v", d)
	}
}

func TestSetDownloadPriority(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "f.mkv", FileSize: 1000,
	})

	err := s.SetDownloadPriority(context.Background(), id, 1)
	if err != nil {
		t.Fatalf("SetDownloadPriority: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Priority != 1 {
		t.Errorf("expected priority 1, got %d", d.Priority)
	}
}

func TestBulkActionDownloads(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id1, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "a.mkv", FileSize: 100,
	})
	id2, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "b.mkv", FileSize: 100,
	})

	results, err := s.BulkActionDownloads(context.Background(), []int64{id1, id2}, "pause")
	if err != nil {
		t.Fatalf("BulkActionDownloads: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	for id, r := range results {
		if r != "success" {
			t.Errorf("expected success for id %d, got %s", id, r)
		}
	}

	// Verify both are paused
	queue, _ := s.GetQueue(context.Background())
	for _, d := range queue {
		if d.ID == id1 || d.ID == id2 {
			if d.Status != DownloadStatusPaused {
				t.Errorf("expected download %d to be paused, got %s", d.ID, d.Status)
			}
		}
	}
}

func TestBulkActionUnknownAction(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "f.mkv", FileSize: 100,
	})
	results, _ := s.BulkActionDownloads(context.Background(), []int64{id}, "unknown")
	if results[id] != "unknown action: unknown" {
		t.Errorf("expected 'unknown action: unknown', got %s", results[id])
	}
}

// ===========================================================================
// Search Cache
// ===========================================================================

func TestSetAndGetSearchCache(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	now := time.Now()
	entry := SearchCacheEntry{
		QueryKey:       "test query",
		Provider:       "nibl",
		PayloadJSON:    `[{"filename":"test.mkv","size":1000}]`,
		FetchedAt:      now,
		ExpiresAt:      now.Add(time.Hour),
		StaleExpiresAt: now.Add(24 * time.Hour),
	}

	err := s.SetSearchCache(context.Background(), entry)
	if err != nil {
		t.Fatalf("SetSearchCache: %v", err)
	}

	got, err := s.GetSearchCache(context.Background(), "test query", "nibl")
	if err != nil {
		t.Fatalf("GetSearchCache: %v", err)
	}
	if got == nil {
		t.Fatal("expected cache entry, got nil")
	}
	if got.PayloadJSON != entry.PayloadJSON {
		t.Errorf("expected payload %s, got %s", entry.PayloadJSON, got.PayloadJSON)
	}
}

func TestGetSearchCache_Missing(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	entry, err := s.GetSearchCache(context.Background(), "nonexistent", "nibl")
	if err != nil {
		t.Fatalf("GetSearchCache: %v", err)
	}
	if entry != nil {
		t.Errorf("expected nil for missing cache entry, got %+v", entry)
	}
}

func TestDeleteExpiredSearchCache(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	now := time.Now()
	entry := SearchCacheEntry{
		QueryKey:       "stale query",
		Provider:       "xdcc_eu",
		PayloadJSON:    "[]",
		FetchedAt:      now.Add(-48 * time.Hour),
		ExpiresAt:      now.Add(-24 * time.Hour),
		StaleExpiresAt: now.Add(-1 * time.Hour),
	}
	_ = s.SetSearchCache(context.Background(), entry)

	err := s.DeleteExpiredSearchCache(context.Background(), now)
	if err != nil {
		t.Fatalf("DeleteExpiredSearchCache: %v", err)
	}

	got, _ := s.GetSearchCache(context.Background(), "stale query", "xdcc_eu")
	if got != nil {
		t.Errorf("expected stale entry to be deleted, but got %+v", got)
	}
}

func TestGetSearchCacheByQuery(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	now := time.Now()
	// Insert entries from multiple providers for same query
	for _, prov := range []string{"nibl", "xdcc_eu", "sunxdcc"} {
		entry := SearchCacheEntry{
			QueryKey:       "multi provider",
			Provider:       prov,
			PayloadJSON:    `[{"filename":"` + prov + `.mkv"}]`,
			FetchedAt:      now,
			ExpiresAt:      now.Add(time.Hour),
			StaleExpiresAt: now.Add(24 * time.Hour),
		}
		if err := s.SetSearchCache(context.Background(), entry); err != nil {
			t.Fatalf("SetSearchCache(%s): %v", prov, err)
		}
	}

	// Insert entry for different query (should not appear)
	other := SearchCacheEntry{
		QueryKey:       "other query",
		Provider:       "nibl",
		PayloadJSON:    `[]`,
		FetchedAt:      now,
		ExpiresAt:      now.Add(time.Hour),
		StaleExpiresAt: now.Add(24 * time.Hour),
	}
	_ = s.SetSearchCache(context.Background(), other)

	entries, err := s.GetSearchCacheByQuery(context.Background(), "multi provider")
	if err != nil {
		t.Fatalf("GetSearchCacheByQuery: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	providers := make(map[string]bool)
	for _, e := range entries {
		providers[e.Provider] = true
		if e.QueryKey != "multi provider" {
			t.Errorf("unexpected query_key: %s", e.QueryKey)
		}
	}
	for _, prov := range []string{"nibl", "xdcc_eu", "sunxdcc"} {
		if !providers[prov] {
			t.Errorf("missing provider %s in results", prov)
		}
	}
}

func TestGetSearchCacheByQuery_Empty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	entries, err := s.GetSearchCacheByQuery(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("GetSearchCacheByQuery: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty slice, got %d entries", len(entries))
	}
}

// TestGetSearchCacheByQuery_NoDeadlock verifies that GetSearchCacheByQuery
// does not deadlock even with limited connections. This is the regression test
// for the critical bug where getFresh() used nested queries.
func TestGetSearchCacheByQuery_NoDeadlock(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	now := time.Now()
	// Populate cache
	for i := 0; i < 5; i++ {
		entry := SearchCacheEntry{
			QueryKey:       "deadlock test",
			Provider:       fmt.Sprintf("provider_%d", i),
			PayloadJSON:    `[{"filename":"test.mkv"}]`,
			FetchedAt:      now,
			ExpiresAt:      now.Add(time.Hour),
			StaleExpiresAt: now.Add(24 * time.Hour),
		}
		_ = s.SetSearchCache(context.Background(), entry)
	}

	// Run concurrent GetSearchCacheByQuery - should not deadlock
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			entries, err := s.GetSearchCacheByQuery(context.Background(), "deadlock test")
			done <- (err == nil && len(entries) == 5)
		}()
	}

	// If this times out, we have a deadlock
	timeout := time.After(5 * time.Second)
	for i := 0; i < 10; i++ {
		select {
		case ok := <-done:
			if !ok {
				t.Error("concurrent GetSearchCacheByQuery returned unexpected result")
			}
		case <-timeout:
			t.Fatal("DEADLOCK: GetSearchCacheByQuery timed out under concurrent access")
		}
	}
}

// ===========================================================================
// Search Presets
// ===========================================================================

func TestAddAndGetSearchPreset(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	p := SearchPreset{
		Name:        "Anime Weekly",
		Query:       "anime 1080p",
		FiltersJSON: `{"ext":"mkv"}`,
		IsDefault:   true,
	}

	id, err := s.AddSearchPreset(context.Background(), p)
	if err != nil {
		t.Fatalf("AddSearchPreset: %v", err)
	}

	got, err := s.GetSearchPreset(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSearchPreset: %v", err)
	}
	if got == nil {
		t.Fatal("expected preset, got nil")
	}
	if got.Name != "Anime Weekly" {
		t.Errorf("expected name 'Anime Weekly', got %s", got.Name)
	}
	if !got.IsDefault {
		t.Errorf("expected is_default=true")
	}
}

func TestListSearchPresets(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	_, _ = s.AddSearchPreset(context.Background(), SearchPreset{Name: "Preset A", Query: "query a"})
	_, _ = s.AddSearchPreset(context.Background(), SearchPreset{Name: "Preset B", Query: "query b"})

	presets, err := s.ListSearchPresets(context.Background())
	if err != nil {
		t.Fatalf("ListSearchPresets: %v", err)
	}
	if len(presets) != 2 {
		t.Errorf("expected 2 presets, got %d", len(presets))
	}
}

func TestUpdateSearchPreset(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.AddSearchPreset(context.Background(), SearchPreset{Name: "Old Name", Query: "old query"})
	err := s.UpdateSearchPreset(context.Background(), SearchPreset{ID: id, Name: "New Name", Query: "new query", FiltersJSON: "{}"})
	if err != nil {
		t.Fatalf("UpdateSearchPreset: %v", err)
	}

	got, _ := s.GetSearchPreset(context.Background(), id)
	if got.Name != "New Name" {
		t.Errorf("expected name 'New Name', got %s", got.Name)
	}
}

func TestDeleteSearchPreset(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.AddSearchPreset(context.Background(), SearchPreset{Name: "Del Me", Query: "delete me"})
	_ = s.DeleteSearchPreset(context.Background(), id)

	got, _ := s.GetSearchPreset(context.Background(), id)
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestSetDefaultSearchPreset(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id1, _ := s.AddSearchPreset(context.Background(), SearchPreset{Name: "A", Query: "a", IsDefault: true})
	id2, _ := s.AddSearchPreset(context.Background(), SearchPreset{Name: "B", Query: "b"})

	err := s.SetDefaultSearchPreset(context.Background(), id2)
	if err != nil {
		t.Fatalf("SetDefaultSearchPreset: %v", err)
	}

	p1, _ := s.GetSearchPreset(context.Background(), id1)
	p2, _ := s.GetSearchPreset(context.Background(), id2)
	if p1.IsDefault {
		t.Errorf("expected preset A to no longer be default")
	}
	if !p2.IsDefault {
		t.Errorf("expected preset B to be default")
	}
}

// ===========================================================================
// Watchlists
// ===========================================================================

func TestAddAndGetWatchlist(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	w := Watchlist{
		Name:        "My Watchlist",
		Query:       "anime 1080p",
		FiltersJSON: `{"ext":"mkv"}`,
		Enabled:     true,
		AutoEnqueue: false,
	}

	id, err := s.AddWatchlist(context.Background(), w)
	if err != nil {
		t.Fatalf("AddWatchlist: %v", err)
	}

	got, err := s.GetWatchlist(context.Background(), id)
	if err != nil {
		t.Fatalf("GetWatchlist: %v", err)
	}
	if got == nil {
		t.Fatal("expected watchlist, got nil")
	}
	if got.Name != "My Watchlist" {
		t.Errorf("expected name 'My Watchlist', got %s", got.Name)
	}
	if !got.Enabled {
		t.Errorf("expected enabled=true")
	}
}

func TestListWatchlists(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	_, _ = s.AddWatchlist(context.Background(), Watchlist{Name: "WL1", Query: "query1"})
	_, _ = s.AddWatchlist(context.Background(), Watchlist{Name: "WL2", Query: "query2"})

	lists, err := s.ListWatchlists(context.Background())
	if err != nil {
		t.Fatalf("ListWatchlists: %v", err)
	}
	if len(lists) != 2 {
		t.Errorf("expected 2 watchlists, got %d", len(lists))
	}
}

func TestDeleteWatchlist(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.AddWatchlist(context.Background(), Watchlist{Name: "Del", Query: "delete"})
	_ = s.DeleteWatchlist(context.Background(), id)

	got, _ := s.GetWatchlist(context.Background(), id)
	if got != nil {
		t.Errorf("expected nil after delete, got %+v", got)
	}
}

func TestSetWatchlistChecked(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.AddWatchlist(context.Background(), Watchlist{Name: "Check", Query: "check"})
	err := s.SetWatchlistChecked(context.Background(), id, "abc123", `[{"filename":"test.mkv","size":1000}]`)
	if err != nil {
		t.Fatalf("SetWatchlistChecked: %v", err)
	}

	w, _ := s.GetWatchlist(context.Background(), id)
	if w.LastMatchFingerprint != "abc123" {
		t.Errorf("expected fingerprint 'abc123', got %s", w.LastMatchFingerprint)
	}
	if w.LastCheckedAt == nil {
		t.Errorf("expected last_checked_at to be set")
	}
	if string(w.LastResultsJSON) != `[{"filename":"test.mkv","size":1000}]` {
		t.Errorf("expected results JSON to be stored, got %s", string(w.LastResultsJSON))
	}
}

func TestGetEnabledWatchlists(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	_, _ = s.AddWatchlist(context.Background(), Watchlist{Name: "Enabled1", Query: "q1", Enabled: true})
	_, _ = s.AddWatchlist(context.Background(), Watchlist{Name: "Disabled", Query: "q2", Enabled: false})
	_, _ = s.AddWatchlist(context.Background(), Watchlist{Name: "Enabled2", Query: "q3", Enabled: true})

	enabled, err := s.GetEnabledWatchlists(context.Background())
	if err != nil {
		t.Fatalf("GetEnabledWatchlists: %v", err)
	}
	if len(enabled) != 2 {
		t.Errorf("expected 2 enabled watchlists, got %d", len(enabled))
	}
}

// ===========================================================================
// Provider Stats
// ===========================================================================

func TestRecordAndGetProviderStats(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	now := time.Now()
	stats := ProviderStats{
		Provider:     "nibl",
		WindowStart:  now.Truncate(1 * time.Hour),
		WindowEnd:    now.Truncate(1 * time.Hour).Add(1 * time.Hour),
		Requests:     10,
		Successes:    8,
		Timeouts:     1,
		Failures:     1,
		AvgLatencyMs: 250.5,
		UpdatedAt:    now,
	}

	err := s.RecordProviderStats(context.Background(), stats)
	if err != nil {
		t.Fatalf("RecordProviderStats: %v", err)
	}

	since := now.Add(-2 * time.Hour)
	got, err := s.GetProviderStats(context.Background(), "nibl", since)
	if err != nil {
		t.Fatalf("GetProviderStats: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 stats record, got %d", len(got))
	}
	if len(got) > 0 {
		if got[0].Requests != 10 {
			t.Errorf("expected 10 requests, got %d", got[0].Requests)
		}
	}
}

func TestGetAllProviderStats(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	now := time.Now()
	_ = s.RecordProviderStats(context.Background(), ProviderStats{
		Provider: "nibl", WindowStart: now.Add(-30 * time.Minute),
		WindowEnd: now, Requests: 5, Successes: 5,
	})
	_ = s.RecordProviderStats(context.Background(), ProviderStats{
		Provider: "xdcc_eu", WindowStart: now.Add(-30 * time.Minute),
		WindowEnd: now, Requests: 3, Successes: 3,
	})

	all, err := s.GetAllProviderStats(context.Background(), now.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("GetAllProviderStats: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 providers, got %d", len(all))
	}
}

// ===========================================================================
// CurrentSchemaVersion
// ===========================================================================

func TestCurrentSchemaVersion(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	v, err := s.CurrentSchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("CurrentSchemaVersion: %v", err)
	}
	if v < 0 {
		t.Errorf("expected non-negative schema version, got %d", v)
	}
}

// ===========================================================================
// Export / Import
// ===========================================================================

func TestExportData_Empty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	exp, err := s.ExportData(context.Background())
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	if exp == nil {
		t.Fatal("expected non-nil export data")
	}
	if len(exp.Servers) != 0 {
		t.Errorf("expected empty servers, got %d", len(exp.Servers))
	}
}

func TestExportImportData(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	// Add some data
	_, _ = s.AddServer(context.Background(), ServerRecord{Address: "irc.export.net", Port: 6667, Status: "connected"})
	_, _ = s.AddWatchlist(context.Background(), Watchlist{Name: "Export WL", Query: "test", Enabled: true})
	_, _ = s.AddSearchPreset(context.Background(), SearchPreset{Name: "Export Preset", Query: "test"})

	exp, err := s.ExportData(context.Background())
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	if len(exp.Servers) != 1 {
		t.Errorf("expected 1 server in export, got %d", len(exp.Servers))
	}
	if len(exp.Watchlists) != 1 {
		t.Errorf("expected 1 watchlist in export, got %d", len(exp.Watchlists))
	}

	// Import into fresh store
	s2 := newTestStore(t)

	err = s2.ImportData(context.Background(), exp)
	if err != nil {
		t.Fatalf("ImportData: %v", err)
	}

	// Verify imported data
	servers, _ := s2.ListServers(context.Background())
	if len(servers) != 1 {
		t.Errorf("expected 1 server imported, got %d", len(servers))
	}
	if servers[0].Address != "irc.export.net" {
		t.Errorf("expected address 'irc.export.net', got %s", servers[0].Address)
	}

	wls, _ := s2.ListWatchlists(context.Background())
	if len(wls) != 1 {
		t.Errorf("expected 1 watchlist imported, got %d", len(wls))
	}
}

// ===========================================================================
// GetDownloadByBotMessage
// ===========================================================================

func TestGetDownloadByBotMessage(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "MyBot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "f.mkv", FileSize: 1000, PackMessage: "xdcc send #42",
	})

	d, err := s.GetDownloadByBotMessage(context.Background(), "MyBot", "xdcc send #42")
	if err != nil {
		t.Fatalf("GetDownloadByBotMessage: %v", err)
	}
	if d == nil {
		t.Fatal("expected download, got nil")
	}
	if d.ID != id {
		t.Errorf("expected id %d, got %d", id, d.ID)
	}

	// Not found
	missing, _ := s.GetDownloadByBotMessage(context.Background(), "MyBot", "xdcc send #999")
	if missing != nil {
		t.Errorf("expected nil for non-matching message, got %+v", missing)
	}
}

// ===========================================================================
// RequeueDownload helper
// ===========================================================================

func TestRequeueDownload(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "f.mkv", FileSize: 1000,
	})
	_ = s.MarkDownloadCompleted(context.Background(), id, "", 0)

	err := s.RequeueDownload(context.Background(), id)
	if err != nil {
		t.Fatalf("RequeueDownload: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Status != DownloadStatusQueued {
		t.Errorf("expected status 'queued', got %s", d.Status)
	}
}
