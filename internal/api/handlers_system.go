package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"

	"xdcc_server/internal/config"
	"xdcc_server/internal/store"
)

// =========================================================================
// GET /healthz
// =========================================================================

func (a *API) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// =========================================================================
// GET /readyz
// =========================================================================

func (a *API) handleReadyz(w http.ResponseWriter, r *http.Request) {
	_, err := a.Store.CurrentSchemaVersion(r.Context())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "not ready",
			"error":  "database not available",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// =========================================================================
// GET /api/version
// =========================================================================

func (a *API) handleVersion(w http.ResponseWriter, r *http.Request) {
	v := a.Version
	if v == "" {
		v = "dev"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"version":                       v,
		"min_compatible_client_version": v,
	})
}

// =========================================================================
// GET /api/config
// =========================================================================

func (a *API) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	// Support ?format=yaml for the raw YAML editor in the GUI.
	if r.URL.Query().Get("format") == "yaml" {
		a.handleGetConfigRaw(w, r)
		return
	}

	// Return a copy of the config with the admin token redacted.
	cfg := a.Config.Clone()
	cfg.Security.AdminToken = "***REDACTED***"
	writeJSON(w, http.StatusOK, cfg)
}

// handleGetConfigRaw reads and returns the raw config.yaml file content
// for the Advanced YAML editor in the Settings page.
func (a *API) handleGetConfigRaw(w http.ResponseWriter, r *http.Request) {
	if a.ConfigPath == "" {
		writeError(w, http.StatusInternalServerError, "NO_CONFIG_PATH",
			"config path not available")
		return
	}

	data, err := os.ReadFile(a.ConfigPath)
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "READ_CONFIG_ERROR",
			fmt.Sprintf("reading config file: %v", err))
		return
	}

	// Redact the admin token in the raw YAML output.
	// Match "admin_token: <value>" and replace the value.
	replaced := replaceYAMLValue(string(data), "admin_token", "***REDACTED***")

	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(replaced))
}

// replaceYAMLValue replaces the value of a top-level YAML key. This is a
// simple string-based replacement that preserves comments and formatting.
func replaceYAMLValue(yamlContent, key, replacement string) string {
	// Find the key at the start of a line (possibly indented).
	// Replace its value (everything after ": " until end of line).
	idx := 0
	for {
		pos := findKeyInYAML(yamlContent, key, idx)
		if pos < 0 {
			break
		}
		// pos points to the start of the key.
		// Find the ": " after the key.
		colonEnd := pos + len(key)
		if colonEnd >= len(yamlContent) || yamlContent[colonEnd] != ':' {
			idx = colonEnd + 1
			continue
		}
		// Find start of value (skip ": " or ":").
		valStart := colonEnd + 1
		if valStart < len(yamlContent) && yamlContent[valStart] == ' ' {
			valStart++
		}
		// Find end of value (end of line).
		valEnd := valStart
		for valEnd < len(yamlContent) && yamlContent[valEnd] != '\n' && yamlContent[valEnd] != '\r' {
			valEnd++
		}
		// Replace the value.
		yamlContent = yamlContent[:valStart] + replacement + yamlContent[valEnd:]
		idx = valStart + len(replacement) + 1
	}
	return yamlContent
}

// findKeyInYAML finds the next occurrence of key in yamlContent starting
// from position idx, where the key appears at the beginning of its line
// (possibly after whitespace), followed by ':'. Returns position or -1.
func findKeyInYAML(s, key string, start int) int {
	for start < len(s)-len(key) {
		pos := -1
		// Find next occurrence of key.
		for i := start; i <= len(s)-len(key); i++ {
			if s[i:i+len(key)] == key {
				pos = i
				break
			}
		}
		if pos < 0 {
			return -1
		}
		// Check that this is at the start of a line (possibly indented).
		lineStart := true
		if pos > 0 && s[pos-1] != '\n' && s[pos-1] != '\r' {
			lineStart = false
		}
		// Also allow whitespace before the key.
		if !lineStart && pos > 0 {
			// Check that all characters from last newline to pos are whitespace.
			lastNL := -1
			for j := pos - 1; j >= 0; j-- {
				if s[j] == '\n' || s[j] == '\r' {
					lastNL = j
					break
				}
			}
			wsOnly := true
			for j := lastNL + 1; j < pos; j++ {
				if s[j] != ' ' && s[j] != '\t' {
					wsOnly = false
					break
				}
			}
			lineStart = wsOnly
		}
		if lineStart && pos+len(key) < len(s) && s[pos+len(key)] == ':' {
			return pos
		}
		start = pos + len(key)
	}
	return -1
}

// syncMsgRateLimiter reconfigures the send-message rate limiter to match
// the current config values. This is called after runtime config updates
// so changes to message_rate_limit / message_rate_window_sec take effect
// without a server restart.
func (a *API) syncMsgRateLimiter() {
	limit, windowSec := a.Config.GetMessageRateLimit()
	if limit > 0 && windowSec > 0 {
		if rl := a.MsgRateLimiter.Load(); rl != nil {
			// Reconfigure existing limiter (method is mutex-protected).
			rl.Reconfigure(limit, time.Duration(windowSec)*time.Second)
		} else {
			a.MsgRateLimiter.Store(NewRateLimiter(limit, time.Duration(windowSec)*time.Second))
		}
	} else {
		// Rate limiting disabled — nil out the limiter.
		a.MsgRateLimiter.Store(nil)
	}
}

// =========================================================================
// PUT /api/config
// =========================================================================

func (a *API) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		writeError(w, http.StatusBadRequest, "READ_ERROR", "Failed to read request body")
		return
	}

	// Check Content-Type to determine handling:
	// - text/yaml (Advanced tab): skip validation, write raw bytes directly
	// - application/json or other: parse, validate, and apply as structured config
	contentType := r.Header.Get("Content-Type")

	if contentType == "text/yaml" {
		// For raw YAML saves from the Advanced tab, use partial validation
		// (only critical fields that could cause immediate crashes). Fields
		// with sane defaults (like temp_dir, dest_dir, etc.) are intentionally
		// skipped since the user may have left them empty intentionally.
		// YAML syntax errors are still reported since they indicate invalid YAML.
		var newCfg config.Config
		if err := yaml.Unmarshal(bodyBytes, &newCfg); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_YAML",
				fmt.Sprintf("YAML syntax error: %v", err))
			return
		}

		// Partial validation — only critical fields that could cause crashes
		if err := newCfg.ValidatePartial(); err != nil {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR",
				fmt.Sprintf("config validation failed: %v", err))
			return
		}

		// SnapshotAndApply atomically captures the live config (so we can
		// preserve the admin token), lets us overlay the parsed newCfg onto
		// the snapshot, and installs the result via Replace — all under a
		// single RLock. The returned pre-snapshot is intentionally unused
		// here because SaveRaw writes the raw body bytes verbatim and does
		// not need a diff.
		a.Config.SnapshotAndApply(func(snap *config.Config) {
			// Always preserve the live admin token: the raw YAML editor
			// round-trips with the redacted placeholder, so a user-supplied
			// token would always be the redacted one.
			newCfg.Security.AdminToken = snap.Security.AdminToken
			// Use snap.Replace (field-by-field copy) instead of *snap = newCfg
			// to avoid copying snap's embedded sync.RWMutex, which would be
			// flagged by `go vet` as "copies lock value".
			snap.Replace(&newCfg)
		})

		// Sync rate limiter with new config values.
		a.syncMsgRateLimiter()

		// Save raw YAML bytes directly, preserving comments and formatting.
		if a.ConfigPath != "" {
			if err := a.Config.SaveRaw(a.ConfigPath, bodyBytes); err != nil {
				a.Logger.Errorf("saving raw config to %s: %v", a.ConfigPath, err)
				writeError(w, http.StatusInternalServerError, "SAVE_ERROR",
					fmt.Sprintf("config save failed: %v", err))
				return
			}
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
		return
	}

	// Parse the incoming JSON into a Config struct for validation.
	var newCfg config.Config
	if err := yaml.Unmarshal(bodyBytes, &newCfg); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_YAML",
			fmt.Sprintf("YAML parsing error: %v", err))
		return
	}

	// Validate the new config before applying or persisting. Validation
	// runs before SnapshotAndApply so a failure leaves the live config
	// untouched (Validate does not depend on the live admin token).
	if err := newCfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR",
			fmt.Sprintf("config validation failed: %v", err))
		return
	}

	// SnapshotAndApply atomically captures the live config (so we can
	// preserve the admin token when the client sent the redacted placeholder
	// or left the field empty), overlays the validated newCfg onto the
	// snapshot, and installs the result via Replace — all under a single
	// RLock. The returned pre-snapshot is used as the "old" side of the
	// diff computed by ApplyPartial.
	oldCfg := a.Config.SnapshotAndApply(func(snap *config.Config) {
		if newCfg.Security.AdminToken == "" || newCfg.Security.AdminToken == "***REDACTED***" {
			newCfg.Security.AdminToken = snap.Security.AdminToken
		}
		// Use snap.Replace (field-by-field copy) instead of *snap = newCfg
		// to avoid copying snap's embedded sync.RWMutex, which would be
		// flagged by `go vet` as "copies lock value".
		snap.Replace(&newCfg)
	})

	// Sync rate limiter with new config values.
	a.syncMsgRateLimiter()

	// Persist using partial update to preserve comments & formatting.
	if a.ConfigPath != "" {
		if err := a.Config.ApplyPartial(a.ConfigPath, oldCfg); err != nil {
			a.Logger.Errorf("saving config to %s: %v", a.ConfigPath, err)
			writeError(w, http.StatusInternalServerError, "SAVE_ERROR",
				fmt.Sprintf("config updated in memory but failed to persist: %v", err))
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// =========================================================================
// PATCH /api/config/theme
// =========================================================================

func (a *API) handleUpdateTheme(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Theme string `json:"theme"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}

	// Validate theme value
	if body.Theme != "dark" && body.Theme != "light" {
		writeError(w, http.StatusBadRequest, "INVALID_THEME",
			fmt.Sprintf("theme must be 'dark' or 'light', got %q", body.Theme))
		return
	}

	// SnapshotAndApply atomically captures the pre-mutation state and installs
	// the post-mutation state under a single RLock, returning the pre-snapshot
	// (oldCfg) for use as the "old" side of the diff when persisting.
	// This closes the TOCTOU window that would open if we called Clone() twice
	// (once for oldCfg, once for the new value).
	oldCfg := a.Config.SnapshotAndApply(func(snap *config.Config) {
		snap.UI.Theme = body.Theme
	})

	// Persist only the theme change to disk using partial update.
	if a.ConfigPath != "" {
		if err := a.Config.ApplyPartial(a.ConfigPath, oldCfg); err != nil {
			a.Logger.Errorf("saving theme to %s: %v", a.ConfigPath, err)
			writeError(w, http.StatusInternalServerError, "SAVE_ERROR",
				fmt.Sprintf("theme updated in memory but failed to persist: %v", err))
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// =========================================================================
// GET /api/stats
// =========================================================================

func (a *API) handleStats(w http.ResponseWriter, r *http.Request) {
	queue, _ := a.Store.GetQueue(r.Context())

	queueCount := 0
	activeCount := 0
	totalSpeedBPS := int64(0)
	for _, item := range queue {
		switch item.Status {
		case "queued":
			queueCount++
		case "downloading":
			activeCount++
			totalSpeedBPS += item.SpeedBPS
		}
	}

	totalDownloadedBytes, _ := a.Store.GetTotalDownloadedBytes(r.Context())

	_, totalHistory, _ := a.Store.GetDownloadHistory(r.Context(), 1, 1, store.HistoryFilter{})

	servers, _ := a.Store.ListServers(r.Context())
	serverCount := len(servers)

	uptimeSeconds := int64(time.Since(a.StartTime).Seconds())

	// Get disk info
	di, err := getDiskInfo(a.Config.GetDestDir())
	diskFreeBytes := int64(0)
	diskTotalBytes := int64(0)
	if err == nil {
		diskFreeBytes = di.available
		diskTotalBytes = di.total
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"active_downloads":       activeCount,
		"queued_downloads":       queueCount,
		"total_completed":        totalHistory,
		"connected_servers":      serverCount,
		"total_downloaded_bytes": totalDownloadedBytes,
		"average_speed_bps":      totalSpeedBPS,
		"uptime_seconds":         uptimeSeconds,
		"disk_free_bytes":        diskFreeBytes,
		"disk_total_bytes":       diskTotalBytes,
		"started_at":             a.StartTime.Format(time.RFC3339),
		"go_version":             runtime.Version(),
		"os":                     runtime.GOOS + "/" + runtime.GOARCH,
	})
}

// =========================================================================
// GET /api/status
// =========================================================================

func (a *API) handleStatus(w http.ResponseWriter, r *http.Request) {
	warnings := make([]string, 0)
	info := make(map[string]interface{})

	di, err := getDiskInfo(a.Config.GetDestDir())
	diskFreeBytes := int64(0)
	diskTotalBytes := int64(0)
	if err == nil {
		diskFreeBytes = di.available
		diskTotalBytes = di.total
		if di.available < 1*1024*1024*1024 {
			warnings = append(warnings, "Low disk space in download directory")
		}
	}

	servers, _ := a.Store.ListServers(r.Context())
	connectedServers := 0
	totalServers := len(servers)
	for _, srv := range servers {
		if srv.Status == "connected" {
			connectedServers++
		}
	}
	info["servers"] = map[string]int{"connected": connectedServers, "total": totalServers}

	queue, _ := a.Store.GetQueue(r.Context())
	activeDownloads := 0
	for _, item := range queue {
		if item.Status == "downloading" {
			activeDownloads++
		}
	}
	info["active_downloads"] = activeDownloads

	uptimeSeconds := int64(time.Since(a.StartTime).Seconds())

	status := "healthy"
	if len(warnings) > 0 {
		status = "degraded"
	}
	if totalServers > 0 && connectedServers == 0 {
		warnings = append(warnings, "No IRC servers connected")
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":            status,
		"warnings":          warnings,
		"info":              info,
		"uptime_seconds":    uptimeSeconds,
		"disk_free_bytes":   diskFreeBytes,
		"disk_total_bytes":  diskTotalBytes,
		"token_ttl_minutes": a.Config.GetTokenTTLMinutes(),
		"ui_theme":          a.Config.GetUITheme(),           // public — used by frontend for initial theme
		"messages_enabled":  a.Config.GetEnableMessageSend(), // public — used by frontend to show/hide send button
	})
}

// =========================================================================
// disk helpers
// =========================================================================

type diskInfo struct {
	available int64
	total     int64
	used      int64
}

// =========================================================================
// POST /api/admin/export
// =========================================================================

func (a *API) handleAdminExport(w http.ResponseWriter, r *http.Request) {
	export, err := a.Store.ExportData(r.Context())
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "EXPORT_ERROR", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"exported_at": time.Now().Format(time.RFC3339),
		"data":        export,
	})
}

// =========================================================================
// GET /api/logs
// =========================================================================

func (a *API) handleLogs(w http.ResponseWriter, r *http.Request) {
	count := 100
	if n := r.URL.Query().Get("count"); n != "" {
		if parsed, err := parseInt(n); err == nil && parsed > 0 {
			count = parsed
		}
	}

	var entries []interface{}
	if a.LogBroadcaster != nil {
		for _, e := range a.LogBroadcaster.RecentEntries(count) {
			entries = append(entries, map[string]interface{}{
				"timestamp": e.Timestamp,
				"level":     e.Level,
				"message":   e.Message,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"logs":  entries,
		"count": len(entries),
	})
}

// =========================================================================
// POST /api/admin/import
// =========================================================================

func (a *API) handleAdminImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Data *store.ExportData `json:"data"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}

	if body.Data == nil {
		writeError(w, http.StatusBadRequest, "MISSING_DATA", "data is required")
		return
	}

	if err := a.Store.ImportData(r.Context(), body.Data); err != nil {
		a.logAndError(w, http.StatusInternalServerError, "IMPORT_ERROR", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "imported"})
}

// =========================================================================
// GET /api/setup/status
// =========================================================================

func (a *API) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	completed := a.Config.GetSetupCompleted()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"setup_completed": completed,
	})
}

// =========================================================================
// POST /api/setup/bootstrap
// =========================================================================

func (a *API) handleSetupBootstrap(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Nickname      string `json:"nickname"`
		ServerAddress string `json:"server_address"`
		ServerPort    int    `json:"server_port"`
		DownloadDir   string `json:"download_dir"`
		TempDir       string `json:"temp_dir"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}

	// SnapshotAndApply atomically captures the pre-mutation config (so we
	// can compute a diff for partial persistence later) and installs the
	// mutated version via Replace — all under proper locking. This closes
	// the data race that existed when the handler wrote directly to Config
	// fields without acquiring mu.Lock().
	oldCfg := a.Config.SnapshotAndApply(func(snap *config.Config) {
		if body.Nickname != "" {
			snap.IRC.Nickname = body.Nickname
		}
		if body.ServerAddress != "" {
			port := body.ServerPort
			if port < 1 || port > 65535 {
				port = 6667
			}
			snap.IRC.DefaultServers = []config.ServerConfig{
				{
					Address:     body.ServerAddress,
					Port:        port,
					AutoConnect: true,
				},
			}
		}
		if body.DownloadDir != "" {
			snap.Download.DestDir = body.DownloadDir
		}
		if body.TempDir != "" {
			snap.Download.TempDir = body.TempDir
		}
		snap.UI.SetupCompleted = true
	})

	// Read directories from the now-updated live config via the thread-safe
	// getter (acquires mu.RLock internally).
	dlCfg := a.Config.GetDownloadConfig()
	for _, dir := range []string{dlCfg.TempDir, dlCfg.DestDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			a.logAndError(w, http.StatusInternalServerError, "MKDIR_ERROR",
				fmt.Sprintf("creating directory %s: %v", dir, err))
			return
		}
	}

	// Persist using ApplyPartial to preserve comments and formatting.
	// Falls back to a full Save() automatically for non-scalar changes
	// (e.g. DefaultServers slice).
	if a.ConfigPath != "" {
		if err := a.Config.ApplyPartial(a.ConfigPath, oldCfg); err != nil {
			a.Logger.Errorf("saving bootstrap config to %s: %v", a.ConfigPath, err)
			writeError(w, http.StatusInternalServerError, "SAVE_ERROR",
				fmt.Sprintf("setup completed in memory but failed to persist config: %v", err))
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "setup_completed"})
}
