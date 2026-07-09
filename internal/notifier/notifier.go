// Package notifier provides external notification support for download and
// watchlist events. It supports four notification providers:
//   - webhook: generic HTTP POST with JSON payload
//   - ntfy: push notifications via ntfy.sh (or self-hosted)
//   - pushover: push notifications via Pushover.net
//   - email: SMTP email notifications
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
//
//	  - type: email
//	    smtp_host: "smtp.example.com"
//	    smtp_port: 587
//	    smtp_username: "user"
//	    smtp_password: "pass"
//	    smtp_from: "sender@example.com"
//	    smtp_to: "recipient@example.com"
//	    events: [download_completed, download_failed, watchlist_new_results]
package notifier

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"sync"
	"time"

	"xdcc_server/internal/config"
	"xdcc_server/internal/logging"
	"xdcc_server/internal/queue"
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
	Query         string `json:"query"`
	SearchURL     string `json:"search_url"`
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
	if event.SearchURL != "" {
		msg += fmt.Sprintf("\nCerca: %s", event.SearchURL)
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
	Data      string `json:"data"`
	SearchURL string `json:"search_url,omitempty"`
}

// send delivers a notification to the webhook endpoint.
func (w *WebhookNotifier) send(ctx context.Context, message, searchURL string) error {
	body, err := json.Marshal(webhookData{Data: message, SearchURL: searchURL})
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
	return w.send(ctx, nm.Message, "")
}

// NotifyWatchlistResults sends a webhook notification for a watchlist event.
func (w *WebhookNotifier) NotifyWatchlistResults(event WatchlistEvent) error {
	if !interested(w.events, EventWatchlistNewResults) {
		return nil
	}

	nm := formatWatchlistMessage(event)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return w.send(ctx, nm.Message, event.SearchURL)
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
func (n *NtfyNotifier) publish(ctx context.Context, message, clickURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.endpoint, bytes.NewReader([]byte(message)))
	if err != nil {
		return fmt.Errorf("ntfy: create request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("User-Agent", "xdcc-server/1.0")
	req.Header.Set("X-Title", "XDCC_server")
	req.Header.Set("Priority", "4")
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	}
	if clickURL != "" {
		req.Header.Set("Click", clickURL)
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
	return n.publish(ctx, nm.Message, "")
}

// NotifyWatchlistResults sends a ntfy notification for a watchlist event.
func (n *NtfyNotifier) NotifyWatchlistResults(event WatchlistEvent) error {
	if !interested(n.events, EventWatchlistNewResults) {
		return nil
	}

	nm := formatWatchlistMessage(event)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return n.publish(ctx, nm.Message, event.SearchURL)
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
func (p *PushoverNotifier) send(ctx context.Context, nm notifyMessage, urlStr, urlTitle string) error {
	data := map[string]string{
		"token":   p.token,
		"user":    p.user,
		"message": nm.Message,
	}
	if nm.Title != "" {
		data["title"] = nm.Title
	}
	if urlStr != "" {
		data["url"] = urlStr
		if urlTitle != "" {
			data["url_title"] = urlTitle
		}
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
	return p.send(ctx, nm, "", "")
}

// NotifyWatchlistResults sends a Pushover notification for a watchlist event.
func (p *PushoverNotifier) NotifyWatchlistResults(event WatchlistEvent) error {
	if !interested(p.events, EventWatchlistNewResults) {
		return nil
	}

	nm := formatWatchlistMessage(event)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.send(ctx, nm, event.SearchURL, "Apri ricerca")
}

// ---------------------------------------------------------------------------
// EmailNotifier (SMTP)
// ---------------------------------------------------------------------------

// EmailNotifier sends notifications via SMTP email.
type EmailNotifier struct {
	host       string
	port       int
	username   string
	password   string
	from       string
	to         []string // parsed recipients
	tlsMode    string   // "starttls", "ssl", "none"
	skipVerify bool
	events     map[EventType]bool
}

// NewEmailNotifier creates an EmailNotifier from a notification config entry.
// Returns nil if the config type is not "email" or required fields are missing.
func NewEmailNotifier(cfg config.NotificationConfig) *EmailNotifier {
	if cfg.Type != "email" || cfg.SMTPHost == "" || cfg.SMTPFrom == "" || cfg.SMTPTo == "" {
		return nil
	}

	port := cfg.SMTPPort
	if port == 0 {
		port = 587 // default STARTTLS
	}

	tlsMode := cfg.SMTPTLS
	if tlsMode == "" {
		tlsMode = "starttls"
	}

	// Parse and normalize recipients
	recipients := strings.Split(cfg.SMTPTo, ",")
	parsed := make([]string, 0, len(recipients))
	for _, r := range recipients {
		r = strings.TrimSpace(r)
		if r != "" {
			parsed = append(parsed, r)
		}
	}

	return &EmailNotifier{
		host:       cfg.SMTPHost,
		port:       port,
		username:   cfg.SMTPUsername,
		password:   cfg.SMTPPassword,
		from:       cfg.SMTPFrom,
		to:         parsed,
		tlsMode:    tlsMode,
		skipVerify: cfg.SMTPSkipVerify,
		events:     eventsFilter(cfg.Events),
	}
}

// send delivers an email notification.
func (e *EmailNotifier) send(ctx context.Context, nm notifyMessage) error {
	if len(e.to) == 0 {
		return fmt.Errorf("email: no recipients configured")
	}

	// Build the MIME email
	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("From: %s\r\n", e.from))
	buf.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(e.to, ", ")))
	buf.WriteString(fmt.Sprintf("Subject: %s\r\n", nm.Title))
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	buf.WriteString("\r\n")
	buf.WriteString(nm.Message)

	addr := fmt.Sprintf("%s:%d", e.host, e.port)

	done := make(chan error, 1)

	go func() {
		switch e.tlsMode {
		case "ssl":
			done <- e.sendSSL(addr, buf.Bytes())
		case "none":
			done <- e.sendNoTLS(addr, buf.Bytes())
		default: // "starttls"
			done <- e.sendSTARTTLS(addr, buf.Bytes())
		}
	}()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("email: %w", err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sendSTARTTLS uses net/smtp.SendMail which automatically negotiates STARTTLS.
func (e *EmailNotifier) sendSTARTTLS(addr string, msg []byte) error {
	var auth smtp.Auth
	if e.username != "" {
		auth = smtp.PlainAuth("", e.username, e.password, e.host)
	}
	return smtp.SendMail(addr, auth, e.from, e.to, msg)
}

// sendSSL connects over an explicit TLS tunnel (SMTPS, typically port 465).
func (e *EmailNotifier) sendSSL(addr string, msg []byte) error {
	tlsCfg := &tls.Config{
		ServerName:         e.host,
		InsecureSkipVerify: e.skipVerify,
	}

	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, e.host)
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	defer client.Close()

	return e.sendViaClient(client, msg)
}

// sendNoTLS connects over a plain TCP connection without TLS.
func (e *EmailNotifier) sendNoTLS(addr string, msg []byte) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, e.host)
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	defer client.Close()

	return e.sendViaClient(client, msg)
}

// allowAnyTLS implements smtp.Auth, wrapping PlainAuth but skipping the TLS
// check. This is needed because smtp.PlainAuth refuses to authenticate on
// connections that did NOT negotiate STARTTLS — but our sendSSL path
// connects via tls.Dial before smtp.NewClient, so ServerInfo.TLS is false
// even though the connection is encrypted.
type allowAnyTLS struct {
	user, pass, host string
}

func (a *allowAnyTLS) Start(server *smtp.ServerInfo) (mech string, resp []byte, err error) {
	resp = []byte(a.user + "\x00" + a.user + "\x00" + a.pass)
	return "PLAIN", resp, nil
}

func (a *allowAnyTLS) Next(fromServer []byte, more bool) ([]byte, error) {
	return nil, nil
}

// smtpAuth returns the appropriate smtp.Auth for the current TLS mode.
func (e *EmailNotifier) smtpAuth() smtp.Auth {
	if e.username == "" {
		return nil
	}
	if e.tlsMode == "starttls" {
		return smtp.PlainAuth("", e.username, e.password, e.host)
	}
	return &allowAnyTLS{user: e.username, pass: e.password, host: e.host}
}

// sendViaClient performs SMTP MAIL, RCPT, DATA, and QUIT on an established client.
func (e *EmailNotifier) sendViaClient(client *smtp.Client, msg []byte) error {
	// Authenticate if credentials are provided
	if auth := e.smtpAuth(); auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}

	// MAIL FROM
	if err := client.Mail(e.from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}

	// RCPT TO for each recipient
	for _, r := range e.to {
		if err := client.Rcpt(r); err != nil {
			return fmt.Errorf("rcpt %s: %w", r, err)
		}
	}

	// DATA (message body)
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		w.Close()
		return fmt.Errorf("write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}

	return client.Quit()
}

// Notify sends an email notification for a download event.
func (e *EmailNotifier) Notify(evt queue.Event) error {
	notifType := mapQueueEvent(evt.Type)
	if notifType == "" || !interested(e.events, notifType) {
		return nil
	}

	nm := formatDownloadMessage(evt)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return e.send(ctx, nm)
}

// NotifyWatchlistResults sends an email notification for a watchlist event.
func (e *EmailNotifier) NotifyWatchlistResults(event WatchlistEvent) error {
	if !interested(e.events, EventWatchlistNewResults) {
		return nil
	}

	nm := formatWatchlistMessage(event)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return e.send(ctx, nm)
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
	baseURL   string
}

// isEnabled returns true if the notification config is enabled.
// nil (unset) defaults to enabled = true.
func isEnabled(cfg *config.NotificationConfig) bool {
	return cfg.Enabled == nil || *cfg.Enabled
}

// validEvents lists the known event types for validation.
var validEvents = map[string]bool{
	string(EventDownloadCompleted):   true,
	string(EventDownloadFailed):      true,
	string(EventWatchlistNewResults): true,
}

// NewManager creates a Manager from notification configs.
// Config entries with unsupported types are silently skipped.
// Config entries with enabled=false are skipped.
// baseURL is the public-facing URL used to construct search links in notifications.
func NewManager(cfgs []config.NotificationConfig, baseURL string, logger *logging.Logger) *Manager {
	var notifiers []Notifier
	// Track whether any provider covers watchlist_new_results so we can warn
	// at startup if base_url is empty (links would not be sent).
	// Empty cfg.Events means "all events" — mirrors eventsFilter/interested semantics.
	var watchlistProviderEnabled bool

	for _, cfg := range cfgs {
		// Skip if explicitly disabled
		if !isEnabled(&cfg) {
			logger.Warnf("notifier: provider %q is disabled via enabled: false, skipping", cfg.Type)
			continue
		}

		// Warn about unknown events in the config
		for _, e := range cfg.Events {
			if !validEvents[e] {
				logger.Warnf("notifier: %q provider has unknown event %q, will be ignored", cfg.Type, e)
			}
		}

		coversWatchlist := len(cfg.Events) == 0
		if !coversWatchlist {
			for _, e := range cfg.Events {
				if e == string(EventWatchlistNewResults) {
					coversWatchlist = true
					break
				}
			}
		}
		if coversWatchlist {
			watchlistProviderEnabled = true
		}

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
		case "email":
			n := NewEmailNotifier(cfg)
			if n != nil {
				notifiers = append(notifiers, n)
				logger.Infof("notifier: added email (%d events) → %s", len(cfg.Events), cfg.SMTPFrom)
			}
		default:
			logger.Warnf("notifier: unknown type %q, skipping", cfg.Type)
		}
	}

	if baseURL != "" {
		logger.Infof("notifier: base URL for notification links: %s", baseURL)
	} else if watchlistProviderEnabled {
		logger.Warnf("notifier: http.base_url is empty; watchlist notifications will fire but will NOT include direct search links (the \"Cerca: <url>\" line). Set http.base_url in config.yaml or XDCC_HTTP_BASE_URL env var to enable them.")
	}

	return &Manager{
		notifiers: notifiers,
		logger:    logger,
		baseURL:   baseURL,
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

// dispatch sends an event to all notifiers concurrently and waits for
// all notifications to complete before returning. This ensures that
// goroutines don't outlive the caller (especially during drainEvents
// at shutdown) and prevents unbounded goroutine accumulation.
func (m *Manager) dispatch(evt queue.Event) {
	var wg sync.WaitGroup
	for _, n := range m.notifiers {
		n := n
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.Notify(evt); err != nil {
				m.logger.Errorf("notifier: notification failed for download %d: %v", evt.DownloadID, err)
			} else {
				m.logger.Debugf("notifier: notification sent for download %d (event=%s)", evt.DownloadID, evt.Type)
			}
		}()
	}
	wg.Wait()
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

// NotifyWatchlistResults sends a watchlist notification to all notifiers
// and waits for all to complete before returning. The query parameter is used
// to construct a search URL linking back to the web UI.
func (m *Manager) NotifyWatchlistResults(name string, newPacks, enqueued int, query string) {
	if len(m.notifiers) == 0 {
		return
	}

	var searchURL string
	if m.baseURL != "" && query != "" {
		searchURL = fmt.Sprintf("%s/#search?q=%s", m.baseURL, url.QueryEscape(query))
	}

	event := WatchlistEvent{
		WatchlistName: name,
		NewPacksCount: newPacks,
		EnqueuedCount: enqueued,
		Timestamp:     time.Now().Format(time.RFC3339),
		Query:         query,
		SearchURL:     searchURL,
	}

	var wg sync.WaitGroup
	for _, n := range m.notifiers {
		n := n
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.NotifyWatchlistResults(event); err != nil {
				m.logger.Errorf("notifier: watchlist notification failed for %q: %v", name, err)
			} else {
				m.logger.Infof("notifier: watchlist notification sent for %q (%d new packs)", name, newPacks)
			}
		}()
	}
	wg.Wait()
}

// Notifiers returns the list of configured notifiers (for introspection).
func (m *Manager) Notifiers() []Notifier {
	return m.notifiers
}
