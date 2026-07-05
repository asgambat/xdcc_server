package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// Migration smoke test
// ===========================================================================

func TestMigrations_DownloadsIndexes(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	rows, err := s.DB().QueryContext(context.Background(),
		`SELECT name FROM sqlite_master
		 WHERE type='index' AND tbl_name='downloads'
		 ORDER BY name`)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer rows.Close()

	want := map[string]bool{
		"idx_downloads_status":         false,
		"idx_downloads_channel":        false,
		"idx_downloads_bot_server":     false,
		"idx_downloads_status_channel": false,
		"idx_downloads_filename":       false,
		"idx_downloads_bot":            false,
		"idx_downloads_completed_at":   false,
		"idx_downloads_lower_filename": false,
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		delete(want, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	for name := range want {
		t.Errorf("missing index: %s", name)
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

// ===========================================================================
// Helper functions
// ===========================================================================

func TestBoolToInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   bool
		want int
	}{
		{true, 1},
		{false, 0},
	}
	for _, tt := range tests {
		got := boolToInt(tt.in)
		if got != tt.want {
			t.Errorf("boolToInt(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// ===========================================================================
// nullTime.Scan
// ===========================================================================

func TestNullTimeScan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   any
		want    time.Time
		wantOk  bool
		wantErr bool
	}{
		{"nil", nil, time.Time{}, false, false},
		{"valid datetime", "2024-01-15 10:30:45", time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC), true, false},
		{"invalid format", "2024-01-15T10:30:45Z", time.Time{}, false, true},
		{"empty string", "", time.Time{}, false, true},
		{"wrong type", 12345, time.Time{}, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var nt nullTime
			err := nt.Scan(tt.value)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if nt.Valid != tt.wantOk {
				t.Errorf("Valid: got %v, want %v", nt.Valid, tt.wantOk)
			}
			if nt.Valid && !nt.Time.Equal(tt.want) {
				t.Errorf("Time: got %v, want %v", nt.Time, tt.want)
			}
		})
	}
}

func TestNullTimeScan_ZeroTime(t *testing.T) {
	t.Parallel()
	var nt nullTime
	if err := nt.Scan(nil); err != nil {
		t.Fatalf("Scan(nil): %v", err)
	}
	if nt.Valid {
		t.Error("expected Valid=false after nil scan")
	}
	if !nt.Time.IsZero() {
		t.Errorf("expected zero time, got %v", nt.Time)
	}
}

// ===========================================================================
// UpdateChannelAvgSpeed
// ===========================================================================

func TestUpdateChannelAvgSpeed(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	srvID, _ := s.AddServer(context.Background(), ServerRecord{Address: "irc.speed.net", Port: 6667})
	chID, _ := s.AddChannel(context.Background(), ChannelRecord{ServerID: srvID, Name: "#speedtest"})

	// First update: avg_speed_bps=0 → EMA cold-start → use value directly
	err := s.UpdateChannelAvgSpeed(context.Background(), "irc.speed.net", "#speedtest", 1024.0)
	if err != nil {
		t.Fatalf("UpdateChannelAvgSpeed (first): %v", err)
	}

	ch, _ := s.GetChannelsByServerAndName(context.Background(), srvID, "#speedtest")
	if ch.AvgSpeedBPS != 1024.0 {
		t.Errorf("expected AvgSpeedBPS=1024 after first update (cold start), got %f", ch.AvgSpeedBPS)
	}

	// Second update: avg_speed_bps=1024 → EMA = 1024*0.7 + 2048*0.3 = 716.8 + 614.4 = 1331.2
	err = s.UpdateChannelAvgSpeed(context.Background(), "irc.speed.net", "#speedtest", 2048.0)
	if err != nil {
		t.Fatalf("UpdateChannelAvgSpeed (second): %v", err)
	}

	ch, _ = s.GetChannelsByServerAndName(context.Background(), srvID, "#speedtest")
	// 1024*0.7 + 2048*0.3 = 716.8 + 614.4 = 1331.2
	if ch.AvgSpeedBPS < 1331.0 || ch.AvgSpeedBPS > 1332.0 {
		t.Errorf("expected AvgSpeedBPS≈1331.2 after EMA update, got %f", ch.AvgSpeedBPS)
	}

	_ = chID
}

// ===========================================================================
// IncrementDownloadRetry
// ===========================================================================

func TestIncrementDownloadRetry(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "f.mkv", FileSize: 100,
	})

	for i := 1; i <= 3; i++ {
		err := s.IncrementDownloadRetry(context.Background(), id)
		if err != nil {
			t.Fatalf("IncrementDownloadRetry (iteration %d): %v", i, err)
		}
		d, _ := s.GetDownload(context.Background(), id)
		if d.RetryCount != i {
			t.Errorf("expected retry_count=%d, got %d", i, d.RetryCount)
		}
	}
}

// ===========================================================================
// UpdateDownloadMetadata
// ===========================================================================

func TestUpdateDownloadMetadata_Filename(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "original.mkv", FileSize: 100,
	})

	err := s.UpdateDownloadMetadata(context.Background(), id, "updated.mkv", 0)
	if err != nil {
		t.Fatalf("UpdateDownloadMetadata: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Filename != "updated.mkv" {
		t.Errorf("expected filename 'updated.mkv', got %s", d.Filename)
	}
	if d.FileSize != 100 {
		t.Errorf("expected file_size 100 unchanged, got %d", d.FileSize)
	}
}

func TestUpdateDownloadMetadata_FileSize(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "f.mkv", FileSize: 100,
	})

	err := s.UpdateDownloadMetadata(context.Background(), id, "", 5000)
	if err != nil {
		t.Fatalf("UpdateDownloadMetadata: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Filename != "f.mkv" {
		t.Errorf("expected filename unchanged 'f.mkv', got %s", d.Filename)
	}
	if d.FileSize != 5000 {
		t.Errorf("expected file_size 5000, got %d", d.FileSize)
	}
}

func TestUpdateDownloadMetadata_Both(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "old.mkv", FileSize: 100,
	})

	err := s.UpdateDownloadMetadata(context.Background(), id, "new.mkv", 9999)
	if err != nil {
		t.Fatalf("UpdateDownloadMetadata: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Filename != "new.mkv" {
		t.Errorf("expected filename 'new.mkv', got %s", d.Filename)
	}
	if d.FileSize != 9999 {
		t.Errorf("expected file_size 9999, got %d", d.FileSize)
	}
}

func TestUpdateDownloadMetadata_Noop(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "keep.mkv", FileSize: 500,
	})

	// Empty string + size 0 should leave both unchanged
	err := s.UpdateDownloadMetadata(context.Background(), id, "", 0)
	if err != nil {
		t.Fatalf("UpdateDownloadMetadata: %v", err)
	}

	d, _ := s.GetDownload(context.Background(), id)
	if d.Filename != "keep.mkv" {
		t.Errorf("expected filename unchanged, got %s", d.Filename)
	}
	if d.FileSize != 500 {
		t.Errorf("expected file_size unchanged, got %d", d.FileSize)
	}
}

// ===========================================================================
// CleanupSearchCache
// ===========================================================================

func TestCleanupSearchCache(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	now := time.Now()

	// Insert a stale entry (stale in the past)
	staleEntry := SearchCacheEntry{
		QueryKey:       "stale",
		Provider:       "test",
		PayloadJSON:    "[]",
		FetchedAt:      now.Add(-48 * time.Hour),
		ExpiresAt:      now.Add(-24 * time.Hour),
		StaleExpiresAt: now.Add(-1 * time.Hour),
	}
	_ = s.SetSearchCache(context.Background(), staleEntry)

	// Insert a fresh entry
	freshEntry := SearchCacheEntry{
		QueryKey:       "fresh",
		Provider:       "test",
		PayloadJSON:    "[]",
		FetchedAt:      now,
		ExpiresAt:      now.Add(time.Hour),
		StaleExpiresAt: now.Add(24 * time.Hour),
	}
	_ = s.SetSearchCache(context.Background(), freshEntry)

	deleted, err := s.CleanupSearchCache(context.Background())
	if err != nil {
		t.Fatalf("CleanupSearchCache: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted entry, got %d", deleted)
	}

	// Stale should be gone
	stale, _ := s.GetSearchCache(context.Background(), "stale", "test")
	if stale != nil {
		t.Error("stale entry should have been deleted")
	}

	// Fresh should remain
	fresh, _ := s.GetSearchCache(context.Background(), "fresh", "test")
	if fresh == nil {
		t.Error("fresh entry should still exist")
	}
}

// ===========================================================================
// ResetAllServerStatuses
// ===========================================================================

func TestResetAllServerStatuses(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	// Add servers with different statuses
	_, _ = s.AddServer(context.Background(), ServerRecord{Address: "irc.one.net", Port: 6667, Status: "connected"})
	_, _ = s.AddServer(context.Background(), ServerRecord{Address: "irc.two.net", Port: 6697, Status: "reconnecting"})
	_, _ = s.AddServer(context.Background(), ServerRecord{Address: "irc.three.net", Port: 6667, Status: "disconnected"})

	err := s.ResetAllServerStatuses(context.Background())
	if err != nil {
		t.Fatalf("ResetAllServerStatuses: %v", err)
	}

	servers, _ := s.ListServers(context.Background())
	for _, srv := range servers {
		if srv.Status != "disconnected" {
			t.Errorf("server %s: expected status 'disconnected', got %s", srv.Address, srv.Status)
		}
	}
}

// ===========================================================================
// SetServerStatus / SetServerConnected
// ===========================================================================

func TestSetServerStatus(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.AddServer(context.Background(), ServerRecord{Address: "irc.status.net", Port: 6667, Status: "disconnected"})

	err := s.SetServerStatus(context.Background(), id, "connected")
	if err != nil {
		t.Fatalf("SetServerStatus: %v", err)
	}

	srv, _ := s.GetServer(context.Background(), id)
	if srv.Status != "connected" {
		t.Errorf("expected status 'connected', got %s", srv.Status)
	}
}

func TestSetServerConnected(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.AddServer(context.Background(), ServerRecord{Address: "irc.conn.net", Port: 6667, Status: "reconnecting", RetryCount: 5})

	err := s.SetServerConnected(context.Background(), id)
	if err != nil {
		t.Fatalf("SetServerConnected: %v", err)
	}

	srv, _ := s.GetServer(context.Background(), id)
	if srv.Status != "connected" {
		t.Errorf("expected status 'connected', got %s", srv.Status)
	}
	if srv.RetryCount != 0 {
		t.Errorf("expected retry_count 0 after connected, got %d", srv.RetryCount)
	}
	if srv.LastConnectedAt == nil {
		t.Error("expected last_connected_at to be set")
	}
}

// ===========================================================================
// GetTotalDownloadedBytes
// ===========================================================================

func TestGetTotalDownloadedBytes_Empty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	total, err := s.GetTotalDownloadedBytes(context.Background())
	if err != nil {
		t.Fatalf("GetTotalDownloadedBytes: %v", err)
	}
	if total != 0 {
		t.Errorf("expected 0, got %d", total)
	}
}

func TestGetTotalDownloadedBytes_WithProgress(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id1, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "a.mkv", FileSize: 1000,
	})
	_ = s.UpdateDownloadProgress(context.Background(), id1, 500, 0)

	id2, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x", Filename: "b.mkv", FileSize: 2000,
	})
	_ = s.UpdateDownloadProgress(context.Background(), id2, 300, 0)

	total, err := s.GetTotalDownloadedBytes(context.Background())
	if err != nil {
		t.Fatalf("GetTotalDownloadedBytes: %v", err)
	}
	if total != 800 {
		t.Errorf("expected 800 (500+300), got %d", total)
	}
}

// ===========================================================================
// FindDuplicateDownload
// ===========================================================================

func TestFindDuplicateDownload_Found(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	_, _ = s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "DupBot", ServerAddress: "irc.dup.net", Channel: "#xdcc",
		PackMessage: "xdcc send #42", Filename: "dup.mkv", FileSize: 1000,
	})

	dup, err := s.FindDuplicateDownload(context.Background(), "DupBot", "irc.dup.net", 42)
	if err != nil {
		t.Fatalf("FindDuplicateDownload: %v", err)
	}
	if dup == nil {
		t.Fatal("expected to find duplicate, got nil")
	}
	if dup.Bot != "DupBot" {
		t.Errorf("expected bot DupBot, got %s", dup.Bot)
	}
}

func TestFindDuplicateDownload_NotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	dup, err := s.FindDuplicateDownload(context.Background(), "NoBot", "irc.nope.net", 1)
	if err != nil {
		t.Fatalf("FindDuplicateDownload: %v", err)
	}
	if dup != nil {
		t.Errorf("expected nil for non-existent duplicate, got %+v", dup)
	}
}

func TestFindDuplicateDownload_DifferentServer(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	_, _ = s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "GlobalBot", ServerAddress: "irc.one.net", Channel: "#x",
		PackMessage: "xdcc send #1", Filename: "f.mkv", FileSize: 100,
	})

	// Same bot, same pack number, but different server — should NOT match
	dup, _ := s.FindDuplicateDownload(context.Background(), "GlobalBot", "irc.two.net", 1)
	if dup != nil {
		t.Errorf("expected nil for different server, got %+v", dup)
	}
}

// ===========================================================================
// FilenamesExist
// ===========================================================================

func TestFilenamesExist_EmptyInput(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	result, err := s.FilenamesExist(context.Background(), nil)
	if err != nil {
		t.Fatalf("FilenamesExist(nil): %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result))
	}

	result, err = s.FilenamesExist(context.Background(), []string{})
	if err != nil {
		t.Fatalf("FilenamesExist([]): %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result))
	}
}

func TestFilenamesExist_Found(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "exists.mkv", FileSize: 100,
	})
	_ = s.MarkDownloadCompleted(context.Background(), id, "", 0)

	result, err := s.FilenamesExist(context.Background(), []string{"exists.mkv", "missing.mkv"})
	if err != nil {
		t.Fatalf("FilenamesExist: %v", err)
	}
	if !result["exists.mkv"] {
		t.Error("expected exists.mkv to be found")
	}
	if result["missing.mkv"] {
		t.Error("expected missing.mkv to not be found")
	}
}

func TestFilenamesExist_CaseInsensitive(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "Video.MKV", FileSize: 100,
	})
	_ = s.MarkDownloadCompleted(context.Background(), id, "", 0)

	result, err := s.FilenamesExist(context.Background(), []string{"video.mkv"})
	if err != nil {
		t.Fatalf("FilenamesExist: %v", err)
	}
	if !result["video.mkv"] {
		t.Error("expected case-insensitive match for video.mkv")
	}
}

func TestFilenamesExist_Deduplication(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.EnqueueDownload(context.Background(), DownloadRecord{
		Bot: "Bot", ServerAddress: "irc.t.net", Channel: "#x",
		Filename: "dup.mkv", FileSize: 100,
	})
	_ = s.MarkDownloadCompleted(context.Background(), id, "", 0)

	// Send duplicate filenames — should handle gracefully
	result, err := s.FilenamesExist(context.Background(), []string{"dup.mkv", "dup.mkv", "other.mkv"})
	if err != nil {
		t.Fatalf("FilenamesExist with duplicates: %v", err)
	}
	if !result["dup.mkv"] {
		t.Error("expected dup.mkv to be found")
	}
	if result["other.mkv"] {
		t.Error("expected other.mkv to not be found")
	}
}

// ===========================================================================
// UpdateChannelTopic
// ===========================================================================

func TestUpdateChannelTopic(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	srvID, _ := s.AddServer(context.Background(), ServerRecord{Address: "irc.topic.net", Port: 6667})
	chID, _ := s.AddChannel(context.Background(), ChannelRecord{ServerID: srvID, Name: "#topictest", Topic: "Welcome"})

	err := s.UpdateChannelTopic(context.Background(), chID, "Updated topic here")
	if err != nil {
		t.Fatalf("UpdateChannelTopic: %v", err)
	}

	ch, _ := s.GetChannelsByServerAndName(context.Background(), srvID, "#topictest")
	if ch.Topic != "Updated topic here" {
		t.Errorf("expected topic 'Updated topic here', got %s", ch.Topic)
	}
}

// ===========================================================================
// SetWatchlistNotified
// ===========================================================================

func TestSetWatchlistNotified(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.AddWatchlist(context.Background(), Watchlist{Name: "NotifyTest", Query: "test", Enabled: true})

	err := s.SetWatchlistNotified(context.Background(), id)
	if err != nil {
		t.Fatalf("SetWatchlistNotified: %v", err)
	}

	w, _ := s.GetWatchlist(context.Background(), id)
	if w.LastNotifiedAt == nil {
		t.Error("expected last_notified_at to be set")
	}
}

// ===========================================================================
// CreateBackup
// ===========================================================================

func TestCreateBackup(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	// Must be in a writable temp dir (backup path must be absolute)
	backupPath, err := s.CreateBackup(context.Background())
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Errorf("backup file should exist at %s", backupPath)
	}
	if !strings.HasPrefix(backupPath, s.dbPath+".backup.") {
		t.Errorf("unexpected backup path format: %s", backupPath)
	}
}

// ===========================================================================
// ExportToFile / ImportFromFile
// ===========================================================================

func TestExportToFile_ImportFromFile(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	// Add some data
	_, _ = s.AddServer(context.Background(), ServerRecord{Address: "irc.fileio.net", Port: 6667, Status: "connected"})
	_, _ = s.AddWatchlist(context.Background(), Watchlist{Name: "FileIO WL", Query: "test", Enabled: true})

	// Export to temp file
	exportPath := filepath.Join(t.TempDir(), "export.json")
	err := s.ExportToFile(context.Background(), exportPath)
	if err != nil {
		t.Fatalf("ExportToFile: %v", err)
	}

	// Verify the file exists and is valid JSON
	data, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatalf("reading export file: %v", err)
	}
	if len(data) < 10 {
		t.Errorf("export file too small (%d bytes)", len(data))
	}

	// Import into fresh store
	s2 := newTestStore(t)
	err = s2.ImportFromFile(context.Background(), exportPath)
	if err != nil {
		t.Fatalf("ImportFromFile: %v", err)
	}

	// Verify imported data
	servers, _ := s2.ListServers(context.Background())
	if len(servers) != 1 {
		t.Errorf("expected 1 server imported, got %d", len(servers))
	}
	if servers[0].Address != "irc.fileio.net" {
		t.Errorf("expected address 'irc.fileio.net', got %s", servers[0].Address)
	}

	wls, _ := s2.ListWatchlists(context.Background())
	if len(wls) != 1 {
		t.Errorf("expected 1 watchlist imported, got %d", len(wls))
	}
}

func TestImportFromFile_NotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	err := s.ImportFromFile(context.Background(), "/nonexistent/export.json")
	if err == nil {
		t.Error("expected error for non-existent import file")
	}
}

func TestImportFromFile_InvalidJSON(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	invalidPath := filepath.Join(t.TempDir(), "invalid.json")
	_ = os.WriteFile(invalidPath, []byte("not json"), 0o644)

	err := s.ImportFromFile(context.Background(), invalidPath)
	if err == nil {
		t.Error("expected error for invalid JSON import")
	}
}

func TestCleanupOldBackups(t *testing.T) {
	t.Parallel()

	// --- maxCount < 1: no-op ---
	t.Run("ZeroMaxCount", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		// Create some backup files that should NOT be deleted
		for i := 0; i < 5; i++ {
			_ = os.WriteFile(fmt.Sprintf("%s.backup.%d", dbPath, i), []byte("data"), 0o644)
		}
		cleanupOldBackups(dbPath, 0)
		// All files should survive
		entries, _ := os.ReadDir(tmpDir)
		if len(entries) != 5 {
			t.Errorf("expected 5 files after cleanup with maxCount=0, got %d", len(entries))
		}
	})

	// --- maxCount negative: no-op ---
	t.Run("NegativeMaxCount", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		for i := 0; i < 3; i++ {
			_ = os.WriteFile(fmt.Sprintf("%s.backup.%d", dbPath, i), []byte("data"), 0o644)
		}
		cleanupOldBackups(dbPath, -1)
		entries, _ := os.ReadDir(tmpDir)
		if len(entries) != 3 {
			t.Errorf("expected 3 files after cleanup with maxCount=-1, got %d", len(entries))
		}
	})

	// --- fewer backups than maxCount: keep all ---
	t.Run("FewerThanMax", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		for i := 0; i < 2; i++ {
			_ = os.WriteFile(fmt.Sprintf("%s.backup.%d", dbPath, i), []byte("data"), 0o644)
		}
		cleanupOldBackups(dbPath, 3)
		entries, _ := os.ReadDir(tmpDir)
		if len(entries) != 2 {
			t.Errorf("expected 2 files when fewer than max, got %d", len(entries))
		}
	})

	// --- exactly at maxCount: keep all ---
	t.Run("ExactlyMax", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")
		for i := 0; i < 3; i++ {
			_ = os.WriteFile(fmt.Sprintf("%s.backup.%d", dbPath, i), []byte("data"), 0o644)
		}
		cleanupOldBackups(dbPath, 3)
		entries, _ := os.ReadDir(tmpDir)
		if len(entries) != 3 {
			t.Errorf("expected 3 files at exactly max, got %d", len(entries))
		}
	})

	// --- more than maxCount: oldest deleted, recent kept ---
	t.Run("ExceedsMax", func(t *testing.T) {
		// No t.Parallel() — uses deterministic file creation order
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")

		// Create 5 backups
		for i := 0; i < 5; i++ {
			_ = os.WriteFile(fmt.Sprintf("%s.backup.%d", dbPath, i), []byte("data"), 0o644)
		}

		// Cleanup keeping only 2 most recent
		cleanupOldBackups(dbPath, 2)

		// Check only 2 files remain
		entries, _ := os.ReadDir(tmpDir)
		if len(entries) != 2 {
			t.Errorf("expected 2 files after cleanup, got %d", len(entries))
		}
	})

	// --- directories mixed in: not removed ---
	t.Run("DirectoriesMixed", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")

		// Create a subdirectory that starts with the backup prefix
		_ = os.Mkdir(filepath.Join(tmpDir, "test.db.backup.dir"), 0o755)
		// Create backup files
		for i := 0; i < 5; i++ {
			_ = os.WriteFile(fmt.Sprintf("%s.backup.%d", dbPath, i), []byte("data"), 0o644)
		}

		cleanupOldBackups(dbPath, 2)

		// Directory should survive
		if _, err := os.Stat(filepath.Join(tmpDir, "test.db.backup.dir")); err != nil {
			t.Error("directory should not be removed by cleanup")
		}
	})

	// --- non-matching files: not removed ---
	t.Run("NonMatchingFiles", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "test.db")

		// Create a mix: 5 backups + 3 other files
		for i := 0; i < 5; i++ {
			_ = os.WriteFile(fmt.Sprintf("%s.backup.%d", dbPath, i), []byte("data"), 0o644)
		}
		_ = os.WriteFile(filepath.Join(tmpDir, "other.txt"), []byte("other"), 0o644)
		_ = os.WriteFile(filepath.Join(tmpDir, "test.db-journal"), []byte("journal"), 0o644)

		cleanupOldBackups(dbPath, 2)

		// Non-matching files survive
		if _, err := os.Stat(filepath.Join(tmpDir, "other.txt")); err != nil {
			t.Error("non-matching file 'other.txt' should survive")
		}
		if _, err := os.Stat(filepath.Join(tmpDir, "test.db-journal")); err != nil {
			t.Error("non-matching file 'test.db-journal' should survive")
		}
	})

	// --- non-existent directory: silent return ---
	t.Run("NonExistentDir", func(t *testing.T) {
		t.Parallel()
		// Should not panic
		cleanupOldBackups("/nonexistent/path/db.sqlite", 3)
	})

	// --- backupPrefix matching at directory boundary ---
	t.Run("PrefixAtDirBoundary", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "subdir", "test.db")
		_ = os.MkdirAll(filepath.Dir(dbPath), 0o755)

		for i := 0; i < 5; i++ {
			_ = os.WriteFile(fmt.Sprintf("%s.backup.%d", dbPath, i), []byte("data"), 0o644)
		}

		cleanupOldBackups(dbPath, 2)

		entries, _ := os.ReadDir(filepath.Dir(dbPath))
		if len(entries) != 2 {
			t.Errorf("expected 2 files in subdir after cleanup, got %d", len(entries))
		}
	})
}
