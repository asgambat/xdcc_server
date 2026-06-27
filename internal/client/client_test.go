package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xdcc_server/internal/store"
)

// ---------------------------------------------------------------------------
// New / BaseURL
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	c := New("http://localhost:8080")
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.baseURL != "http://localhost:8080" {
		t.Errorf("expected baseURL, got %q", c.baseURL)
	}
}

func TestNewStripsTrailingSlash(t *testing.T) {
	c := New("http://localhost:8080/")
	if c.baseURL != "http://localhost:8080" {
		t.Errorf("expected trailing slash stripped, got %q", c.baseURL)
	}
}

func TestBaseURL(t *testing.T) {
	c := New("http://example.com:9090")
	if c.BaseURL() != "http://example.com:9090" {
		t.Errorf("BaseURL wrong: %q", c.BaseURL())
	}
}

// ---------------------------------------------------------------------------
// ServerError
// ---------------------------------------------------------------------------

func TestServerErrorFormatting(t *testing.T) {
	tests := []struct {
		err  ServerError
		want string
	}{
		{ServerError{StatusCode: 500, Code: "INTERNAL_ERROR", Message: "something broke"},
			"server error 500: INTERNAL_ERROR — something broke"},
		{ServerError{StatusCode: 404},
			"server error: HTTP 404"},
		{ServerError{StatusCode: 400, Message: "bad"},
			"server error: HTTP 400"},
	}
	for _, tt := range tests {
		got := tt.err.Error()
		if got != tt.want {
			t.Errorf("ServerError.Error() = %q, want %q", got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// VersionMismatchError
// ---------------------------------------------------------------------------

func TestVersionMismatchError(t *testing.T) {
	err := &VersionMismatchError{ServerVersion: "2.0", ClientVersion: "1.5"}
	want := `server version "2.0" is incompatible with client "1.5"`
	if err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// Integration tests with httptest
// ---------------------------------------------------------------------------

func TestCheckVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ServerVersion{
			Version:                    "1.0.0",
			MinCompatibleClientVersion: "1.0.0",
		})
	}))
	defer srv.Close()

	c := New(srv.URL)
	v, err := c.CheckVersion()
	if err != nil {
		t.Fatalf("CheckVersion error: %v", err)
	}
	if v.Version != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %q", v.Version)
	}
}

func TestCheckVersionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.CheckVersion()
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestEnqueueDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/downloads" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(EnqueueResponse{ID: 42})
	}))
	defer srv.Close()

	c := New(srv.URL)
	id, err := c.EnqueueDownload(store.DownloadRecord{
		PackMessage:   "xdcc send #1",
		Bot:           "TestBot",
		ServerAddress: "irc.test.com",
		Channel:       "#xdcc",
		Filename:      "test.mkv",
		FileSize:      12345,
	})
	if err != nil {
		t.Fatalf("EnqueueDownload error: %v", err)
	}
	if id != 42 {
		t.Errorf("expected ID 42, got %d", id)
	}
}

func TestEnqueueDownloadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ServerError{Code: "INVALID", Message: "bad pack"})
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.EnqueueDownload(store.DownloadRecord{
		PackMessage: "bad",
		Bot:         "TestBot",
	})
	if err == nil {
		t.Error("expected error for 400 response")
	}
}

func TestGetDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/downloads/42" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(store.DownloadRecord{
			ID:       42,
			Filename: "test.mkv",
			Status:   "completed",
		})
	}))
	defer srv.Close()

	c := New(srv.URL)
	rec, err := c.GetDownload(42)
	if err != nil {
		t.Fatalf("GetDownload error: %v", err)
	}
	if rec.ID != 42 {
		t.Errorf("expected ID 42, got %d", rec.ID)
	}
	if rec.Filename != "test.mkv" {
		t.Errorf("expected filename test.mkv, got %q", rec.Filename)
	}
}

func TestSearchURLBuilding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("q") != "test query" {
			t.Errorf("expected q='test query', got %q", query.Get("q"))
		}
		if query.Get("compact") != "true" {
			t.Error("expected compact=true")
		}
		if query.Get("page") != "2" {
			t.Errorf("expected page=2, got %q", query.Get("page"))
		}
		if query.Get("pageSize") != "25" {
			t.Errorf("expected pageSize=25, got %q", query.Get("pageSize"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SearchResult{Total: 0})
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.Search(&SearchOptions{
		Query:    "test query",
		Compact:  true,
		Page:     2,
		PageSize: 25,
	})
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
}

func TestSearchServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New(srv.URL)
	_, err := c.Search(&SearchOptions{Query: "test"})
	if err == nil {
		t.Error("expected error for 503 response")
	}
}

func TestPollDownloadCompleted(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		status := "downloading"
		if callCount >= 3 {
			status = "completed"
		}
		json.NewEncoder(w).Encode(store.DownloadRecord{
			ID:     1,
			Status: status,
		})
	}))
	defer srv.Close()

	c := New(srv.URL)
	var progressCalls int
	rec, err := c.PollDownload(1, 10*time.Millisecond, 5*time.Second, func(r *store.DownloadRecord) {
		progressCalls++
	})
	if err != nil {
		t.Fatalf("PollDownload error: %v", err)
	}
	if rec.Status != "completed" {
		t.Errorf("expected completed, got %s", rec.Status)
	}
	// At least 2 callbacks: one with "downloading", one with "completed"
	if progressCalls < 2 {
		t.Errorf("expected at least 2 progress callbacks, got %d", progressCalls)
	}
}

func TestServerUnreachable(t *testing.T) {
	c := New("http://127.0.0.1:19999") // non-existent server
	_, err := c.CheckVersion()
	if err == nil {
		t.Error("expected error for unreachable server")
	}
}
