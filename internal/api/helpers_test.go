package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xdcc_server/internal/logging"
)

// =========================================================================
// parseID
// =========================================================================

func TestParseID(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"42", 42, false},
		{"0", 0, false},
		{"-1", -1, false},
		{"9999999999", 9999999999, false},
		{"abc", 0, true},
		{"", 0, true},
		{"12.5", 0, true},
	}
	for _, tt := range tests {
		got, err := parseID(tt.in)
		if tt.wantErr && err == nil {
			t.Errorf("parseID(%q): expected error, got %d", tt.in, got)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("parseID(%q): unexpected error: %v", tt.in, err)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseID(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// =========================================================================
// parseInt / parseInt64
// =========================================================================

func TestParseInt(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"42", 42, false},
		{"0", 0, false},
		{"-5", -5, false},
		{"abc", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := parseInt(tt.in)
		if tt.wantErr && err == nil {
			t.Errorf("parseInt(%q): expected error", tt.in)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseInt(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseInt64(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"42", 42, false},
		{"0", 0, false},
		{"abc", 0, true},
	}
	for _, tt := range tests {
		got, err := parseInt64(tt.in)
		if tt.wantErr && err == nil {
			t.Errorf("parseInt64(%q): expected error", tt.in)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("parseInt64(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// =========================================================================
// parsePageParams
// =========================================================================

func TestParsePageParams(t *testing.T) {
	tests := []struct {
		url          string
		wantPage     int
		wantPageSize int
	}{
		{"/api/downloads", 1, 50},
		{"/api/downloads?page=3&pageSize=10", 3, 10},
		{"/api/downloads?page=0&pageSize=0", 1, 50},
		{"/api/downloads?page=999&pageSize=500", 999, 50},
		{"/api/downloads?page=abc&pageSize=xyz", 1, 50},
		{"/api/downloads?pageSize=-1", 1, 50},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.url, nil)
		page, pageSize := parsePageParams(req)
		if page != tt.wantPage {
			t.Errorf("parsePageParams(%q) page = %d, want %d", tt.url, page, tt.wantPage)
		}
		if pageSize != tt.wantPageSize {
			t.Errorf("parsePageParams(%q) pageSize = %d, want %d", tt.url, pageSize, tt.wantPageSize)
		}
	}
}

// =========================================================================
// mimeTypeByExtension
// =========================================================================

func TestMimeTypeByExtension(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".html", "text/html; charset=utf-8"},
		{".css", "text/css; charset=utf-8"},
		{".js", "application/javascript"},
		{".json", "application/json"},
		{".png", "image/png"},
		{".svg", "image/svg+xml"},
		{".ico", "image/x-icon"},
		{".woff2", "font/woff2"},
		{".webp", "image/webp"},
		{".xyz", "application/octet-stream"},
		{"", "application/octet-stream"},
	}
	for _, tt := range tests {
		got := mimeTypeByExtension(tt.ext)
		if got != tt.want {
			t.Errorf("mimeTypeByExtension(%q) = %q, want %q", tt.ext, got, tt.want)
		}
	}
}

// =========================================================================
// writeError / writeJSON
// =========================================================================

func TestWriteError(t *testing.T) {
	rr := httptest.NewRecorder()
	writeError(rr, http.StatusBadRequest, "TEST_CODE", "test message")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error.Code != "TEST_CODE" {
		t.Errorf("expected TEST_CODE, got %s", resp.Error.Code)
	}
	if resp.Error.Message != "test message" {
		t.Errorf("expected 'test message', got %q", resp.Error.Message)
	}
}

func TestWriteJSON(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusOK, map[string]string{"key": "value"})

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["key"] != "value" {
		t.Errorf("expected value, got %q", resp["key"])
	}
}

func TestWriteJSONNil(t *testing.T) {
	rr := httptest.NewRecorder()
	writeJSON(rr, http.StatusNoContent, nil)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rr.Code)
	}
}

// =========================================================================
// newErrorResponse
// =========================================================================

func TestNewErrorResponse(t *testing.T) {
	resp := newErrorResponse("ERR", "something broke", "req-123")
	if resp.Error.Code != "ERR" {
		t.Errorf("code: %s", resp.Error.Code)
	}
	if resp.Error.Message != "something broke" {
		t.Errorf("message: %s", resp.Error.Message)
	}
	if resp.Error.RequestID != "req-123" {
		t.Errorf("request_id: %s", resp.Error.RequestID)
	}
}

// =========================================================================
// responseWriter
// =========================================================================

func TestResponseWriter(t *testing.T) {
	base := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: base, status: 0}
	rw.WriteHeader(http.StatusTeapot)
	if rw.status != http.StatusTeapot {
		t.Errorf("status: %d", rw.status)
	}
	if base.Code != http.StatusTeapot {
		t.Errorf("underlying code: %d", base.Code)
	}
}

// =========================================================================
// CORS middleware
// =========================================================================

func TestCORSMiddleware_EmptyOrigins(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := CORS(nil)(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("expected no Allow-Origin with empty origins list")
	}
}

func TestCORSMiddleware_AllowedOrigin(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := CORS([]string{"http://example.com"})(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Header().Get("Access-Control-Allow-Origin") != "http://example.com" {
		t.Errorf("expected reflected origin, got %q", rr.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSMiddleware_DisallowedOrigin(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := CORS([]string{"http://trusted.com"})(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "http://evil.com")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("expected no Allow-Origin for disallowed origin")
	}
}

func TestCORSMiddleware_OptionsPreflight(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for OPTIONS preflight")
	})
	wrapped := CORS([]string{"http://example.com"})(handler)

	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "http://example.com")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", rr.Code)
	}
}

// =========================================================================
// RequireAdminToken middleware
// =========================================================================

func TestRequireAdminToken_EmptyToken(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with empty token")
	})
	wrapped := RequireAdminToken("")(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestRequireAdminToken_WrongToken(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called with wrong token")
	})
	wrapped := RequireAdminToken("secret")(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("X-Admin-Token", "wrong")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestRequireAdminToken_CorrectToken(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	wrapped := RequireAdminToken("secret")(handler)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Header.Set("X-Admin-Token", "secret")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	if !called {
		t.Error("handler should have been called")
	}
}

// =========================================================================
// RequestID middleware
// =========================================================================

func TestRequestID_GeneratesID(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := RequestID(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header")
	}
}

func TestRequestID_UsesProvidedID(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := RequestID(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("X-Request-ID", "custom-id-123")
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Header().Get("X-Request-ID") != "custom-id-123" {
		t.Errorf("expected custom-id-123, got %q", rr.Header().Get("X-Request-ID"))
	}
}

func TestRequestID_ContextValue(t *testing.T) {
	var ctxID string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.Context().Value(requestIDKey); id != nil {
			ctxID = id.(string)
		}
		w.WriteHeader(http.StatusOK)
	})
	wrapped := RequestID(handler)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if ctxID == "" {
		t.Error("expected request ID in context")
	}
}

// =========================================================================
// MaxBodySize middleware
// =========================================================================

func TestMaxBodySize_UnderLimit(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := MaxBodySize(100)(handler)

	body := bytes.NewBufferString(`{"key":"value"}`)
	req := httptest.NewRequest(http.MethodPost, "/test", body)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

// =========================================================================
// Logging middleware
// =========================================================================

func TestLoggingMiddleware_Smoke(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	logger := logging.New(logging.LevelDebug, "", 0)
	wrapped := Logging(logger)(handler)

	req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", rr.Code)
	}
}

// =========================================================================
// replaceYAMLValue / findKeyInYAML
// =========================================================================

func TestReplaceYAMLValue(t *testing.T) {
	input := "token: secret123\nother: value\n"
	result := replaceYAMLValue(input, "token", "***")
	want := "token: ***\nother: value\n"
	if result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}

func TestFindKeyInYAML(t *testing.T) {
	s := "irc:\n  nickname: bot\nadmin_token: secret\n"

	pos := findKeyInYAML(s, "admin_token", 0)
	if pos < 0 {
		t.Error("expected to find admin_token")
	}
	if s[pos:pos+11] != "admin_token" {
		t.Errorf("found wrong position: %q", s[pos:pos+11])
	}

	pos = findKeyInYAML(s, "nonexistent", 0)
	if pos >= 0 {
		t.Error("expected not to find nonexistent key")
	}
}

func TestReplaceYAMLValue_AdminTokenRedaction(t *testing.T) {
	input := "admin_token: supersecret\nother: value\n"
	result := replaceYAMLValue(input, "admin_token", "***REDACTED***")
	expected := "admin_token: ***REDACTED***\nother: value\n"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
}

// =========================================================================
// extractClientIP (already tested in extractip_test.go — smoke test)
// =========================================================================

func TestExtractClientIP_NoPort(t *testing.T) {
	r := &http.Request{RemoteAddr: "1.2.3.4"}
	got := extractClientIP(r, false)
	if got != "1.2.3.4" {
		t.Errorf("expected 1.2.3.4, got %q", got)
	}
}

// =========================================================================
// handleHealthz smoke test (standalone handler, no DB)
// =========================================================================

func TestHandleHealthz(t *testing.T) {
	api := &API{StartTime: time.Now()}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	api.handleHealthz(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["status"] != "ok" {
		t.Errorf("expected ok, got %s", resp["status"])
	}
}

// =========================================================================
// handleVersion (standalone, no DB)
// =========================================================================

func TestHandleVersion(t *testing.T) {
	api := &API{Version: "1.2.3", StartTime: time.Now()}
	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	rr := httptest.NewRecorder()
	api.handleVersion(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["version"] != "1.2.3" {
		t.Errorf("expected 1.2.3, got %s", resp["version"])
	}
}

func TestHandleVersion_Dev(t *testing.T) {
	api := &API{Version: "", StartTime: time.Now()}
	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	rr := httptest.NewRecorder()
	api.handleVersion(rr, req)

	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["version"] != "dev" {
		t.Errorf("expected 'dev' for empty version, got %s", resp["version"])
	}
}
