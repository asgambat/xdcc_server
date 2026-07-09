package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lrstanley/girc"
	"xdcc_server/internal/searchagg"
	"xdcc_server/internal/store"
)

// =========================================================================
// GET /api/search — aggregated search
// =========================================================================

func (a *API) handleSearch(w http.ResponseWriter, r *http.Request) {
	if a.SearchAggregator == nil {
		writeError(w, http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE", "Search aggregator not available")
		return
	}

	q := r.URL.Query()
	opts := searchagg.SearchOptions{
		Query:     q.Get("q"),
		Prefix:    q.Get("prefix"),
		Bot:       q.Get("bot"),
		Compact:   q.Get("compact") == "true",
		VideoOnly: q.Get("video_only") == "true",
		AudioOnly: q.Get("audio_only") == "true",
		BooksOnly: q.Get("books_only") == "true",
		ZipOnly:   q.Get("zip_only") == "true",
		MinSize:   q.Get("min_size"),
		MaxSize:   q.Get("max_size"),
		Page:      1,
		PageSize:  50,
	}
	if ext := q.Get("ext"); ext != "" {
		opts.Ext = strings.Split(ext, ",")
	}
	if prov := q["providers"]; len(prov) > 0 {
		opts.Providers = prov
	}
	if p := q.Get("page"); p != "" {
		_, _ = fmt.Sscanf(p, "%d", &opts.Page)
	}
	if ps := q.Get("pageSize"); ps != "" {
		_, _ = fmt.Sscanf(ps, "%d", &opts.PageSize)
	}
	if opts.Page < 1 {
		opts.Page = 1
	}
	if opts.PageSize < 1 || opts.PageSize > 500 {
		opts.PageSize = 50
	}

	// Create context with 30s timeout
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, err := a.SearchAggregator.Search(ctx, opts)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			writeError(w, http.StatusGatewayTimeout, "SEARCH_TIMEOUT", "Search request timed out")
		case errors.Is(err, context.Canceled):
			// Client disconnected — write nothing, just log at debug level
			a.Logger.Debugf("search request cancelled: %v", err)
		default:
			a.logAndError(w, http.StatusInternalServerError, "SEARCH_ERROR", err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// =========================================================================
// GET /api/search/presets
// =========================================================================

func (a *API) handleListPresets(w http.ResponseWriter, r *http.Request) {
	if a.SearchAggregator == nil {
		writeError(w, http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE", "Search aggregator not available")
		return
	}

	presetList, err := a.SearchAggregator.ListPresets(r.Context())
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "LIST_PRESETS_ERROR", err.Error())
		return
	}

	// Enrich response with parsed filter fields
	type presetResp struct {
		store.SearchPreset
		Providers []string `json:"providers,omitempty"`
		MinSize   string   `json:"min_size,omitempty"`
		MaxSize   string   `json:"max_size,omitempty"`
	}
	resp := make([]presetResp, 0, len(presetList))
	for _, p := range presetList {
		f := searchagg.ParseFilters(p.FiltersJSON)
		resp = append(resp, presetResp{
			SearchPreset: p,
			Providers:    f.Providers,
			MinSize:      f.MinSize,
			MaxSize:      f.MaxSize,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// =========================================================================
// POST /api/search/presets
// =========================================================================

func (a *API) handleCreatePreset(w http.ResponseWriter, r *http.Request) {
	if a.SearchAggregator == nil {
		writeError(w, http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE", "Search aggregator not available")
		return
	}

	var body struct {
		Name      string   `json:"name"`
		Query     string   `json:"query"`
		Providers []string `json:"providers"`
		MinSize   string   `json:"min_size"`
		MaxSize   string   `json:"max_size"`
		IsDefault bool     `json:"is_default"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "name is required")
		return
	}
	if body.Query == "" {
		writeError(w, http.StatusBadRequest, "MISSING_QUERY", "query is required")
		return
	}

	f := searchagg.FilterOptions{
		Providers: body.Providers,
		MinSize:   body.MinSize,
		MaxSize:   body.MaxSize,
	}
	filtersJSON := searchagg.SerializeFilters(f)

	id, err := a.SearchAggregator.CreatePreset(r.Context(), body.Name, body.Query, filtersJSON, body.IsDefault)
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "CREATE_PRESET_ERROR", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

// =========================================================================
// PUT /api/search/presets/:presetID
// =========================================================================

func (a *API) handleUpdatePreset(w http.ResponseWriter, r *http.Request) {
	if a.SearchAggregator == nil {
		writeError(w, http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE", "Search aggregator not available")
		return
	}

	id, err := parseID(chi.URLParam(r, "presetID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid preset ID")
		return
	}

	// Get existing preset
	existing, err := a.SearchAggregator.GetPreset(r.Context(), id)
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "GET_PRESET_ERROR", err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "PRESET_NOT_FOUND", fmt.Sprintf("Preset %d not found", id))
		return
	}

	var body struct {
		Name      string   `json:"name"`
		Query     string   `json:"query"`
		Providers []string `json:"providers"`
		MinSize   string   `json:"min_size"`
		MaxSize   string   `json:"max_size"`
		IsDefault bool     `json:"is_default"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}

	// Build updated preset
	updated := *existing
	if body.Name != "" {
		updated.Name = body.Name
	}
	if body.Query != "" {
		updated.Query = body.Query
	}
	f := searchagg.FilterOptions{
		Providers: body.Providers,
		MinSize:   body.MinSize,
		MaxSize:   body.MaxSize,
	}
	updated.FiltersJSON = searchagg.SerializeFilters(f)
	updated.IsDefault = body.IsDefault

	if err := a.SearchAggregator.UpdatePreset(r.Context(), updated); err != nil {
		a.logAndError(w, http.StatusInternalServerError, "UPDATE_PRESET_ERROR", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// =========================================================================
// DELETE /api/search/presets/:presetID
// =========================================================================

func (a *API) handleDeletePreset(w http.ResponseWriter, r *http.Request) {
	if a.SearchAggregator == nil {
		writeError(w, http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE", "Search aggregator not available")
		return
	}

	id, err := parseID(chi.URLParam(r, "presetID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid preset ID")
		return
	}

	if err := a.SearchAggregator.DeletePreset(r.Context(), id); err != nil {
		a.logAndError(w, http.StatusInternalServerError, "DELETE_PRESET_ERROR", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// =========================================================================
// GET /api/watchlists
// =========================================================================

func (a *API) handleListWatchlists(w http.ResponseWriter, r *http.Request) {
	wlList, err := a.Store.ListWatchlists(r.Context())
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "LIST_WATCHLISTS_ERROR", err.Error())
		return
	}

	// Enrich response with parsed filter fields
	type wlResp struct {
		store.Watchlist
		Providers []string `json:"providers,omitempty"`
		MinSize   string   `json:"min_size,omitempty"`
		MaxSize   string   `json:"max_size,omitempty"`
	}
	resp := make([]wlResp, 0, len(wlList))
	for _, w := range wlList {
		f := searchagg.ParseFilters(w.FiltersJSON)
		resp = append(resp, wlResp{
			Watchlist: w,
			Providers: f.Providers,
			MinSize:   f.MinSize,
			MaxSize:   f.MaxSize,
		})
	}

	writeJSON(w, http.StatusOK, resp)
}

// =========================================================================
// POST /api/watchlists
// =========================================================================

func (a *API) handleCreateWatchlist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name            string   `json:"name"`
		Query           string   `json:"query"`
		IntervalMinutes int      `json:"interval_minutes"`
		Providers       []string `json:"providers"`
		MinSize         string   `json:"min_size"`
		MaxSize         string   `json:"max_size"`
		NotifyEnabled   *bool    `json:"notify_enabled"`
		Enabled         *bool    `json:"enabled"`
		AutoEnqueue     *bool    `json:"auto_enqueue"`
		FiltersJSON     string   `json:"filters_json"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "name is required")
		return
	}
	if body.Query == "" {
		writeError(w, http.StatusBadRequest, "MISSING_QUERY", "query is required")
		return
	}
	interval := body.IntervalMinutes
	if interval < 5 {
		interval = 60 // default to 60 minutes, minimum 5
	}

	// Default enabled to true if not provided
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	autoEnqueue := false
	if body.AutoEnqueue != nil {
		autoEnqueue = *body.AutoEnqueue
	}
	notifyEnabled := true
	if body.NotifyEnabled != nil {
		notifyEnabled = *body.NotifyEnabled
	}

	// Serialize filters from explicit fields
	f := searchagg.FilterOptions{
		Providers: body.Providers,
		MinSize:   body.MinSize,
		MaxSize:   body.MaxSize,
	}
	filtersJSON := searchagg.SerializeFilters(f)

	id, err := a.Store.AddWatchlist(r.Context(), store.Watchlist{
		Name:            body.Name,
		Query:           body.Query,
		IntervalMinutes: interval,
		FiltersJSON:     filtersJSON,
		NotifyEnabled:   notifyEnabled,
		Enabled:         enabled,
		AutoEnqueue:     autoEnqueue,
	})
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "CREATE_WATCHLIST_ERROR", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

// =========================================================================
// PUT /api/watchlists/:watchlistID
// =========================================================================

func (a *API) handleUpdateWatchlist(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "watchlistID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid watchlist ID")
		return
	}

	// Get existing
	existing, err := a.Store.GetWatchlist(r.Context(), id)
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "GET_WATCHLIST_ERROR", err.Error())
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "WATCHLIST_NOT_FOUND", fmt.Sprintf("Watchlist %d not found", id))
		return
	}

	var body struct {
		Name            string   `json:"name"`
		Query           string   `json:"query"`
		IntervalMinutes int      `json:"interval_minutes"`
		Providers       []string `json:"providers"`
		MinSize         string   `json:"min_size"`
		MaxSize         string   `json:"max_size"`
		NotifyEnabled   *bool    `json:"notify_enabled"`
		Enabled         *bool    `json:"enabled"`
		AutoEnqueue     *bool    `json:"auto_enqueue"`
		FiltersJSON     string   `json:"filters_json"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}

	updated := *existing
	if body.Name != "" {
		updated.Name = body.Name
	}
	if body.Query != "" {
		updated.Query = body.Query
	}
	if body.IntervalMinutes >= 5 {
		updated.IntervalMinutes = body.IntervalMinutes
	}
	// Only update fields explicitly provided in JSON
	// (not sent = nil pointer → keep existing value)
	if body.Enabled != nil {
		updated.Enabled = *body.Enabled
	}
	if body.AutoEnqueue != nil {
		updated.AutoEnqueue = *body.AutoEnqueue
	}
	if body.NotifyEnabled != nil {
		updated.NotifyEnabled = *body.NotifyEnabled
	}
	// Serialize filters from explicit fields (if providers/min_size/max_size sent)
	if body.Providers != nil || body.MinSize != "" || body.MaxSize != "" {
		f := searchagg.FilterOptions{
			Providers: body.Providers,
			MinSize:   body.MinSize,
			MaxSize:   body.MaxSize,
		}
		updated.FiltersJSON = searchagg.SerializeFilters(f)
	} else if body.FiltersJSON != "" {
		updated.FiltersJSON = body.FiltersJSON
	}

	if err := a.Store.UpdateWatchlist(r.Context(), updated); err != nil {
		a.logAndError(w, http.StatusInternalServerError, "UPDATE_WATCHLIST_ERROR", err.Error())
		return
	}

	// When the user manually edits and saves a watchlist (Update button),
	// reset the results counter to 0 so the UI reflects a fresh state.
	// (RunWatchlist preserves results on no-changes; this is the explicit reset.)
	_ = a.Store.SetWatchlistChecked(r.Context(), id, "", "[]")

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// =========================================================================
// DELETE /api/watchlists/:watchlistID
// =========================================================================

func (a *API) handleDeleteWatchlist(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "watchlistID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid watchlist ID")
		return
	}

	if err := a.Store.DeleteWatchlist(r.Context(), id); err != nil {
		a.logAndError(w, http.StatusInternalServerError, "DELETE_WATCHLIST_ERROR", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// =========================================================================
// POST /api/watchlists/:watchlistID/run
// =========================================================================

func (a *API) handleRunWatchlist(w http.ResponseWriter, r *http.Request) {
	if a.SearchAggregator == nil {
		writeError(w, http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE", "Search aggregator not available")
		return
	}

	id, err := parseID(chi.URLParam(r, "watchlistID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid watchlist ID")
		return
	}

	// Get the watchlist
	wl, err := a.Store.GetWatchlist(r.Context(), id)
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "GET_WATCHLIST_ERROR", err.Error())
		return
	}
	if wl == nil {
		writeError(w, http.StatusNotFound, "WATCHLIST_NOT_FOUND", fmt.Sprintf("Watchlist %d not found", id))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	result, err := a.SearchAggregator.RunWatchlist(ctx, *wl)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			writeError(w, http.StatusGatewayTimeout, "WATCHLIST_TIMEOUT", "Watchlist execution timed out")
		case errors.Is(err, context.Canceled):
			a.Logger.Debugf("watchlist execution cancelled: %v", err)
		default:
			a.logAndError(w, http.StatusInternalServerError, "RUN_WATCHLIST_ERROR", err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// =========================================================================
// GET /api/search/providers — provider status and insights
// =========================================================================

func (a *API) handleGetProviders(w http.ResponseWriter, r *http.Request) {
	if a.SearchAggregator == nil {
		writeError(w, http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE", "Search aggregator not available")
		return
	}

	// Return both states and insights
	states := a.SearchAggregator.GetProviderStates(r.Context())
	insights, err := a.SearchAggregator.GetProviderInsights(r.Context())
	if err != nil {
		// Non-fatal — just log
		a.Logger.Warnf("getting provider insights: %v", err)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"states":   states,
		"insights": insights,
	})
}

// =========================================================================
// PATCH /api/search/providers/:providerName — enable/disable provider
// =========================================================================

func (a *API) handlePatchProvider(w http.ResponseWriter, r *http.Request) {
	if a.SearchAggregator == nil {
		writeError(w, http.StatusServiceUnavailable, "SEARCH_UNAVAILABLE", "Search aggregator not available")
		return
	}

	name := chi.URLParam(r, "providerName")

	var body struct {
		Enabled bool `json:"enabled"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}

	if body.Enabled {
		a.SearchAggregator.EnableProvider(name)
	} else {
		a.SearchAggregator.DisableProvider(name)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"provider": name,
		"enabled":  body.Enabled,
	})
}

// =========================================================================
// POST /api/xdcc/parse — parse raw XDCC command
// =========================================================================

func (a *API) handleParseXDCC(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Command string `json:"command"`
		Message string `json:"message"` // alternative field name (frontend compatibility)
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}

	// Support both "command" (backend convention) and "message" (frontend sent this)
	cmd := strings.TrimSpace(body.Command)
	if cmd == "" {
		cmd = strings.TrimSpace(body.Message)
	}
	if cmd == "" {
		writeError(w, http.StatusBadRequest, "MISSING_COMMAND", "command is required")
		return
	}

	// Parse common XDCC command formats:
	// /msg Bot XDCC SEND #123
	// /msg Bot xdcc send #123
	// Bot: xdcc send #123
	// xdcc send #123 (implicit bot)

	bot := ""
	packNum := 0

	// Format: /msg <bot> XDCC SEND #<num>
	if strings.HasPrefix(cmd, "/msg ") {
		parts := strings.Fields(cmd)
		if len(parts) >= 4 && strings.EqualFold(parts[2], "XDCC") && strings.EqualFold(parts[3], "SEND") && len(parts) >= 5 {
			bot = parts[1]
			_, _ = fmt.Sscanf(parts[4], "#%d", &packNum)
		}
	}

	// Format: <bot>: xdcc send #<num>
	if bot == "" && packNum == 0 {
		if idx := strings.Index(cmd, ":"); idx > 0 {
			rest := strings.TrimSpace(cmd[idx+1:])
			if strings.Contains(strings.ToLower(rest), "xdcc send") {
				bot = strings.TrimSpace(cmd[:idx])
				parts := strings.Fields(rest)
				for i, p := range parts {
					if strings.EqualFold(p, "send") && i+1 < len(parts) {
						_, _ = fmt.Sscanf(parts[i+1], "#%d", &packNum)
						break
					}
				}
			}
		}
	}

	// Format: xdcc send #<num> (bot unknown)
	if bot == "" && packNum == 0 {
		parts := strings.Fields(cmd)
		for i, p := range parts {
			if strings.EqualFold(p, "send") && i+1 < len(parts) {
				_, _ = fmt.Sscanf(parts[i+1], "#%d", &packNum)
				break
			}
		}
	}

	if packNum == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"parsed":       false,
			"bot":          bot,
			"pack_number":  0,
			"pack_message": "",
			"original":     cmd,
			"error":        "no pack number found in command",
		})
		return
	}

	// If bot is not specified, search for it on all connected servers via WHOIS
	var serverAddress string
	if bot != "" && a.IRCManager != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		serverAddress = findBotOnConnectedServers(ctx, a.IRCManager, bot)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"parsed":         true,
		"bot":            bot,
		"pack_number":    packNum,
		"pack_message":   fmt.Sprintf("xdcc send #%d", packNum),
		"original":       cmd,
		"server_address": serverAddress, // empty if not found or bot was already specified
	})
}

// findBotOnConnectedServers performs parallel WHOIS on all connected servers
// to find which one the bot is on. Returns the server address (host:port) of the
// first server where the bot responds to WHOIS, or empty string if not found.
// Maximum 10 concurrent WHOIS requests.
func findBotOnConnectedServers(ctx context.Context, ircMgr IRCManager, bot string) string {
	servers := ircMgr.GetServers()

	// Collect connected servers
	var connectedServers []struct {
		id   int64
		addr string
	}
	for _, srv := range servers {
		if srv.Status != "connected" {
			continue
		}
		addr := srv.Address
		if srv.Port != 0 {
			addr = fmt.Sprintf("%s:%d", srv.Address, srv.Port)
		}
		connectedServers = append(connectedServers, struct {
			id   int64
			addr string
		}{srv.ID, addr})
	}

	if len(connectedServers) == 0 {
		return ""
	}

	const maxParallel = 10

	// Derive a cancelable context so we can signal all inflight goroutines
	// as soon as the first hit is found, instead of letting them run until
	// the parent timeout or their internal 5s WHOIS timeout.
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()

	resultCh := make(chan string, 1)
	var wg sync.WaitGroup

	// Limit concurrency with a semaphore using a buffered channel
	sem := make(chan struct{}, maxParallel)

	for _, srv := range connectedServers {
		wg.Add(1)
		go func(serverID int64, addr string) {
			defer wg.Done()
			// Acquire semaphore slot
			select {
			case sem <- struct{}{}:
			case <-ctx2.Done():
				return
			}
			defer func() { <-sem }()

			// Check if context is still valid before starting WHOIS
			select {
			case <-ctx2.Done():
				return
			default:
			}

			if whoisBotOnServer(ctx2, ircMgr, serverID, bot) {
				// Bot found on this server — send result (non-blocking)
				select {
				case resultCh <- addr:
				default:
				}
			}
		}(srv.id, srv.addr)
	}

	// Wait for all goroutines to complete (they exit when context is cancelled or done)
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Return the first result received, or empty if context cancelled / no result
	select {
	case result, ok := <-resultCh:
		if ok && result != "" {
			// Cancel all other inflight goroutines — we already found the bot.
			cancel()
			return result
		}
		return ""
	case <-ctx2.Done():
		return ""
	}
}

// whoisBotOnServer does a WHOIS for `bot` on a specific server and waits for
// the response. Returns true if the bot was found (WHOIS returned a response).
func whoisBotOnServer(ctx context.Context, ircMgr IRCManager, serverID int64, bot string) bool {
	client := ircMgr.GetClient(serverID)
	if client == nil {
		return false
	}

	// Use a channel to receive the WHOIS result, with a timeout.
	resultCh := make(chan string, 1)

	// sync.Once ensures exactly one callback sends the result, eliminating
	// the data race that a bare bool would have when RPL_WHOISUSER and
	// ERR_NOSUCHNICK fire concurrently (girc dispatches events in separate
	// goroutines).
	var replyOnce sync.Once
	sendReply := func(v string) {
		replyOnce.Do(func() {
			select {
			case resultCh <- v:
			default:
			}
		})
	}

	// Handler for RPL_WHOISUSER — bot exists on this server
	// IRC format: :server 311 ourNick targetNick user host * :realname
	// girc includes the recipient (ourNick) as Params[0]; the bot is Params[1].
	cuid := client.Handlers.Add(girc.RPL_WHOISUSER, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 2 {
			return
		}
		whoisNick := e.Params[1]
		if strings.EqualFold(whoisNick, bot) {
			sendReply(whoisNick)
		}
	})
	defer client.Handlers.Remove(cuid)

	// Handler for ERR_NOSUCHNICK — bot not found on this server (no such nick)
	// IRC format: :server 401 ourNick targetNick :No such nick/channel
	// girc includes the recipient (ourNick) as Params[0]; the target is Params[1].
	cuid2 := client.Handlers.Add(girc.ERR_NOSUCHNICK, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 2 {
			return
		}
		if strings.EqualFold(e.Params[1], bot) {
			sendReply("")
		}
	})
	defer client.Handlers.Remove(cuid2)

	// Send WHOIS
	client.Cmd.Whois(bot)

	// Wait for reply or timeout
	select {
	case nick := <-resultCh:
		return nick != ""
	case <-ctx.Done():
		return false
	case <-time.After(5 * time.Second):
		return false
	}
}
