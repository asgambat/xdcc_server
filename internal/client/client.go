// Package client implements an HTTP client for delegating XDCC operations to
// a remote xdcc-server. Used by CLI commands (xdcc-dl, xdcc-browse, xdcc-search)
// when the --command-server flag is specified.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"xdcc-go/internal/store"
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ServerError is returned when the server responds with an HTTP error.
type ServerError struct {
	StatusCode int
	Code       string `json:"code"`
	Message    string `json:"message"`
}

func (e *ServerError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("server error %d: %s — %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("server error: HTTP %d", e.StatusCode)
}

// VersionMismatchError is returned when the server version is incompatible.
type VersionMismatchError struct {
	ServerVersion string
	ClientVersion string
}

func (e *VersionMismatchError) Error() string {
	return fmt.Sprintf("server version %q is incompatible with client %q",
		e.ServerVersion, e.ClientVersion)
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// Client is an HTTP client for the xdcc-server REST API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New creates a new client for the given server base URL.
func New(baseURL string) *Client {
	// Strip trailing slash
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// BaseURL returns the server base URL.
func (c *Client) BaseURL() string { return c.baseURL }

// ---------------------------------------------------------------------------
// Version check
// ---------------------------------------------------------------------------

// ServerVersion holds the version info returned by GET /api/version.
type ServerVersion struct {
	Version                    string `json:"version"`
	MinCompatibleClientVersion string `json:"min_compatible_client_version"`
}

// CheckVersion verifies the server is reachable and returns version info.
func (c *Client) CheckVersion() (*ServerVersion, error) {
	var v ServerVersion
	if err := c.doJSON("GET", "/api/version", nil, &v); err != nil {
		return nil, fmt.Errorf("checking server version: %w", err)
	}
	return &v, nil
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// SearchResult mirrors the aggregated search API response.
type SearchResult struct {
	Packs      []PackResult `json:"packs"`
	Total      int          `json:"total"`
	Page       int          `json:"page"`
	PageSize   int          `json:"page_size"`
	TotalPages int          `json:"total_pages"`
	Provenance string       `json:"provenance"`
	Warnings   []string     `json:"warnings,omitempty"`
}

// PackResult represents a single XDCC pack in search results.
type PackResult struct {
	Bot        string `json:"bot,omitempty"`
	PackNumber int    `json:"pack_number,omitempty"`
	Filename   string `json:"filename,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Server     string `json:"server,omitempty"`
	Channel    string `json:"channel,omitempty"`
}

// SearchOptions are the query parameters for a search request.
type SearchOptions struct {
	Query    string
	Prefix   string
	Bot      string
	Ext      []string
	Compact   bool
	VideoOnly bool
	Page      int
	PageSize  int
}

// Search performs an aggregated search via the server.
func (c *Client) Search(opts *SearchOptions) (*SearchResult, error) {
	if opts == nil {
		opts = &SearchOptions{}
	}

	// Build query string with proper URL escaping
	v := url.Values{}
	v.Set("q", opts.Query)
	if opts.Prefix != "" {
		v.Set("prefix", opts.Prefix)
	}
	if opts.Bot != "" {
		v.Set("bot", opts.Bot)
	}
	if len(opts.Ext) > 0 {
		v.Set("ext", strings.Join(opts.Ext, ","))
	}
	if opts.Compact {
		v.Set("compact", "true")
	}
	if opts.VideoOnly {
		v.Set("video_only", "true")
	}
	if opts.Page > 0 {
		v.Set("page", strconv.Itoa(opts.Page))
	}
	if opts.PageSize > 0 {
		v.Set("pageSize", strconv.Itoa(opts.PageSize))
	}

	path := "/api/search?" + v.Encode()

	var result SearchResult
	if err := c.doJSON("GET", path, nil, &result); err != nil {
		return nil, fmt.Errorf("server search failed: %w", err)
	}
	return &result, nil
}

// ---------------------------------------------------------------------------
// Download delegation
// ---------------------------------------------------------------------------

// EnqueueResponse is the response from POST /api/downloads.
type EnqueueResponse struct {
	ID int64 `json:"id"`
}

// EnqueueDownload delegates a download to the server.
func (c *Client) EnqueueDownload(download store.DownloadRecord) (int64, error) {
	body := map[string]interface{}{
		"pack_message":   download.PackMessage,
		"bot":            download.Bot,
		"server_address": download.ServerAddress,
		"channel":        download.Channel,
		"filename":       download.Filename,
		"file_size":      download.FileSize,
	}
	if download.Priority > 0 {
		body["priority"] = download.Priority
	}

	var resp EnqueueResponse
	if err := c.doJSON("POST", "/api/downloads", body, &resp); err != nil {
		return 0, fmt.Errorf("delegating download: %w", err)
	}
	return resp.ID, nil
}

// GetDownload retrieves a download record by ID.
func (c *Client) GetDownload(id int64) (*store.DownloadRecord, error) {
	url := fmt.Sprintf("/api/downloads/%d", id)
	var rec store.DownloadRecord
	if err := c.doJSON("GET", url, nil, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// ---------------------------------------------------------------------------
// Progress polling helpers
// ---------------------------------------------------------------------------

// ProgressCallback is called on each poll tick with the current download status.
type ProgressCallback func(rec *store.DownloadRecord)

// PollDownload polls the download status at the given interval until the
// download reaches a terminal state (completed, failed, skipped) or the
// timeout is reached.
//
// interval is the polling interval (e.g., 1 second).
// timeout is the maximum time to poll (0 = no timeout).
func (c *Client) PollDownload(id int64, interval, timeout time.Duration, cb ProgressCallback) (*store.DownloadRecord, error) {
	var timeoutCh <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timeoutCh = t.C
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Terminal states
	isTerminal := func(status string) bool {
		return status == store.DownloadStatusCompleted ||
			status == store.DownloadStatusFailed ||
			status == store.DownloadStatusSkipped
	}

	for {
		rec, err := c.GetDownload(id)
		if err != nil {
			// Retry on transient errors
			select {
			case <-ticker.C:
				continue
			case <-timeoutCh:
				return nil, fmt.Errorf("timed out polling download %d", id)
			}
		}

		if cb != nil {
			cb(rec)
		}

		if isTerminal(rec.Status) {
			return rec, nil
		}

		select {
		case <-ticker.C:
		case <-timeoutCh:
			return rec, fmt.Errorf("timed out waiting for download %d to complete (status: %s)", id, rec.Status)
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (c *Client) doJSON(method, path string, body, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshaling request: %w", err)
		}
		reqBody = strings.NewReader(string(b))
	}

	req, err := http.NewRequestWithContext(context.Background(), method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("server unreachable at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var srvErr ServerError
		if err := json.NewDecoder(resp.Body).Decode(&srvErr); err == nil && srvErr.Message != "" {
			srvErr.StatusCode = resp.StatusCode
			return &srvErr
		}
		return &ServerError{StatusCode: resp.StatusCode}
	}

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}

	return nil
}
