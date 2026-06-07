// Package api implements the REST API + SSE + frontend serving for the
// xdcc-server (Fase 6-8).
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lrstanley/girc"
	"xdcc_server/internal/config"
	"xdcc_server/internal/ircmanager"
	"xdcc_server/internal/logging"
	"xdcc_server/internal/metrics"
	"xdcc_server/internal/queue"
	"xdcc_server/internal/searchagg"
	"xdcc_server/internal/sse"
	"xdcc_server/internal/store"
)

// =========================================================================
// API — dependency container
// =========================================================================

// API holds all dependencies needed by the HTTP handlers.
type API struct {
	Store            *store.SQLiteStore
	IRCManager       IRCManager
	QueueManager     QueueManager
	SearchAggregator *searchagg.Aggregator
	SSEHub           *sse.Hub
	LogBroadcaster   *logging.LogBroadcaster
	Config           *config.Config
	ConfigPath       string // path to config.yaml for persisting runtime changes
	Logger           *logging.Logger
	SseDebugLogger   *logging.Logger // separate logger for SSE debug (avoids feedback loop with LogBroadcaster)
	Metrics          *metrics.Collector
	StartTime        time.Time
}

// IRCManager defines the subset of ircmanager.Manager methods used by handlers.
type IRCManager interface {
	GetServers() []store.ServerRecord
	GetClient(serverID int64) *girc.Client
	ConnectServerByID(id int64) error
	DisconnectServer(id int64) error
	JoinChannel(serverID int64, channel string) error
	LeaveChannel(serverID int64, channel string) error
	GetChannels(serverID int64) []store.ChannelRecord
	GetChannelTopic(serverID int64, channel string) (string, error)
	// Subscribe returns a channel that receives IRC state change events.
	Subscribe() chan ircmanager.Event
	// Unsubscribe removes a previously subscribed channel.
	Unsubscribe(ch chan ircmanager.Event)
}

// QueueManager defines the subset of queue.Manager methods used by handlers.
type QueueManager interface {
	Enqueue(d store.DownloadRecord) (int64, error)
	CancelDownload(id int64, reason string) error
	PauseDownload(id int64) error
	ResumeDownload(id int64) error
	RemoveDownload(id int64) error
	BulkAction(ids []int64, action string) (map[int64]string, error)
	GetActiveCount() int
	GetActiveIDs() []int64
	// Subscribe returns a channel that receives queue state change events.
	Subscribe() chan queue.Event
	// Unsubscribe removes a previously subscribed channel.
	Unsubscribe(ch chan queue.Event)
}

// New creates a new API handler container.
func New(st *store.SQLiteStore, ircMgr IRCManager, queueMgr QueueManager,
	searchAgg *searchagg.Aggregator, sseHub *sse.Hub,
	logBroadcaster *logging.LogBroadcaster,
	cfg *config.Config, configPath string, logger *logging.Logger, met *metrics.Collector,
	sseDebugLogger *logging.Logger) *API {
	return &API{
		Store:            st,
		IRCManager:       ircMgr,
		QueueManager:     queueMgr,
		SearchAggregator: searchAgg,
		SSEHub:           sseHub,
		LogBroadcaster:   logBroadcaster,
		Config:           cfg,
		ConfigPath:       configPath,
		Logger:           logger,
		SseDebugLogger:   sseDebugLogger,
		Metrics:          met,
		StartTime:        time.Now(),
	}
}

// =========================================================================
// Standard error response
// =========================================================================

// ErrorResponse is the standard JSON error response body.
type ErrorResponse struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id,omitempty"`
	} `json:"error"`
}

func newErrorResponse(code, msg, reqID string) ErrorResponse {
	var e ErrorResponse
	e.Error.Code = code
	e.Error.Message = msg
	e.Error.RequestID = reqID
	return e
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	resp := newErrorResponse(code, msg, "")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("writeError: encoding response failed (client may have disconnected): %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			log.Printf("writeJSON: encoding response failed (client may have disconnected): %v", err)
		}
	}
}

// =========================================================================
// Middleware
// =========================================================================

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher to support SSE streaming
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// MaxBodySize returns middleware that limits request body size to the given
// number of bytes. Responses with oversized bodies get a 413 Payload Too Large.
func MaxBodySize(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CORS returns middleware that sets CORS headers based on the configured
// allowlist. When origins is empty, defaults to same-origin (no wildcard).
// In dev mode, expects origins to include e.g. "http://localhost:5173".
func CORS(origins []string) func(http.Handler) http.Handler {
	allowSet := make(map[string]bool, len(origins))
	for _, o := range origins {
		allowSet[strings.TrimRight(o, "/")] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Determine the Access-Control-Allow-Origin value:
			// - If allowlist is empty, use same-origin (no header for cross-origin)
			// - If origin is in the allowlist, reflect it back
			// - Otherwise, omit the header (browser will reject)
			if len(allowSet) > 0 && origin != "" {
				if allowSet[strings.TrimRight(origin, "/")] {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
				}
			}

			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID, X-Admin-Token")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireAdminToken returns middleware that validates the X-Admin-Token header
// against the expected token using constant-time comparison.
// When expected is empty, all requests are rejected — callers must ensure
// a token is generated before using this middleware.
func RequireAdminToken(expected string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if expected == "" {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "admin token not configured")
				return
			}
			got := r.Header.Get("X-Admin-Token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid admin token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Logging returns middleware that logs each request.
// HTTP request logs are at DEBUG level since they are high-frequency.
func Logging(logger *logging.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rw, r)

			duration := time.Since(start)
			reqID := rw.Header().Get("X-Request-ID")
			if reqID == "" {
				reqID = "-"
			}

			logger.Debugf("%s %s %d %s [%s]",
				r.Method, r.URL.Path, rw.status, duration.Round(time.Millisecond), reqID)
		})
	}
}

// MetricsMiddleware returns middleware that tracks in-flight request count
// per route pattern using the metrics collector.
func MetricsMiddleware(met *metrics.Collector) func(http.Handler) http.Handler {
	if met == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			routePattern := r.URL.Path // fallback
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				if p := rctx.RoutePattern(); p != "" {
					routePattern = p
				}
			}
			key := r.Method + " " + routePattern

			met.IncEndpoint(key)
			defer met.DecEndpoint(key)

			next.ServeHTTP(w, r)
		})
	}
}

// contextKey is used for storing values in request context to avoid
// collisions with built-in string keys.
type contextKey string

const requestIDKey contextKey = "request-id"

// RequestID returns middleware that injects a unique request ID.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = fmt.Sprintf("req-%d", time.Now().UnixNano())
		}
		w.Header().Set("X-Request-ID", id)

		// Store request ID in context so handlers can access it
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// =========================================================================
// Helpers
// =========================================================================

// parseID parses an int64 from a string (URL param).
func parseID(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parseID %q: %w", s, err)
	}
	return n, nil
}

// parsePageParams extracts page and pageSize from query string.
func parsePageParams(r *http.Request) (page, pageSize int) {
	page = 1
	pageSize = 50
	if p := r.URL.Query().Get("page"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			page = n
		}
	}
	if ps := r.URL.Query().Get("pageSize"); ps != "" {
		if n, err := strconv.Atoi(ps); err == nil {
			pageSize = n
		}
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	return
}

// parseInt parses an integer from a string.
func parseInt(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parseInt %q: %w", s, err)
	}
	return n, nil
}

// parseInt64 parses an int64 from a string.
func parseInt64(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parseInt64 %q: %w", s, err)
	}
	return n, nil
}

// logAndError is a helper to log and write an error response.
func (a *API) logAndError(w http.ResponseWriter, status int, code, msg string) {
	a.Logger.Errorf("ERROR: %s: %s", code, msg)
	writeError(w, status, code, msg)
}
