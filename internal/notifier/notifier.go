// Package notifier provides external notification support for download and
// watchlist events. It supports three notification providers:
//   - webhook: generic HTTP POST with JSON payload
//   - ntfy: push notifications via ntfy.sh (or self-hosted)
//   - pushover: push notifications via Pushover.net
//
// Configuration is loaded from config.yaml:
//
//	notifications:
//	  - type: webhook
//	    webhook_url: "https://example.com/webhook"
//	    events: [download_completed, download_failed]
//
//	  - type: ntfy
//	    ntfy_token: "tk_abc123"
//	    ntfy_endpoint: "https://ntfy.sh/mytopic"
//	    events: [download_completed, download_failed, watchlist_new_results]
//
//	  - type: pushover
//	    pushover_token: "abc123"
//	    pushover_user: "userkey"
//	    events: [download_completed, download_failed, watchlist_new_results]
package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"xdcc-go/internal/config"
	"xdcc-go/internal/logging"
	"xdcc-go/internal/queue"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// EventType represents a type of event that can trigger a notification.
type EventType string

const (
	EventDownloadCompleted   EventType = "download_completed"
	EventDownloadFailed      EventType = "download_failed"
	EventWatchlistNewResults EventType = "watchlist_new_results"
)

// ---------------------------------------------------------------------------
// Notifier interface
// ---------------------------------------------------------------------------

// Notifier sends notifications for download and watchlist events.
// Implementations must be safe for concurrent calls.
type Notifier interface {
	Notify(evt queue.Event) error
	NotifyWatchlistResults(event WatchlistEvent) error
}

// ---------------------------------------------------------------------------
// Watchlist event
// ---------------------------------------------------------------------------

// WatchlistEvent holds details about new packs found by a watchlist.
type WatchlistEvent struct {
	WatchlistName string `json:"watchlist_name"`
	NewPacksCount int    `json:"new_packs_count"`
	EnqueuedCount int    `json:"enqueued_count"`
	Timestamp     string `json:"timestamp"`
}

// ---------------------------------------------------------------------------
// Event filter helper
// ---------------------------------------------------------------------------

// eventsFilter builds a map of allowed EventTypes from a config events list.
// An empty list means all events are allowed.
func eventsFilter(events []string) map[EventType]bool {
	m := make(map[EventType]bool, len(events))
	for _, e := range events {
		m[EventType(e)] = true
	}
	return m
}

// interested checks whether the filter allows the given event type.
func interested(filter map[EventType]bool, evtType EventType) bool {
	if len(filter) == 0 {
		return true // no filter = notify on all events
	}
	return filter[evtType]
}

// ---------------------------------------------------------------------------
// Message formatting
// ---------------------------------------------------------------------------

// notifyMessage holds a human-readable title and body for push notifications.
type notifyMessage struct {
	Title   string
	Message string
}

// formatDownloadMessage generates a human-readable notification message
// from a queue event.
func formatDownloadMessage(evt queue.Event) notifyMessage {
	switch evt.Type {
	case queue.EventDownloadCompleted:
		return notifyMessage{
			Title:   "Download completato",
			Message: fmt.Sprintf("✓ %s completato", evt.Filename),
		}
	case queue.EventDownloadFailed:
		msg := fmt.Sprintf("✗ %s fallito", evt.Filename)
		if evt.ErrorMessage != "" {
			msg += ": " + evt.ErrorMessage
		}
		return notifyMessage{
			Title:   "Download fallito",
			Message: msg,
		}
	default:
		return notifyMessage{
			Title:   string(evt.Type),
			Message: fmt.Sprintf("Download %s: %s", evt.Type, evt.Filename),
		}
	}
}

// formatWatchlistMessage generates a human-readable notification message
// from a watchlist event.
func formatWatchlistMessage(event WatchlistEvent) notifyMessage {
	msg := fmt.Sprintf("Watchlist %q: %d nuovi pacchetti", event.WatchlistName, event.NewPacksCount)
	if event.EnqueuedCount > 0 {
		msg += fmt.Sprintf(" (%d in coda)", event.EnqueuedCount)
	}
	return notifyMessage{
		Title:   "Watchlist aggiornata",
		Message: msg,
	}
}

// ---------------------------------------------------------------------------
// Shared HTTP helpers
// ---------------------------------------------------------------------------

var defaultHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
}

// sendFormPOST sends an HTTP POST with form-urlencoded data.
func sendFormPOST(ctx context.Context, urlStr string, data map[string]string) error {
	form := url.Values{}
	for k, v := range data {
		form.Set(k, v)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader([]byte(form.Encode())))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "xdcc-server/1.0")

	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body for error details
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ---------------------------------------------------------------------------
// WebhookNotifier
// ---------------------------------------------------------------------------

// WebhookNotifier sends notifications via HTTP POST to a configurable endpoint.
// The POST body is {"data": "<formatted message>"} with an optional Bearer token.
type WebhookNotifier struct {
	endpoint string
	token    string
	events   map[EventType]bool
}

// NewWebhookNotifier creates a WebhookNotifier from a notification config entry.
// Returns nil if the config type is not "webhook" or the endpoint is empty.
func NewWebhookNotifier(cfg config.NotificationConfig) *WebhookNotifier {
	if cfg.Type != "webhook" || cfg.WebhookEndpoint == "" {
		return nil
	}
	return &WebhookNotifier{
		endpoint: cfg.WebhookEndpoint,
		token:    cfg.WebhookToken,
		events:   eventsFilter(cfg.Events),
	}
}

// webhookData is the JSON body sent to the webhook endpoint.
type webhookData struct {
	Data string `json:"data"`
}

// send delivers a notification to the webhook endpoint.
func (w *WebhookNotifier) send(ctx context.Context, message string) error {
	body, err := json.Marshal(webhookData{Data: message})
	if err != nil {
		return fmt.Errorf("webhook: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "xdcc-server/1.0")
	if w.token != "" {
		req.Header.Set("Authorization", "Bearer "+w.token)
	}

	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// Notify sends a webhook notification for a download event.
func (w *WebhookNotifier) Notify(evt queue.Event) error {
	notifType := mapQueueEvent(evt.Type)
	if notifType == "" || !interested(w.events, notifType) {
		return nil
	}

	nm := formatDownloadMessage(evt)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return w.send(ctx, nm.Message)
}

// NotifyWatchlistResults sends a webhook notification for a watchlist event.
func (w *WebhookNotifier) NotifyWatchlistResults(event WatchlistEvent) error {
	if !interested(w.events, EventWatchlistNewResults) {
		return nil
	}

	nm := formatWatchlistMessage(event)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return w.send(ctx, nm.Message)
}

// ---------------------------------------------------------------------------
// NtfyNotifier
// ---------------------------------------------------------------------------

// NtfyNotifier sends push notifications via ntfy.sh (or a self-hosted instance).
type NtfyNotifier struct {
	token    string
	endpoint string
	events   map[EventType]bool
}

// NewNtfyNotifier creates an NtfyNotifier from a notification config entry.
// Returns nil if the config type is not "ntfy" or the endpoint is empty.
func NewNtfyNotifier(cfg config.NotificationConfig) *NtfyNotifier {
	if cfg.Type != "ntfy" || cfg.NtfyEndpoint == "" {
		return nil
	}
	return &NtfyNotifier{
		token:    cfg.NtfyToken,
		endpoint: cfg.NtfyEndpoint,
		events:   eventsFilter(cfg.Events),
	}
}

// publish sends a message to the ntfy endpoint with Bearer auth and priority header.
func (n *NtfyNotifier) publish(ctx context.Context, message string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.endpoint, bytes.NewReader([]byte(message)))
	if err != nil {
		return fmt.Errorf("ntfy: create request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("User-Agent", "xdcc-server/1.0")
	req.Header.Set("X-Title", "XDCC-go")
	req.Header.Set("Priority", "4")
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}

	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// Notify sends a ntfy notification for a download event.
func (n *NtfyNotifier) Notify(evt queue.Event) error {
	notifType := mapQueueEvent(evt.Type)
	if notifType == "" || !interested(n.events, notifType) {
		return nil
	}

	nm := formatDownloadMessage(evt)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return n.publish(ctx, nm.Message)
}

// NotifyWatchlistResults sends a ntfy notification for a watchlist event.
func (n *NtfyNotifier) NotifyWatchlistResults(event WatchlistEvent) error {
	if !interested(n.events, EventWatchlistNewResults) {
		return nil
	}

	nm := formatWatchlistMessage(event)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return n.publish(ctx, nm.Message)
}

// ---------------------------------------------------------------------------
// PushoverNotifier
// ---------------------------------------------------------------------------

// PushoverNotifier sends push notifications via the Pushover API.
type PushoverNotifier struct {
	token    string
	user     string
	endpoint string
	events   map[EventType]bool
}

// NewPushoverNotifier creates a PushoverNotifier from a notification config entry.
// Returns nil if the config type is not "pushover" or token/user is empty.
func NewPushoverNotifier(cfg config.NotificationConfig) *PushoverNotifier {
	if cfg.Type != "pushover" || cfg.PushoverToken == "" || cfg.PushoverUser == "" {
		return nil
	}
	endpoint := cfg.PushoverEndpoint
	if endpoint == "" {
		endpoint = "https://api.pushover.net/1/messages.json"
	}
	return &PushoverNotifier{
		token:    cfg.PushoverToken,
		user:     cfg.PushoverUser,
		endpoint: endpoint,
		events:   eventsFilter(cfg.Events),
	}
}

// send delivers a notification to the Pushover API.
func (p *PushoverNotifier) send(ctx context.Context, nm notifyMessage) error {
	data := map[string]string{
		"token":   p.token,
		"user":    p.user,
		"message": nm.Message,
	}
	if nm.Title != "" {
		data["title"] = nm.Title
	}
	return sendFormPOST(ctx, p.endpoint, data)
}

// Notify sends a Pushover notification for a download event.
func (p *PushoverNotifier) Notify(evt queue.Event) error {
	notifType := mapQueueEvent(evt.Type)
	if notifType == "" || !interested(p.events, notifType) {
		return nil
	}

	nm := formatDownloadMessage(evt)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.send(ctx, nm)
}

// NotifyWatchlistResults sends a Pushover notification for a watchlist event.
func (p *PushoverNotifier) NotifyWatchlistResults(event WatchlistEvent) error {
	if !interested(p.events, EventWatchlistNewResults) {
		return nil
	}

	nm := formatWatchlistMessage(event)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.send(ctx, nm)
}

// ---------------------------------------------------------------------------
// Queue event mapping
// ---------------------------------------------------------------------------

// mapQueueEvent converts a queue.EventType to a notifier.EventType.
// Returns "" for unsupported event types.
func mapQueueEvent(evtType queue.EventType) EventType {
	switch evtType {
	case queue.EventDownloadCompleted:
		return EventDownloadCompleted
	case queue.EventDownloadFailed:
		return EventDownloadFailed
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// Manager subscribes to queue events and dispatches them to configured notifiers.
// It also supports direct watchlist-result notifications.
type Manager struct {
	notifiers []Notifier
	logger    *logging.Logger
}

// NewManager creates a Manager from notification configs.
// Config entries with unsupported types are silently skipped.
func NewManager(cfgs []config.NotificationConfig, logger *logging.Logger) *Manager {
	var notifiers []Notifier

	for _, cfg := range cfgs {
		switch cfg.Type {
		case "webhook":
			n := NewWebhookNotifier(cfg)
			if n != nil {
				notifiers = append(notifiers, n)
				logger.Infof("notifier: added webhook (%d events) → %s", len(cfg.Events), cfg.WebhookEndpoint)
			}
		case "ntfy":
			n := NewNtfyNotifier(cfg)
			if n != nil {
				notifiers = append(notifiers, n)
				logger.Infof("notifier: added ntfy (%d events) → %s", len(cfg.Events), cfg.NtfyEndpoint)
			}
		case "pushover":
			n := NewPushoverNotifier(cfg)
			if n != nil {
				notifiers = append(notifiers, n)
				logger.Infof("notifier: added pushover (%d events) → user=%s", len(cfg.Events), cfg.PushoverUser)
			}
		default:
			logger.Warnf("notifier: unknown type %q, skipping", cfg.Type)
		}
	}

	return &Manager{
		notifiers: notifiers,
		logger:    logger,
	}
}

// Run subscribes to queue events and dispatches them to all notifiers.
// It blocks until the context is cancelled. The caller should call
// wg.Add(1) before calling Run. On shutdown it drains remaining events.
func (m *Manager) Run(ctx context.Context, ch <-chan queue.Event, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			m.drainEvents(ch)
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			m.dispatch(evt)
		}
	}
}

// dispatch sends an event to all notifiers concurrently.
func (m *Manager) dispatch(evt queue.Event) {
	for _, n := range m.notifiers {
		n := n
		go func() {
			if err := n.Notify(evt); err != nil {
				m.logger.Errorf("notifier: notification failed for download %d: %v", evt.DownloadID, err)
			} else {
				m.logger.Debugf("notifier: notification sent for download %d (event=%s)", evt.DownloadID, evt.Type)
			}
		}()
	}
}

// drainEvents drains remaining events during shutdown.
func (m *Manager) drainEvents(ch <-chan queue.Event) {
	timeout := time.After(100 * time.Millisecond)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			m.dispatch(evt)
		case <-timeout:
			return
		}
	}
}

// NotifyWatchlistResults sends a watchlist notification to all notifiers.
func (m *Manager) NotifyWatchlistResults(name string, newPacks, enqueued int) {
	if len(m.notifiers) == 0 {
		return
	}

	event := WatchlistEvent{
		WatchlistName: name,
		NewPacksCount: newPacks,
		EnqueuedCount: enqueued,
		Timestamp:     time.Now().Format(time.RFC3339),
	}

	for _, n := range m.notifiers {
		n := n
		go func() {
			if err := n.NotifyWatchlistResults(event); err != nil {
				m.logger.Errorf("notifier: watchlist notification failed for %q: %v", name, err)
			} else {
				m.logger.Debugf("notifier: watchlist notification sent for %q (%d new packs)", name, newPacks)
			}
		}()
	}
}

// Notifiers returns the list of configured notifiers (for introspection).
func (m *Manager) Notifiers() []Notifier {
	return m.notifiers
}
