package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"xdcc_server/internal/config"
	"xdcc_server/internal/logging"
	"xdcc_server/internal/metrics"
	"xdcc_server/internal/searchagg"
	"xdcc_server/internal/sse"
	"xdcc_server/internal/store"
)

// ===========================================================================
// Test API setup
// ===========================================================================

type testAPI struct {
	api    *API
	store  *store.SQLiteStore
	hub    *sse.Hub
	router http.Handler
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()

	// Create a leveled logger for the API (debug level so all logs are visible in tests)
	apiLogger := logging.New(logging.LevelDebug, "", 0)

	// Copy pre-migrated template DB to avoid expensive Migrate() per test.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if templateDBPath == "" {
		t.Fatal("templateDBPath is empty — did TestMain run?")
	}
	if err := copyFile(templateDBPath, dbPath); err != nil {
		t.Fatalf("copying template DB: %v", err)
	}

	// Open the copied database through NewSQLiteStore (sets up WAL, foreign keys,
	// connection pool). Migrate() is NOT called because the template already has
	// all migrations applied — it would be a fast no-op anyway.
	st, err := store.NewSQLiteStore(dbPath, apiLogger)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	// Speed up tests: disable fsync and WAL flushing (no durability needed).
	// These are per-connection settings, not persisted in the file copy.
	if _, err := st.DB().Exec("PRAGMA synchronous=OFF"); err != nil {
		st.Close()
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if _, err := st.DB().Exec("PRAGMA journal_mode=MEMORY"); err != nil {
		st.Close()
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Security.AdminToken = testAdminToken
	cfg.HTTP.CORSOrigins = []string{"http://localhost:5173"}

	hub := sse.NewHub(50)

	// Create a real searchagg.Aggregator so preset/watchlist CRUD handlers
	// don't return 503. This uses the same store, so CRUD operations work.
	agg := searchagg.New(st, &cfg.Search, apiLogger)

	met := metrics.New()
	sseDebugLogger := logging.New(logging.LevelDebug, "", 0)
	api := New(st, nil, nil, agg, hub, nil, cfg, "", apiLogger, met, sseDebugLogger)

	router := api.Router()

	ta := &testAPI{
		api:    api,
		store:  st,
		hub:    hub,
		router: router,
	}

	t.Cleanup(func() {
		agg.Stop()
		hub.Close()
		st.Close()
	})

	return ta
}

// request is a helper to make an HTTP request and return the response.
// All requests include the test admin token since the API now requires
// authentication for protected SYSTEM endpoints.
const testAdminToken = "test-integration-token"

func (ta *testAPI) request(t *testing.T, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()

	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encoding body: %v", err)
		}
	}

	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Admin-Token", testAdminToken)

	w := httptest.NewRecorder()
	ta.router.ServeHTTP(w, req)
	return w
}

// ===========================================================================
// Health / Version
// ===========================================================================

func TestHealthz(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/healthz", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("expected status 'ok', got %s", resp["status"])
	}
}

func TestVersion(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/version", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]string
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["version"] == "" {
		t.Errorf("expected version to be set, got empty")
	}
}

func TestReadyz(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/readyz", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// ===========================================================================
// Download CRUD via API
// ===========================================================================

func TestEnqueueDownload_Success(t *testing.T) {
	ta := newTestAPI(t)

	body := map[string]interface{}{
		"pack_message":   "xdcc send #1",
		"bot":            "TestBot",
		"server_address": "irc.test.net",
		"channel":        "#xdcc",
		"filename":       "test.mkv",
		"file_size":      1000,
	}
	w := ta.request(t, "POST", "/api/downloads", body)
	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]int64
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["id"] <= 0 {
		t.Errorf("expected positive id, got %d", resp["id"])
	}
}

func TestEnqueueDownload_TLTBotServerOverride(t *testing.T) {
	ta := newTestAPI(t)

	body := map[string]interface{}{
		"pack_message":   "xdcc send #1",
		"bot":            "TLTBot",
		"server_address": "irc.rizon.net",
		"channel":        "#xdcc",
		"filename":       "test.mkv",
		"file_size":      1000,
	}
	createResp := ta.request(t, "POST", "/api/downloads", body)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", createResp.Code, createResp.Body.String())
	}

	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	// Verify the stored server address was corrected to irc.williamgattone.it
	getResp := ta.request(t, "GET", "/api/downloads/"+itoa(id), nil)
	var dl store.DownloadRecord
	_ = json.NewDecoder(getResp.Body).Decode(&dl)
	if dl.ServerAddress != "irc.williamgattone.it" {
		t.Errorf("expected server_address irc.williamgattone.it for TLT bot, got %s", dl.ServerAddress)
	}
}

func TestEnqueueDownload_WeCBotServerOverride(t *testing.T) {
	ta := newTestAPI(t)

	body := map[string]interface{}{
		"pack_message":   "xdcc send #1",
		"bot":            "WeCBot",
		"server_address": "irc.rizon.net",
		"channel":        "#xdcc",
		"filename":       "test.mkv",
		"file_size":      1000,
	}
	createResp := ta.request(t, "POST", "/api/downloads", body)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", createResp.Code, createResp.Body.String())
	}

	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	// Verify the stored server address was corrected to irc.explosionirc.net
	getResp := ta.request(t, "GET", "/api/downloads/"+itoa(id), nil)
	var dl store.DownloadRecord
	_ = json.NewDecoder(getResp.Body).Decode(&dl)
	if dl.ServerAddress != "irc.explosionirc.net" {
		t.Errorf("expected server_address irc.explosionirc.net for WeC bot, got %s", dl.ServerAddress)
	}
}

func TestEnqueueDownload_MissingFields(t *testing.T) {
	ta := newTestAPI(t)

	tests := []struct {
		name string
		body map[string]interface{}
	}{
		{"missing pack_message", map[string]interface{}{"bot": "Bot", "server_address": "irc.t.net", "channel": "#x"}},
		{"missing bot", map[string]interface{}{"pack_message": "xdcc send #1", "server_address": "irc.t.net", "channel": "#x"}},
		{"missing server_address", map[string]interface{}{"pack_message": "xdcc send #1", "bot": "Bot", "channel": "#x"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := ta.request(t, "POST", "/api/downloads", tt.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestEnqueueDownload_OptionalChannel(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message":   "xdcc send #1",
		"bot":            "TestBot",
		"server_address": "irc.test.net",
		"filename":       "test.mkv",
		"file_size":      1000,
	})
	if w.Code != http.StatusCreated {
		t.Errorf("expected 201 when channel is omitted, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListServers_IncludesChannelCount(t *testing.T) {
	ta := newTestAPI(t)

	// Add a server with some channels
	id, err := ta.store.AddServer(context.Background(), store.ServerRecord{
		Address: "irc.test.net",
		Port:    6667,
		Status:  "disconnected",
	})
	if err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	_, err = ta.store.AddChannel(context.Background(), store.ChannelRecord{ServerID: id, Name: "#channel1", Joined: true})
	if err != nil {
		t.Fatalf("AddChannel: %v", err)
	}
	_, err = ta.store.AddChannel(context.Background(), store.ChannelRecord{ServerID: id, Name: "#channel2", Joined: false})
	if err != nil {
		t.Fatalf("AddChannel: %v", err)
	}

	w := ta.request(t, "GET", "/api/servers", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp []map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp) != 1 {
		t.Fatalf("expected 1 server, got %d", len(resp))
	}
	if resp[0]["channel_count"] != float64(1) {
		t.Errorf("expected channel_count=1 (only joined), got %v", resp[0]["channel_count"])
	}
}

func TestListDownloads_Empty(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/downloads", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)

	downloads, ok := resp["downloads"].([]interface{})
	if !ok {
		t.Fatal("expected 'downloads' array")
	}
	if len(downloads) != 0 {
		t.Errorf("expected empty downloads, got %d", len(downloads))
	}
}

func TestListDownloads_WithItems(t *testing.T) {
	ta := newTestAPI(t)

	// Enqueue two downloads
	ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "Bot1",
		"server_address": "irc.t.net", "channel": "#a", "filename": "a.mkv", "file_size": 100,
	})
	ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #2", "bot": "Bot2",
		"server_address": "irc.t.net", "channel": "#b", "filename": "b.mkv", "file_size": 200,
	})

	w := ta.request(t, "GET", "/api/downloads", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)

	downloads := resp["downloads"].([]interface{})
	if len(downloads) != 2 {
		t.Errorf("expected 2 downloads, got %d", len(downloads))
	}
}

func TestListDownloads_IncludesRecentCompleted(t *testing.T) {
	ta := newTestAPI(t)

	// Enqueue a download, then complete it
	createResp := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "Bot",
		"server_address": "irc.t.net", "channel": "#x", "filename": "f.mkv", "file_size": 100,
	})
	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	_ = ta.store.MarkDownloadStarted(context.Background(), id)
	_ = ta.store.MarkDownloadCompleted(context.Background(), id, "", 0)

	// The completed download should still appear in the list
	w := ta.request(t, "GET", "/api/downloads", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)

	downloads := resp["downloads"].([]interface{})
	if len(downloads) != 1 {
		t.Errorf("expected 1 download (completed), got %d", len(downloads))
	}

	// Verify the record has status 'completed'
	dlJSON, _ := json.Marshal(downloads[0])
	var dl store.DownloadRecord
	_ = json.Unmarshal(dlJSON, &dl)
	if dl.Status != store.DownloadStatusCompleted {
		t.Errorf("expected status 'completed', got %s", dl.Status)
	}
}

func TestGetDownload_Found(t *testing.T) {
	ta := newTestAPI(t)

	// Enqueue
	createResp := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "Bot",
		"server_address": "irc.t.net", "channel": "#x", "filename": "f.mkv", "file_size": 100,
	})
	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	// Get by ID
	w := ta.request(t, "GET", "/api/downloads/"+itoa(id), nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var dl store.DownloadRecord
	_ = json.NewDecoder(w.Body).Decode(&dl)
	if dl.Bot != "Bot" {
		t.Errorf("expected bot 'Bot', got %s", dl.Bot)
	}
	if dl.Status != store.DownloadStatusQueued {
		t.Errorf("expected status 'queued', got %s", dl.Status)
	}
}

func TestGetDownload_NotFound(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/downloads/999", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPauseAndResumeDownload(t *testing.T) {
	ta := newTestAPI(t)

	createResp := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "Bot",
		"server_address": "irc.t.net", "channel": "#x", "filename": "f.mkv", "file_size": 100,
	})
	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	// Pause
	pauseResp := ta.request(t, "POST", "/api/downloads/"+itoa(id)+"/pause", nil)
	if pauseResp.Code != http.StatusOK {
		t.Errorf("expected 200 on pause, got %d: %s", pauseResp.Code, pauseResp.Body.String())
	}

	// Resume
	resumeResp := ta.request(t, "POST", "/api/downloads/"+itoa(id)+"/resume", nil)
	if resumeResp.Code != http.StatusOK {
		t.Errorf("expected 200 on resume, got %d: %s", resumeResp.Code, resumeResp.Body.String())
	}
}

func TestRetryDownload(t *testing.T) {
	ta := newTestAPI(t)

	createResp := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "Bot",
		"server_address": "irc.t.net", "channel": "#x", "filename": "f.mkv", "file_size": 100,
	})
	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	// Mark as failed manually
	_ = ta.store.MarkDownloadFailed(context.Background(), id, "test error")

	// Retry via API
	w := ta.request(t, "POST", "/api/downloads/"+itoa(id)+"/retry", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 on retry, got %d: %s", w.Code, w.Body.String())
	}

	// Verify status is now queued
	getResp := ta.request(t, "GET", "/api/downloads/"+itoa(id), nil)
	var dl store.DownloadRecord
	_ = json.NewDecoder(getResp.Body).Decode(&dl)
	if dl.Status != store.DownloadStatusQueued {
		t.Errorf("expected status 'queued' after retry, got %s", dl.Status)
	}
}

func TestRetryDownload_Completed(t *testing.T) {
	ta := newTestAPI(t)

	createResp := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "Bot",
		"server_address": "irc.t.net", "channel": "#x", "filename": "f.mkv", "file_size": 100,
	})
	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	// Mark as completed
	_ = ta.store.MarkDownloadStarted(context.Background(), id)
	_ = ta.store.MarkDownloadCompleted(context.Background(), id, "", 0)

	// Retry via API — should succeed now that RetryDownload accepts 'completed'
	w := ta.request(t, "POST", "/api/downloads/"+itoa(id)+"/retry", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 on retry of completed download, got %d: %s", w.Code, w.Body.String())
	}

	// Verify status is now queued
	getResp := ta.request(t, "GET", "/api/downloads/"+itoa(id), nil)
	var dl store.DownloadRecord
	_ = json.NewDecoder(getResp.Body).Decode(&dl)
	if dl.Status != store.DownloadStatusQueued {
		t.Errorf("expected status 'queued' after retry of completed, got %s", dl.Status)
	}
}

func TestRemoveDownload(t *testing.T) {
	ta := newTestAPI(t)

	createResp := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "Bot",
		"server_address": "irc.t.net", "channel": "#x", "filename": "f.mkv", "file_size": 100,
	})
	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	w := ta.request(t, "DELETE", "/api/downloads/"+itoa(id), nil)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBulkDownloads(t *testing.T) {
	ta := newTestAPI(t)

	// Create two downloads
	createResp1 := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "Bot1",
		"server_address": "irc.t.net", "channel": "#a", "filename": "a.mkv", "file_size": 100,
	})
	var d1 map[string]int64
	_ = json.NewDecoder(createResp1.Body).Decode(&d1)

	createResp2 := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #2", "bot": "Bot2",
		"server_address": "irc.t.net", "channel": "#b", "filename": "b.mkv", "file_size": 200,
	})
	var d2 map[string]int64
	_ = json.NewDecoder(createResp2.Body).Decode(&d2)

	// Bulk pause
	w := ta.request(t, "POST", "/api/downloads/bulk", map[string]interface{}{
		"ids":    []int64{d1["id"], d2["id"]},
		"action": "pause",
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["successes"].(float64) != 2 {
		t.Errorf("expected 2 successes, got %v", resp["successes"])
	}
}

func TestBulkDownloads_InvalidAction(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "POST", "/api/downloads/bulk", map[string]interface{}{
		"ids":    []int64{1},
		"action": "invalid",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSetDownloadPosition(t *testing.T) {
	ta := newTestAPI(t)

	createResp := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "Bot",
		"server_address": "irc.t.net", "channel": "#x", "filename": "f.mkv", "file_size": 100,
	})
	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	w := ta.request(t, "PATCH", "/api/downloads/"+itoa(id)+"/position", map[string]interface{}{
		"priority": 1,
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify priority was updated
	getResp := ta.request(t, "GET", "/api/downloads/"+itoa(id), nil)
	var dl store.DownloadRecord
	_ = json.NewDecoder(getResp.Body).Decode(&dl)
	if dl.Priority != 1 {
		t.Errorf("expected priority 1, got %d", dl.Priority)
	}
}

// ===========================================================================
// Download History
// ===========================================================================

func TestDownloadHistory_Empty(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/downloads/history", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 0 {
		t.Errorf("expected total 0, got %v", resp["total"])
	}
}

func TestDownloadHistory_WithItems(t *testing.T) {
	ta := newTestAPI(t)

	// Enqueue and complete a download
	createResp := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "Bot",
		"server_address": "irc.t.net", "channel": "#x", "filename": "f.mkv", "file_size": 100,
	})
	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	_ = ta.store.MarkDownloadStarted(context.Background(), id)
	_ = ta.store.MarkDownloadCompleted(context.Background(), id, "", 0)

	w := ta.request(t, "GET", "/api/downloads/history", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 1 {
		t.Errorf("expected total 1, got %v", resp["total"])
	}
}

// ===========================================================================
// Stats
// ===========================================================================

func TestStats(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/stats", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["uptime"] == "" {
		t.Errorf("expected uptime to be set")
	}
	if resp["go_version"] == "" {
		t.Errorf("expected go_version to be set")
	}
}

func TestStatus(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/status", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// ===========================================================================
// XDCC Parse
// ===========================================================================

func TestParseXDCC(t *testing.T) {
	ta := newTestAPI(t)

	tests := []struct {
		name     string
		command  string
		wantBot  string
		wantPack int
	}{
		{"full msg command", "/msg BotName XDCC SEND #42", "BotName", 42},
		{"lowercase", "/msg BotName xdcc send #7", "BotName", 7},
		{"bot: format", "BotName: xdcc send #5", "BotName", 5},
		{"simple", "xdcc send #3", "", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := ta.request(t, "POST", "/api/xdcc/parse", map[string]interface{}{
				"command": tt.command,
			})
			if w.Code != http.StatusOK {
				t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
				return
			}

			var resp map[string]interface{}
			_ = json.NewDecoder(w.Body).Decode(&resp)
			if tt.wantPack > 0 && resp["pack_number"].(float64) != float64(tt.wantPack) {
				t.Errorf("expected pack_number %d, got %v", tt.wantPack, resp["pack_number"])
			}
		})
	}
}

func TestParseXDCC_EmptyCommand(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "POST", "/api/xdcc/parse", map[string]interface{}{
		"command": "",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty command, got %d: %s", w.Code, w.Body.String())
	}
}

// ===========================================================================
// Search Presets CRUD via API
// ===========================================================================

func TestPresetsCRUD(t *testing.T) {
	ta := newTestAPI(t)

	// Create
	createResp := ta.request(t, "POST", "/api/search/presets", map[string]interface{}{
		"name":       "Test Preset",
		"query":      "anime 1080p",
		"is_default": true,
	})
	if createResp.Code != http.StatusCreated {
		t.Errorf("expected 201 on create, got %d: %s", createResp.Code, createResp.Body.String())
	}

	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	// List
	listResp := ta.request(t, "GET", "/api/search/presets", nil)
	if listResp.Code != http.StatusOK {
		t.Errorf("expected 200 on list, got %d", listResp.Code)
	}

	var presets []store.SearchPreset
	_ = json.NewDecoder(listResp.Body).Decode(&presets)
	if len(presets) != 1 {
		t.Errorf("expected 1 preset, got %d", len(presets))
	}

	// Update
	updateResp := ta.request(t, "PUT", "/api/search/presets/"+itoa(id), map[string]interface{}{
		"name":  "Updated Preset",
		"query": "updated query",
	})
	if updateResp.Code != http.StatusOK {
		t.Errorf("expected 200 on update, got %d: %s", updateResp.Code, updateResp.Body.String())
	}

	// Delete
	deleteResp := ta.request(t, "DELETE", "/api/search/presets/"+itoa(id), nil)
	if deleteResp.Code != http.StatusNoContent {
		t.Errorf("expected 204 on delete, got %d", deleteResp.Code)
	}
}

// ===========================================================================
// Watchlists CRUD via API
// ===========================================================================

func TestWatchlistsCRUD(t *testing.T) {
	ta := newTestAPI(t)

	// Create
	createResp := ta.request(t, "POST", "/api/watchlists", map[string]interface{}{
		"name":    "Test Watchlist",
		"query":   "anime 1080p",
		"enabled": true,
	})
	if createResp.Code != http.StatusCreated {
		t.Errorf("expected 201 on create, got %d: %s", createResp.Code, createResp.Body.String())
	}

	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	// List
	listResp := ta.request(t, "GET", "/api/watchlists", nil)
	if listResp.Code != http.StatusOK {
		t.Errorf("expected 200 on list, got %d", listResp.Code)
	}

	var watchlists []store.Watchlist
	_ = json.NewDecoder(listResp.Body).Decode(&watchlists)
	if len(watchlists) != 1 {
		t.Errorf("expected 1 watchlist, got %d", len(watchlists))
	}

	// Update
	updateResp := ta.request(t, "PUT", "/api/watchlists/"+itoa(id), map[string]interface{}{
		"name":    "Updated WL",
		"query":   "updated query",
		"enabled": false,
	})
	if updateResp.Code != http.StatusOK {
		t.Errorf("expected 200 on update, got %d: %s", updateResp.Code, updateResp.Body.String())
	}

	// Delete
	deleteResp := ta.request(t, "DELETE", "/api/watchlists/"+itoa(id), nil)
	if deleteResp.Code != http.StatusNoContent {
		t.Errorf("expected 204 on delete, got %d", deleteResp.Code)
	}
}

func TestCreateWatchlist_MissingFields(t *testing.T) {
	ta := newTestAPI(t)

	// Missing name
	w := ta.request(t, "POST", "/api/watchlists", map[string]interface{}{
		"query": "test",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing name, got %d", w.Code)
	}

	// Missing query
	w = ta.request(t, "POST", "/api/watchlists", map[string]interface{}{
		"name": "Test",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing query, got %d", w.Code)
	}
}

func TestCreatePreset_MissingFields(t *testing.T) {
	ta := newTestAPI(t)

	// Missing name
	w := ta.request(t, "POST", "/api/search/presets", map[string]interface{}{
		"query": "test",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing name, got %d", w.Code)
	}

	// Missing query
	w = ta.request(t, "POST", "/api/search/presets", map[string]interface{}{
		"name": "Test",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing query, got %d", w.Code)
	}
}

// ===========================================================================
// Admin export/import
// ===========================================================================

func TestAdminExport_Empty(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "POST", "/api/admin/export", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["exported_at"] == "" {
		t.Errorf("expected exported_at to be set")
	}
}

// ===========================================================================
// Config
// ===========================================================================

func TestGetConfig(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/config", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestUpdateConfig(t *testing.T) {
	ta := newTestAPI(t)

	// The backend unmarshals the body into a blank Config and validates
	// ALL fields, so we must send a complete valid config.
	w := ta.request(t, "PUT", "/api/config", map[string]interface{}{
		"irc":      map[string]interface{}{"nickname": "xdcc-test"},
		"http":     map[string]interface{}{"port": 8080, "bind_address": "127.0.0.1"},
		"download": map[string]interface{}{"max_parallel_total": 3, "conflict_policy": "overwrite", "temp_dir": "/tmp/dl", "dest_dir": "/tmp/dl", "fail_fallback": "suggest_only"},
		"storage":  map[string]interface{}{"db_path": "/tmp/db", "downloads_retention": "30d", "cleanup_interval": "12h"},
		"logging":  map[string]interface{}{"level": "info"},
		"search":   map[string]interface{}{"provider_timeout": 5, "page_size": 50},
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if ta.api.Config.Download.MaxParallelTotal != 3 {
		t.Errorf("expected max_parallel=3, got %d", ta.api.Config.Download.MaxParallelTotal)
	}
}

// ===========================================================================
// SSE Events
// ===========================================================================

func TestSSEEndpoint(t *testing.T) {
	ta := newTestAPI(t)

	// Create a request with cancellable context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		ta.api.handleEvents(w, req)
		close(done)
	}()

	// Publish an event after a short delay
	time.Sleep(50 * time.Millisecond)
	ta.hub.Publish("test_event", map[string]interface{}{"msg": "hello"})

	// Cancel the context to stop the SSE handler
	cancel()
	select {
	case <-done:
		// Handler stopped
	case <-time.After(2 * time.Second):
		t.Fatal("SSE handler did not stop after context cancellation")
	}

	// Verify at least the connected event was sent
	body := w.Body.String()
	if !containsSubstring(body, "event: connected") {
		t.Errorf("expected SSE 'connected' event in body, got: %s", body)
	}
}

// ===========================================================================
// CORS headers
// ===========================================================================

func TestCORSPreflight(t *testing.T) {
	ta := newTestAPI(t)

	req := httptest.NewRequest(http.MethodOptions, "/healthz", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	w := httptest.NewRecorder()
	ta.router.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Errorf("expected CORS Allow-Origin to reflect localhost:5173, got %q",
			w.Header().Get("Access-Control-Allow-Origin"))
	}
}

// ===========================================================================
// Setup wizard
// ===========================================================================

func TestSetupStatus(t *testing.T) {
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/setup/status", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["setup_completed"] != false {
		t.Errorf("expected setup_completed=false initially, got %v", resp["setup_completed"])
	}
}

func TestSetupBootstrap(t *testing.T) {
	ta := newTestAPI(t)

	tempDir := t.TempDir()
	w := ta.request(t, "POST", "/api/setup/bootstrap", map[string]interface{}{
		"nickname":       "testuser",
		"server_address": "irc.test.net",
		"server_port":    6667,
		"download_dir":   filepath.Join(tempDir, "downloads"),
		"temp_dir":       filepath.Join(tempDir, "tmp"),
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify config was updated
	if ta.api.Config.IRC.Nickname != "testuser" {
		t.Errorf("expected nickname 'testuser', got %s", ta.api.Config.IRC.Nickname)
	}
	if !ta.api.Config.UI.SetupCompleted {
		t.Errorf("expected setup_completed=true after bootstrap")
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func containsSubstring(s, substr string) bool {
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
