package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"xdcc_server/internal/entities"
	"xdcc_server/internal/store"
)

// =========================================================================
// GET /api/downloads — list active + queued
// =========================================================================

func (a *API) handleListDownloads(w http.ResponseWriter, r *http.Request) {
	queue, err := a.Store.GetQueue(r.Context())
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "LIST_QUEUE_ERROR", err.Error())
		return
	}
	if queue == nil {
		queue = []store.DownloadRecord{}
	}

	var activeIDs []int64
	if a.QueueManager != nil {
		activeIDs = a.QueueManager.GetActiveIDs()
	}
	if activeIDs == nil {
		activeIDs = []int64{}
	}

	// Include recently completed/failed/skipped downloads so the UI can show
	// "Completed Today" counters and recent history without a separate fetch.
	recent, _, err := a.Store.GetDownloadHistory(r.Context(), 1, 50, store.HistoryFilter{})
	if err != nil {
		// Graceful degradation: log the warning but continue with the queue only.
		// The frontend can still show active downloads even if history fails.
		a.Logger.Warnf("failed to get recent download history: %v", err)
	} else {
		seen := make(map[int64]bool, len(queue))
		for _, d := range queue {
			seen[d.ID] = true
		}
		for _, d := range recent {
			if !seen[d.ID] {
				queue = append(queue, d)
				seen[d.ID] = true
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"downloads":  queue,
		"active_ids": activeIDs,
		"count":      len(activeIDs),
	})
}

// =========================================================================
// POST /api/downloads — enqueue
// =========================================================================

func (a *API) handleEnqueueDownload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PackMessage   string `json:"pack_message"`
		Bot           string `json:"bot"`
		ServerAddress string `json:"server_address"`
		Channel       string `json:"channel"`
		Filename      string `json:"filename"`
		FileSize      int64  `json:"file_size"`
		Priority      int    `json:"priority,omitempty"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}
	if body.PackMessage == "" {
		writeError(w, http.StatusBadRequest, "MISSING_PACK_MESSAGE", "pack_message is required")
		return
	}
	if body.Bot == "" {
		writeError(w, http.StatusBadRequest, "MISSING_BOT", "bot is required")
		return
	}
	if body.ServerAddress == "" {
		writeError(w, http.StatusBadRequest, "MISSING_SERVER", "server_address is required")
		return
	}
	// Channel is optional - if not provided, WHOIS will discover it

	// Apply bot-prefix → server mapping so TLT/WeC bots always use the
	// correct server regardless of what the search engine returned.
	resolved := entities.ResolveServer(body.Bot, body.ServerAddress)
	body.ServerAddress = resolved.Address

	// Sanitize filename at API boundary to prevent path traversal
	safeFilename := filepath.Base(body.Filename)
	if safeFilename == "." || safeFilename == "" {
		safeFilename = ""
	}

	rec := store.DownloadRecord{
		PackMessage:   body.PackMessage,
		Bot:           body.Bot,
		ServerAddress: body.ServerAddress,
		Channel:       body.Channel,
		Filename:      safeFilename,
		FileSize:      body.FileSize,
	}
	if body.Priority > 0 {
		rec.Priority = body.Priority
	} else {
		rec.Priority = 100
	}

	var id int64
	var err error

	if a.QueueManager != nil {
		id, err = a.QueueManager.Enqueue(rec)
		if err != nil {
			errMsg := err.Error()
			if strings.Contains(errMsg, "duplicate") {
				writeError(w, http.StatusConflict, "DUPLICATE_DOWNLOAD", errMsg)
			} else {
				a.logAndError(w, http.StatusInternalServerError, "ENQUEUE_ERROR", errMsg)
			}
			return
		}
	} else {
		// No queue manager — store directly
		id, err = a.Store.EnqueueDownload(r.Context(), rec)
		if err != nil {
			a.logAndError(w, http.StatusInternalServerError, "ENQUEUE_ERROR", err.Error())
			return
		}
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

// =========================================================================
// GET /api/downloads/history
// =========================================================================

func (a *API) handleDownloadHistory(w http.ResponseWriter, r *http.Request) {
	page, pageSize := parsePageParams(r)

	var filter store.HistoryFilter
	q := r.URL.Query()
	filter.Filename = q.Get("filename")
	filter.Bot = q.Get("bot")
	if q.Get("min_bytes") != "" {
		filter.MinBytes, _ = parseInt64(q.Get("min_bytes"))
	}
	if q.Get("max_bytes") != "" {
		filter.MaxBytes, _ = parseInt64(q.Get("max_bytes"))
	}
	filter.DateFrom = q.Get("date_from")
	filter.DateTo = q.Get("date_to")
	if statuses := q["status"]; len(statuses) > 0 {
		filter.StatusList = statuses
	}

	downloads, total, err := a.Store.GetDownloadHistory(r.Context(), page, pageSize, filter)
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "HISTORY_ERROR", err.Error())
		return
	}
	if downloads == nil {
		downloads = []store.DownloadRecord{}
	}

	totalPages := (total + pageSize - 1) / pageSize

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"downloads":   downloads,
		"total":       total,
		"page":        page,
		"page_size":   pageSize,
		"total_pages": totalPages,
	})
}

// =========================================================================
// POST /api/downloads/bulk
// =========================================================================

func (a *API) handleBulkDownloads(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs    []int64 `json:"ids"`
		Action string  `json:"action"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}
	if len(body.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_IDS", "ids array is required")
		return
	}

	validActions := map[string]bool{"pause": true, "resume": true, "remove": true}
	if !validActions[body.Action] {
		writeError(w, http.StatusBadRequest, "INVALID_ACTION",
			fmt.Sprintf("action must be one of: pause, resume, remove (got %q)", body.Action))
		return
	}

	var results map[int64]string
	var err error

	if a.QueueManager != nil {
		results, err = a.QueueManager.BulkAction(body.IDs, body.Action)
	} else {
		results, err = a.Store.BulkActionDownloads(r.Context(), body.IDs, body.Action)
	}
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "BULK_ERROR", err.Error())
		return
	}

	successes := 0
	failures := 0
	for _, r := range results {
		if r == "success" {
			successes++
		} else {
			failures++
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results":   results,
		"successes": successes,
		"failures":  failures,
	})
}

// =========================================================================
// GET /api/downloads/:downloadID
// =========================================================================

func (a *API) handleGetDownload(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "downloadID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid download ID")
		return
	}

	d, err := a.Store.GetDownload(r.Context(), id)
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "GET_DOWNLOAD_ERROR", err.Error())
		return
	}
	if d == nil {
		writeError(w, http.StatusNotFound, "DOWNLOAD_NOT_FOUND", fmt.Sprintf("Download %d not found", id))
		return
	}

	writeJSON(w, http.StatusOK, d)
}

// =========================================================================
// DELETE /api/downloads/:downloadID
// =========================================================================

func (a *API) handleRemoveDownload(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "downloadID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid download ID")
		return
	}

	if a.QueueManager != nil {
		if err := a.QueueManager.RemoveDownload(id); err != nil {
			a.logAndError(w, http.StatusInternalServerError, "REMOVE_ERROR", err.Error())
			return
		}
	} else {
		if err := a.Store.DeleteDownload(r.Context(), id); err != nil {
			a.logAndError(w, http.StatusInternalServerError, "REMOVE_ERROR", err.Error())
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// =========================================================================
// POST /api/downloads/:downloadID/pause
// =========================================================================

func (a *API) handlePauseDownload(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "downloadID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid download ID")
		return
	}

	if a.QueueManager != nil {
		if err := a.QueueManager.PauseDownload(id); err != nil {
			a.logAndError(w, http.StatusInternalServerError, "PAUSE_ERROR", err.Error())
			return
		}
	} else {
		if err := a.Store.MarkDownloadPaused(r.Context(), id); err != nil {
			a.logAndError(w, http.StatusInternalServerError, "PAUSE_ERROR", err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// =========================================================================
// POST /api/downloads/:downloadID/resume
// =========================================================================

func (a *API) handleResumeDownload(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "downloadID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid download ID")
		return
	}

	if a.QueueManager != nil {
		if err := a.QueueManager.ResumeDownload(id); err != nil {
			a.logAndError(w, http.StatusInternalServerError, "RESUME_ERROR", err.Error())
			return
		}
	} else {
		if err := a.Store.RetryDownload(r.Context(), id); err != nil {
			a.logAndError(w, http.StatusInternalServerError, "RESUME_ERROR", err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
}

// =========================================================================
// POST /api/downloads/:downloadID/retry
// =========================================================================

func (a *API) handleRetryDownload(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "downloadID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid download ID")
		return
	}

	if err := a.Store.RetryDownload(r.Context(), id); err != nil {
		a.logAndError(w, http.StatusInternalServerError, "RETRY_ERROR", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
}

// =========================================================================
// PATCH /api/downloads/:downloadID/position
// =========================================================================

func (a *API) handleSetDownloadPosition(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "downloadID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid download ID")
		return
	}

	var body struct {
		Priority int `json:"priority"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}
	if body.Priority < 1 {
		body.Priority = 100
	}

	// Verify the download exists
	d, err := a.Store.GetDownload(r.Context(), id)
	if err != nil || d == nil {
		writeError(w, http.StatusNotFound, "DOWNLOAD_NOT_FOUND", fmt.Sprintf("Download %d not found", id))
		return
	}

	if err := a.Store.SetDownloadPriority(r.Context(), id, body.Priority); err != nil {
		a.logAndError(w, http.StatusInternalServerError, "SET_PRIORITY_ERROR", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
