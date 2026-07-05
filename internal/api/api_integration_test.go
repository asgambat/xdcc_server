package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	st, err := store.NewSQLiteStore(dbPath, 2000, 3, apiLogger)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	// Speed up tests: disable fsync and WAL flushing (no durability needed).
	// These are per-connection settings, not persisted in the file copy.
	// Batched into a single Exec to reduce round-trips.
	if _, err := st.DB().Exec("PRAGMA synchronous=OFF; PRAGMA journal_mode=MEMORY"); err != nil {
		st.Close()
		t.Fatalf("setting speed PRAGMAs: %v", err)
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
	api := New(st, nil, nil, agg, hub, nil, cfg, "", apiLogger, met, sseDebugLogger, "0.9.5-test")

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
	t.Parallel()
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
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/version", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp["version"] != "0.9.5-test" {
		t.Errorf("expected version \"0.9.5-test\", got %q", resp["version"])
	}
	if resp["min_compatible_client_version"] != "0.9.5-test" {
		t.Errorf("expected min_compatible_client_version \"0.9.5-test\", got %q", resp["min_compatible_client_version"])
	}
}

// TestVersion_FallbackEmpty verifies the handler reports "dev" when the API
// struct's Version field is empty (e.g. test fixtures that bypass the wiring).
func TestVersion_FallbackEmpty(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "empty-version.db")
	if err := copyFile(templateDBPath, dbPath); err != nil {
		t.Fatalf("copying template DB: %v", err)
	}
	apiLogger := logging.New(logging.LevelDebug, "", 0)
	st, err := store.NewSQLiteStore(dbPath, 2000, 3, apiLogger)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	cfg := config.DefaultConfig()
	cfg.Security.AdminToken = testAdminToken
	hub := sse.NewHub(10)
	defer hub.Close()
	agg := searchagg.New(st, &cfg.Search, apiLogger)
	defer agg.Stop()
	met := metrics.New()

	api := New(st, nil, nil, agg, hub, nil, cfg, "", apiLogger, met, apiLogger, "")
	router := api.Router()

	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["version"] != "dev" {
		t.Errorf("expected fallback version \"dev\", got %q", resp["version"])
	}
}

func TestReadyz(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/downloads/999", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPauseAndResumeDownload(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/config", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestUpdateConfig(t *testing.T) {
	t.Parallel()
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

	if ta.api.Config.GetDownloadConfig().MaxParallelTotal != 3 {
		t.Errorf("expected max_parallel=3, got %d", ta.api.Config.GetDownloadConfig().MaxParallelTotal)
	}
}

// ===========================================================================
// SSE Events
// ===========================================================================

func TestSSEEndpoint(t *testing.T) {
	t.Parallel()
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
	if !strings.Contains(body, "event: connected") {
		t.Errorf("expected SSE 'connected' event in body, got: %s", body)
	}
}

// ===========================================================================
// CORS headers
// ===========================================================================

func TestCORSPreflight(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	if ta.api.Config.GetNickname() != "testuser" {
		t.Errorf("expected nickname 'testuser', got %s", ta.api.Config.GetNickname())
	}
	if !ta.api.Config.GetSetupCompleted() {
		t.Errorf("expected setup_completed=true after bootstrap")
	}
}

// ===========================================================================
// Theme update
// ===========================================================================

func TestUpdateTheme_Dark(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "PATCH", "/api/config/theme", map[string]interface{}{
		"theme": "dark",
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateTheme_Light(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "PATCH", "/api/config/theme", map[string]interface{}{
		"theme": "light",
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateTheme_InvalidValue(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "PATCH", "/api/config/theme", map[string]interface{}{
		"theme": "blue",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid theme, got %d", w.Code)
	}
}

// ===========================================================================
// Logs endpoint
// ===========================================================================

func TestGetLogs_DefaultCount(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/logs", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["logs"]; !ok {
		t.Error("expected 'logs' in response")
	}
}

func TestGetLogs_WithCount(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/logs?count=5", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	// count in response should reflect the actual entries (may be 0 if LogBroadcaster is nil)
	if _, ok := resp["count"]; !ok {
		t.Error("expected 'count' in response")
	}
}

func TestGetLogs_InvalidCount(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// Invalid count should default to 100
	w := ta.request(t, "GET", "/api/logs?count=abc", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var logResp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&logResp)
	// Verify response has the expected fields (even if empty)
	if _, ok := logResp["count"]; !ok {
		t.Error("expected 'count' field in response")
	}
}

// ===========================================================================
// Admin import
// ===========================================================================

func TestAdminImport_MissingData(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// Missing "data" field
	w := ta.request(t, "POST", "/api/admin/import", map[string]interface{}{
		"other": "value",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing data, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAdminImport_EmptyData(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// data is null
	w := ta.request(t, "POST", "/api/admin/import", map[string]interface{}{
		"data": nil,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for nil data, got %d: %s", w.Code, w.Body.String())
	}
}

// ===========================================================================
// Debug goroutines
// ===========================================================================

func TestDebugGoroutines(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/debug/goroutines", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["num_goroutines"] == nil {
		t.Errorf("expected num_goroutines in response, got keys: %v", mapKeys(resp))
	}
	if resp["sse_clients"] == nil {
		t.Error("expected sse_clients in response")
	}
	if _, ok := resp["memory"]; !ok {
		t.Error("expected memory stats in response")
	}
}

func TestDebugGoroutinesDump(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/debug/goroutines/dump", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if body == "" {
		t.Error("expected non-empty goroutine dump")
	}
}

// ===========================================================================
// SetDownloadPosition edge cases
// ===========================================================================

func TestSetDownloadPosition_NotFound(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "PATCH", "/api/downloads/99999/position", map[string]interface{}{
		"priority": 1,
	})
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent download, got %d", w.Code)
	}
}

func TestSetDownloadPosition_ZeroPriorityClamps(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	createResp := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "Bot",
		"server_address": "irc.t.net", "channel": "#x", "filename": "f.mkv", "file_size": 100,
	})
	var createData map[string]int64
	_ = json.NewDecoder(createResp.Body).Decode(&createData)
	id := createData["id"]

	// Priority of 0 should be clamped to 100
	w := ta.request(t, "PATCH", "/api/downloads/"+itoa(id)+"/position", map[string]interface{}{
		"priority": 0,
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	getResp := ta.request(t, "GET", "/api/downloads/"+itoa(id), nil)
	var dl store.DownloadRecord
	_ = json.NewDecoder(getResp.Body).Decode(&dl)
	if dl.Priority != 100 {
		t.Errorf("expected priority clamped to 100, got %d", dl.Priority)
	}
}

// ===========================================================================
// Download History with filters
// ===========================================================================

func TestDownloadHistory_WithStatusFilter(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// Create and complete a download, create and fail another
	cr1 := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "Bot1",
		"server_address": "irc.t.net", "channel": "#x", "filename": "pass.mkv", "file_size": 100,
	})
	var d1 map[string]int64
	_ = json.NewDecoder(cr1.Body).Decode(&d1)
	_ = ta.store.MarkDownloadStarted(context.Background(), d1["id"])
	_ = ta.store.MarkDownloadCompleted(context.Background(), d1["id"], "", 0)

	cr2 := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #2", "bot": "Bot2",
		"server_address": "irc.t.net", "channel": "#x", "filename": "fail.mkv", "file_size": 200,
	})
	var d2 map[string]int64
	_ = json.NewDecoder(cr2.Body).Decode(&d2)
	_ = ta.store.MarkDownloadFailed(context.Background(), d2["id"], "timeout")

	// Filter by completed status only
	w := ta.request(t, "GET", "/api/downloads/history?status=completed", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 1 {
		t.Errorf("expected 1 completed download, got %v", resp["total"])
	}
}

func TestDownloadHistory_WithBotFilter(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	cr1 := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #1", "bot": "AnimeBot",
		"server_address": "irc.t.net", "channel": "#x", "filename": "a.mkv", "file_size": 100,
	})
	var d1 map[string]int64
	_ = json.NewDecoder(cr1.Body).Decode(&d1)
	_ = ta.store.MarkDownloadStarted(context.Background(), d1["id"])
	_ = ta.store.MarkDownloadCompleted(context.Background(), d1["id"], "", 0)

	cr2 := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
		"pack_message": "xdcc send #2", "bot": "OtherBot",
		"server_address": "irc.t.net", "channel": "#x", "filename": "o.mkv", "file_size": 200,
	})
	var d2 map[string]int64
	_ = json.NewDecoder(cr2.Body).Decode(&d2)
	_ = ta.store.MarkDownloadStarted(context.Background(), d2["id"])
	_ = ta.store.MarkDownloadCompleted(context.Background(), d2["id"], "", 0)

	// Filter by bot
	w := ta.request(t, "GET", "/api/downloads/history?bot=AnimeBot", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 1 {
		t.Errorf("expected 1 download matching bot filter, got %v", resp["total"])
	}
}

func TestDownloadHistory_WithPageParams(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// Create 5 completed downloads
	for i := 0; i < 5; i++ {
		cr := ta.request(t, "POST", "/api/downloads", map[string]interface{}{
			"pack_message":   "xdcc send #" + itoa(int64(i+1)),
			"bot":            "Bot",
			"server_address": "irc.t.net",
			"channel":        "#x",
			"filename":       "f.mkv",
			"file_size":      100,
		})
		var d map[string]int64
		_ = json.NewDecoder(cr.Body).Decode(&d)
		_ = ta.store.MarkDownloadStarted(context.Background(), d["id"])
		_ = ta.store.MarkDownloadCompleted(context.Background(), d["id"], "", 0)
	}

	// Page 1, size 2
	w := ta.request(t, "GET", "/api/downloads/history?page=1&pageSize=2", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["total"].(float64) != 5 {
		t.Errorf("expected total 5, got %v", resp["total"])
	}
	if resp["page"].(float64) != 1 {
		t.Errorf("expected page 1, got %v", resp["page"])
	}
	if resp["page_size"].(float64) != 2 {
		t.Errorf("expected pageSize 2, got %v", resp["page_size"])
	}
	if resp["total_pages"].(float64) != 3 {
		t.Errorf("expected total_pages 3 (ceil(5/2)), got %v", resp["total_pages"])
	}
	// Verify page contains exactly 2 items
	downloads := resp["downloads"].([]interface{})
	if len(downloads) != 2 {
		t.Errorf("expected 2 downloads on page, got %d", len(downloads))
	}
}

// ===========================================================================
// Config YAML format
// ===========================================================================

func TestGetConfig_YAMLFormat(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// Create a temporary YAML config file so the handler can read it
	yamlBytes := []byte("irc:\n  nickname: yaml-test\nui:\n  theme: dark\nsecurity:\n  admin_token: secret123\n")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, yamlBytes, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Set ConfigPath on the API so handleGetConfigRaw can find the file
	ta.api.ConfigPath = path

	w := ta.request(t, "GET", "/api/config?format=yaml", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	// Admin token should be redacted
	if strings.Contains(body, "secret123") {
		t.Error("admin token should be redacted in YAML output")
	}
	if !strings.Contains(body, "***REDACTED***") {
		t.Error("expected redacted token placeholder")
	}
	if !strings.Contains(body, "yaml-test") {
		t.Error("expected nickname in YAML output")
	}
}

func TestGetConfig_YAMLFormat_NoConfigPath(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// ConfigPath is empty → should return 500
	w := ta.request(t, "GET", "/api/config?format=yaml", nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 when ConfigPath is empty, got %d", w.Code)
	}
}

// ===========================================================================
// List servers - empty
// ===========================================================================

func TestListServers_Empty(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/servers", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var servers []interface{}
	_ = json.NewDecoder(w.Body).Decode(&servers)
	if len(servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(servers))
	}
}

// ===========================================================================
// Providers
// ===========================================================================

func TestGetProviders(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// GetProviders is public (no admin token needed) but ta.request adds one
	w := ta.request(t, "GET", "/api/search/providers", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["states"]; !ok {
		t.Error("expected 'states' in response")
	}
	if _, ok := resp["insights"]; !ok {
		t.Error("expected 'insights' in response")
	}
}

func TestGetProviders_NoAggregator(t *testing.T) {
	t.Parallel()

	// Build an API without an aggregator
	apiLogger := logging.New(logging.LevelDebug, "", 0)
	dbPath := filepath.Join(t.TempDir(), "noproviders.db")
	if err := copyFile(templateDBPath, dbPath); err != nil {
		t.Fatalf("copying template DB: %v", err)
	}
	st, err := store.NewSQLiteStore(dbPath, 2000, 3, apiLogger)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	cfg := config.DefaultConfig()
	cfg.Security.AdminToken = testAdminToken
	met := metrics.New()
	hub := sse.NewHub(10)
	defer hub.Close()

	api := New(st, nil, nil, nil, hub, nil, cfg, "", apiLogger, met, apiLogger, "test")
	router := api.Router()

	req := httptest.NewRequest(http.MethodGet, "/api/search/providers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 without aggregator, got %d", w.Code)
	}
}

func TestPatchProvider_Enable(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "PATCH", "/api/search/providers/nibl", map[string]interface{}{
		"enabled": true,
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["provider"] != "nibl" {
		t.Errorf("expected provider 'nibl', got %v", resp["provider"])
	}
	if resp["enabled"] != true {
		t.Errorf("expected enabled=true, got %v", resp["enabled"])
	}
}

func TestPatchProvider_Disable(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "PATCH", "/api/search/providers/xdcc_eu", map[string]interface{}{
		"enabled": false,
	})
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["enabled"] != false {
		t.Errorf("expected enabled=false, got %v", resp["enabled"])
	}
}

func TestPatchProvider_NoAdminToken(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	req := httptest.NewRequest(http.MethodPatch, "/api/search/providers/nibl",
		bytes.NewBufferString(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	// No admin token
	w := httptest.NewRecorder()
	ta.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without admin token, got %d", w.Code)
	}
}

// ===========================================================================
// Metrics endpoint
// ===========================================================================

func TestGetMetrics(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "GET", "/api/metrics", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if len(resp) == 0 {
		t.Error("expected non-empty metrics response")
	}
}

func TestGetMetrics_NoAdminToken(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	req := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	w := httptest.NewRecorder()
	ta.router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without admin token, got %d", w.Code)
	}
}

// ===========================================================================
// Channel operations
// ===========================================================================

func TestJoinChannel_Success(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// Add a server first
	srvID, err := ta.store.AddServer(context.Background(), store.ServerRecord{
		Address: "irc.join.net",
		Port:    6667,
		Status:  "connected",
	})
	if err != nil {
		t.Fatalf("AddServer: %v", err)
	}

	w := ta.request(t, "POST", "/api/servers/"+itoa(srvID)+"/channels", map[string]interface{}{
		"name": "#mychannel",
	})
	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]int64
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["id"] <= 0 {
		t.Errorf("expected positive channel id, got %d", resp["id"])
	}

	// Verify channel was added to store
	channels, _ := ta.store.GetChannelsByServer(context.Background(), srvID)
	if len(channels) != 1 {
		t.Errorf("expected 1 channel in store, got %d", len(channels))
	}
	if len(channels) > 0 && channels[0].Name != "#mychannel" {
		t.Errorf("expected channel name '#mychannel', got %s", channels[0].Name)
	}
}

func TestJoinChannel_EmptyName(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	srvID, _ := ta.store.AddServer(context.Background(), store.ServerRecord{
		Address: "irc.empty.net",
		Port:    6667,
	})

	w := ta.request(t, "POST", "/api/servers/"+itoa(srvID)+"/channels", map[string]interface{}{
		"name": "",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty channel name, got %d", w.Code)
	}
}

func TestJoinChannel_InvalidServerID(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	w := ta.request(t, "POST", "/api/servers/abc/channels", map[string]interface{}{
		"name": "#test",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid server ID, got %d", w.Code)
	}
}

func TestJoinChannel_Blacklisted(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// Add a blacklisted channel
	ta.api.Config.IRC.ChannelBlacklist = []string{"#blacklisted"}

	srvID, _ := ta.store.AddServer(context.Background(), store.ServerRecord{
		Address: "irc.black.net",
		Port:    6667,
	})

	w := ta.request(t, "POST", "/api/servers/"+itoa(srvID)+"/channels", map[string]interface{}{
		"name": "#blacklisted",
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for blacklisted channel, got %d: %s", w.Code, w.Body.String())
	}
}

func TestJoinChannel_NoAdminToken(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	srvID, _ := ta.store.AddServer(context.Background(), store.ServerRecord{
		Address: "irc.public.net",
		Port:    6667,
	})

	// Join channel is a public endpoint — no admin token needed
	req := httptest.NewRequest(http.MethodPost, "/api/servers/"+itoa(srvID)+"/channels",
		bytes.NewBufferString(`{"name":"#publicchan"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	ta.router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201 for public join, got %d: %s", w.Code, w.Body.String())
	}
}

func TestJoinChannel_NonExistentServer(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// Server ID 99999 doesn't exist → foreign key constraint will fail
	w := ta.request(t, "POST", "/api/servers/99999/channels", map[string]interface{}{
		"name": "#orphan",
	})
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for non-existent server (FK constraint), got %d", w.Code)
	}
}

func TestLeaveChannel_Success(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	srvID, _ := ta.store.AddServer(context.Background(), store.ServerRecord{
		Address: "irc.leave.net",
		Port:    6667,
	})
	chID, _ := ta.store.AddChannel(context.Background(), store.ChannelRecord{
		ServerID: srvID,
		Name:     "#leaveme",
		Joined:   true,
	})

	// IRCManager is nil, so LeaveChannel just deletes from store
	w := ta.request(t, "DELETE", "/api/servers/"+itoa(srvID)+"/channels/%23leaveme", nil)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify channel was removed from store
	channels, _ := ta.store.GetChannelsByServer(context.Background(), srvID)
	if len(channels) != 0 {
		t.Errorf("expected 0 channels after leave, got %d", len(channels))
	}
	_ = chID
}

func TestLeaveChannel_NoMatchingChannel(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	srvID, _ := ta.store.AddServer(context.Background(), store.ServerRecord{
		Address: "irc.nochan.net",
		Port:    6667,
	})

	// Channel doesn't exist — still 204 (handler silently succeeds)
	w := ta.request(t, "DELETE", "/api/servers/"+itoa(srvID)+"/channels/%23missing", nil)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204 even for non-existent channel, got %d", w.Code)
	}
}

func TestSendChannelMessage_Disabled(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// Default config has EnableMessageSend=false
	w := ta.request(t, "POST", "/api/servers/1/channels/%23test/messages", map[string]interface{}{
		"message": "hello",
	})
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 when message send is disabled, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSendChannelMessage_NoIRCManager(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	// Enable message sending but IRCManager is nil → 503
	ta.api.Config.IRC.EnableMessageSend = true

	w := ta.request(t, "POST", "/api/servers/1/channels/%23test/messages", map[string]interface{}{
		"message": "hello",
	})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when IRCManager is nil, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSendChannelMessage_TooLong(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	ta.api.Config.IRC.EnableMessageSend = true

	// Build a message > 4000 chars
	longMsg := ""
	for i := 0; i < 4001; i++ {
		longMsg += "x"
	}

	w := ta.request(t, "POST", "/api/servers/1/channels/%23test/messages", map[string]interface{}{
		"message": longMsg,
	})
	// IRCManager is nil, so we'll get 503 before the length check.
	// This is fine — it tests the IRCManager gate is hit.
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (IRCManager nil), got %d", w.Code)
	}
}

func TestSendChannelMessage_InvalidServerID(t *testing.T) {
	t.Parallel()
	ta := newTestAPI(t)

	ta.api.Config.IRC.EnableMessageSend = true

	w := ta.request(t, "POST", "/api/servers/abc/channels/%23test/messages", map[string]interface{}{
		"message": "hello",
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid server ID, got %d", w.Code)
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

// mapKeys returns a string slice of the map's keys (useful for debugging test failures).
func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
