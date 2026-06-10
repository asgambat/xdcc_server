package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"xdcc_server/internal/config"
	"xdcc_server/internal/logging"
	"xdcc_server/internal/metrics"
)

// ===========================================================================
// Lightweight test helpers for handler-level tests (no DB needed)
// ===========================================================================

// newHandlerTestAPI creates a minimal API for handler tests without a database.
func newHandlerTestAPI(cfg *config.Config, ircMgr IRCManager) *API {
	logger := logging.New(logging.LevelError, "", 0)
	return &API{
		IRCManager: ircMgr,
		Config:     cfg,
		Logger:     logger,
		Metrics:    metrics.New(),
		StartTime:  time.Now(),
	}
}

// sendChannelMessageRouter creates a chi mux with the send-message route.
func sendChannelMessageRouter(api *API) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/servers/{serverID}/channels/{channelName}", func(r chi.Router) {
		r.Post("/messages", api.handleSendChannelMessage)
	})
	return r
}

// ===========================================================================
// handleSendChannelMessage tests
// ===========================================================================

func TestHandleSendChannelMessage_MessagesDisabled(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		IRC: config.IRCConfig{EnableMessageSend: false},
	}
	api := newHandlerTestAPI(cfg, nil)
	r := sendChannelMessageRouter(api)

	body := `{"message":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/servers/1/channels/%23test/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if resp.Error.Code != "MESSAGES_DISABLED" {
		t.Errorf("expected MESSAGES_DISABLED, got %s", resp.Error.Code)
	}
}

func TestHandleSendChannelMessage_NilIRCManager(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		IRC: config.IRCConfig{EnableMessageSend: true},
	}
	api := newHandlerTestAPI(cfg, nil) // nil IRCManager
	r := sendChannelMessageRouter(api)

	body := `{"message":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/servers/1/channels/%23test/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
	var resp ErrorResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Error.Code != "IRC_UNAVAILABLE" {
		t.Errorf("expected IRC_UNAVAILABLE, got %s", resp.Error.Code)
	}
}

func TestHandleSendChannelMessage_EmptyMessage(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		IRC: config.IRCConfig{EnableMessageSend: true},
	}
	api := newHandlerTestAPI(cfg, &mockIRCManager{})
	r := sendChannelMessageRouter(api)

	body := `{"message":"   "}`
	req := httptest.NewRequest(http.MethodPost, "/api/servers/1/channels/%23test/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	var resp ErrorResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Error.Code != "MESSAGE_EMPTY" {
		t.Errorf("expected MESSAGE_EMPTY, got %s", resp.Error.Code)
	}
}

func TestHandleSendChannelMessage_TooLong(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		IRC: config.IRCConfig{EnableMessageSend: true},
	}
	api := newHandlerTestAPI(cfg, &mockIRCManager{})
	r := sendChannelMessageRouter(api)

	longMsg := make([]byte, 4001)
	for i := range longMsg {
		longMsg[i] = 'a'
	}
	body, _ := json.Marshal(map[string]string{"message": string(longMsg)})
	req := httptest.NewRequest(http.MethodPost, "/api/servers/1/channels/%23test/messages", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	var resp ErrorResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Error.Code != "MESSAGE_TOO_LONG" {
		t.Errorf("expected MESSAGE_TOO_LONG, got %s", resp.Error.Code)
	}
}

func TestHandleSendChannelMessage_InvalidJSON(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		IRC: config.IRCConfig{EnableMessageSend: true},
	}
	api := newHandlerTestAPI(cfg, &mockIRCManager{})
	r := sendChannelMessageRouter(api)

	req := httptest.NewRequest(http.MethodPost, "/api/servers/1/channels/%23test/messages", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestHandleSendChannelMessage_HappyPath(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		IRC: config.IRCConfig{EnableMessageSend: true},
	}
	ircMgr := &mockIRCManager{}
	api := newHandlerTestAPI(cfg, ircMgr)
	r := sendChannelMessageRouter(api)

	body := `{"message":"hello world"}`
	req := httptest.NewRequest(http.MethodPost, "/api/servers/1/channels/%23test/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["status"] != "sent" {
		t.Errorf("expected status=sent, got %s", resp["status"])
	}
	if resp["channel"] != "#test" {
		t.Errorf("expected channel=#test, got %s", resp["channel"])
	}
}

func TestHandleSendChannelMessage_Multiline(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		IRC: config.IRCConfig{EnableMessageSend: true},
	}
	api := newHandlerTestAPI(cfg, &mockIRCManager{})
	r := sendChannelMessageRouter(api)

	body := `{"message":"line1\nline2\nline3"}`
	req := httptest.NewRequest(http.MethodPost, "/api/servers/1/channels/%23test/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 for multiline, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestHandleSendChannelMessage_RateLimited(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		IRC: config.IRCConfig{
			EnableMessageSend:    true,
			MessageRateLimit:     2,
			MessageRateWindowSec: 60,
		},
	}
	ircMgr := &mockIRCManager{}
	api := newHandlerTestAPI(cfg, ircMgr)
	api.MsgRateLimiter.Store(NewRateLimiter(2, 60*time.Second))
	r := sendChannelMessageRouter(api)

	body := `{"message":"hello"}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/servers/1/channels/%23test/messages", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rr.Code)
		}
	}

	// Third request should be rate-limited.
	req := httptest.NewRequest(http.MethodPost, "/api/servers/1/channels/%23test/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", rr.Code)
	}
	var resp ErrorResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Error.Code != "RATE_LIMITED" {
		t.Errorf("expected RATE_LIMITED, got %s", resp.Error.Code)
	}
}

func TestHandleSendChannelMessage_ConcurrentWithReconfigure(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		IRC: config.IRCConfig{
			EnableMessageSend:    true,
			MessageRateLimit:     100,
			MessageRateWindowSec: 60,
		},
	}
	api := newHandlerTestAPI(cfg, &mockIRCManager{})
	api.MsgRateLimiter.Store(NewRateLimiter(100, 60*time.Second))
	r := sendChannelMessageRouter(api)

	body := `{"message":"hello"}`

	// Concurrent reconfigure while requests are in flight.
	go func() {
		time.Sleep(5 * time.Millisecond)
		if rl := api.MsgRateLimiter.Load(); rl != nil {
			rl.Reconfigure(200, 60*time.Second)
		}
	}()

	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/servers/1/channels/%23test/messages", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code == http.StatusOK {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	if successes.Load() == 0 {
		t.Error("expected at least some requests to succeed")
	}
}

// errorSendIRCManager wraps mockIRCManager but returns an error from SendChannelMessage.
type errorSendIRCManager struct{ mockIRCManager }

func (m *errorSendIRCManager) SendChannelMessage(serverID int64, channel, message string) error {
	return fmt.Errorf("not joined to channel %s", channel)
}

func TestHandleSendChannelMessage_SendError(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		IRC: config.IRCConfig{EnableMessageSend: true},
	}
	api := newHandlerTestAPI(cfg, &errorSendIRCManager{})
	r := sendChannelMessageRouter(api)

	body := `{"message":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/servers/1/channels/%23test/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp ErrorResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Error.Code != "SEND_MESSAGE_ERROR" {
		t.Errorf("expected SEND_MESSAGE_ERROR, got %s", resp.Error.Code)
	}
}

func TestExtractClientIP_TrustProxyConfigured(t *testing.T) {
	t.Parallel()
	r := &http.Request{
		RemoteAddr: "10.0.0.1:8080",
		Header:     http.Header{"X-Forwarded-For": {"203.0.113.50"}},
	}

	ip := extractClientIP(r, false)
	if ip != "10.0.0.1" {
		t.Errorf("expected 10.0.0.1, got %s", ip)
	}

	ip = extractClientIP(r, true)
	if ip != "203.0.113.50" {
		t.Errorf("expected 203.0.113.50, got %s", ip)
	}
}
