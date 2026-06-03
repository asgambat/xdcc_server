package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"xdcc-go/internal/store"
)

// =========================================================================
// GET /api/servers
// =========================================================================

// serverResponse extends ServerRecord with derived fields used by the UI.
type serverResponse struct {
	store.ServerRecord
	ChannelCount  int   `json:"channel_count"`
	UptimeSeconds int64 `json:"uptime_seconds"`
}

func (a *API) handleListServers(w http.ResponseWriter, r *http.Request) {
	var servers []store.ServerRecord

	// If IRC manager is available, use it for live status overlay
	if a.IRCManager != nil {
		servers = a.IRCManager.GetServers()
	} else {
		// Fallback: get from store directly
		var err error
		servers, err = a.Store.ListServers(r.Context())
		if err != nil {
			a.logAndError(w, http.StatusInternalServerError, "LIST_SERVERS_ERROR", err.Error())
			return
		}
	}

	// Enrich each server with its live joined channel count and uptime so the UI
	// dashboard and server list display accurate numbers.
	result := make([]serverResponse, len(servers))
	for i, s := range servers {
		joinedCount := 0
		var chs []store.ChannelRecord
		if a.IRCManager != nil {
			chs = a.IRCManager.GetChannels(s.ID)
		} else {
			chs, _ = a.Store.GetChannelsByServer(r.Context(), s.ID)
		}
		for _, ch := range chs {
			if ch.Joined {
				joinedCount++
			}
		}

		var uptimeSeconds int64
		// Only show uptime for currently connected servers; disconnected servers show 0.
		if s.Status == "connected" && s.LastConnectedAt != nil {
			uptimeSeconds = int64(time.Since(*s.LastConnectedAt).Seconds())
		}

		result[i] = serverResponse{
			ServerRecord:  servers[i],
			ChannelCount:  joinedCount,
			UptimeSeconds: uptimeSeconds,
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// =========================================================================
// POST /api/servers
// =========================================================================

func (a *API) handleConnectServer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID          int64  `json:"id"`
		Address     string `json:"address"`
		Port        int    `json:"port"`
		AutoConnect bool   `json:"auto_connect"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}

	// Reconnect to an existing server by ID: fetch it from the store and
	// tell the IRC manager to dial it again. The server card stays in the UI.
	if body.ID > 0 {
		srv, err := a.Store.GetServer(r.Context(), body.ID)
		if err != nil {
			a.logAndError(w, http.StatusInternalServerError, "GET_SERVER_ERROR", err.Error())
			return
		}
		if srv == nil {
			writeError(w, http.StatusNotFound, "SERVER_NOT_FOUND", fmt.Sprintf("server %d not found", body.ID))
			return
		}

		if a.IRCManager != nil {
			if err := a.IRCManager.ConnectServerByID(body.ID); err != nil {
				a.Logger.Warnf("reconnecting to server %d (%s) failed: %v", body.ID, srv.Address, err)
			}
		}

		writeJSON(w, http.StatusOK, map[string]int64{"id": body.ID})
		return
	}

	if body.Address == "" {
		writeError(w, http.StatusBadRequest, "MISSING_ADDRESS", "server address is required")
		return
	}
	if body.Port < 1 || body.Port > 65535 {
		body.Port = 6667
	}

	// Add to store
	id, err := a.Store.AddServer(r.Context(), store.ServerRecord{
		Address:     body.Address,
		Port:        body.Port,
		AutoConnect: body.AutoConnect,
		Status:      "disconnected",
	})
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "ADD_SERVER_ERROR", err.Error())
		return
	}

	// Connect via IRC manager if available
	if a.IRCManager != nil {
		if err := a.IRCManager.ConnectServerByID(id); err != nil {
			a.Logger.Warnf("connecting to server %s failed: %v", body.Address, err)
		}
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

// =========================================================================
// DELETE /api/servers/:serverID
// =========================================================================

func (a *API) handleDisconnectServer(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "serverID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid server ID")
		return
	}

	if a.IRCManager != nil {
		if err := a.IRCManager.DisconnectServer(id); err != nil {
			a.logAndError(w, http.StatusInternalServerError, "DISCONNECT_ERROR", err.Error())
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// =========================================================================
// DELETE /api/servers/:serverID/remove
// =========================================================================

func (a *API) handleRemoveServer(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "serverID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid server ID")
		return
	}

	// Disconnect first if IRC manager is available
	if a.IRCManager != nil {
		// Best-effort disconnect; ignore errors since the server may already be
		// disconnected or the connection may have failed.
		_ = a.IRCManager.DisconnectServer(id)
	}

	// Delete from store (also removes associated channels via CASCADE)
	if err := a.Store.DeleteServer(r.Context(), id); err != nil {
		a.logAndError(w, http.StatusInternalServerError, "DELETE_SERVER_ERROR", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// =========================================================================
// GET /api/servers/:serverID/channels
// =========================================================================

func (a *API) handleListChannels(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "serverID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid server ID")
		return
	}

	// If IRC manager is available, use it for live status overlay
	if a.IRCManager != nil {
		channels := a.IRCManager.GetChannels(id)
		writeJSON(w, http.StatusOK, channels)
		return
	}

	// Fallback: get from store directly
	channels, err := a.Store.GetChannelsByServer(r.Context(), id)
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "LIST_CHANNELS_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, channels)
}

// =========================================================================
// POST /api/servers/:serverID/channels
// =========================================================================

func (a *API) handleJoinChannel(w http.ResponseWriter, r *http.Request) {
	serverID, err := parseID(chi.URLParam(r, "serverID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid server ID")
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_CHANNEL", "channel name is required")
		return
	}

	// Normalize: IRC channel names are case-insensitive (RFC 1459).
	// Lowercase immediately so DB stores the canonical form and no duplicate
	// entries are created when IRC events use different casing.
	body.Name = strings.ToLower(body.Name)

	chID, err := a.Store.AddChannel(r.Context(), store.ChannelRecord{
		ServerID: serverID,
		Name:     body.Name,
		AutoJoin: true,
		Joined:   false,
	})
	if err != nil {
		a.logAndError(w, http.StatusInternalServerError, "ADD_CHANNEL_ERROR", err.Error())
		return
	}

	if a.IRCManager != nil {
		if err := a.IRCManager.JoinChannel(serverID, body.Name); err != nil {
			// Clean up the channel record since the IRC join failed
			_ = a.Store.DeleteChannel(r.Context(), chID)
			a.logAndError(w, http.StatusInternalServerError, "JOIN_CHANNEL_ERROR",
				fmt.Sprintf("joining channel %s: %v", body.Name, err))
			return
		}
	}

	writeJSON(w, http.StatusCreated, map[string]int64{"id": chID})
}

// =========================================================================
// DELETE /api/servers/:serverID/channels/:channelName
// =========================================================================

func (a *API) handleLeaveChannel(w http.ResponseWriter, r *http.Request) {
	serverID, err := parseID(chi.URLParam(r, "serverID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid server ID")
		return
	}
	channelName, err := url.PathUnescape(chi.URLParam(r, "channelName"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CHANNEL", "Invalid channel name encoding")
		return
	}

	if a.IRCManager != nil {
		if err := a.IRCManager.LeaveChannel(serverID, channelName); err != nil {
			a.logAndError(w, http.StatusInternalServerError, "LEAVE_CHANNEL_ERROR",
				fmt.Sprintf("leaving channel %s: %v", channelName, err))
			return
		}
	}

	// Remove from store
	if ch, err := a.Store.GetChannelsByServerAndName(r.Context(), serverID, channelName); err == nil && ch != nil {
		_ = a.Store.DeleteChannel(r.Context(), ch.ID)
	}

	w.WriteHeader(http.StatusNoContent)
}

// =========================================================================
// GET /api/servers/:serverID/channels/:channelName/topic
// =========================================================================

func (a *API) handleGetChannelTopic(w http.ResponseWriter, r *http.Request) {
	serverID, err := parseID(chi.URLParam(r, "serverID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid server ID")
		return
	}
	channelName, err := url.PathUnescape(chi.URLParam(r, "channelName"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CHANNEL", "Invalid channel name encoding")
		return
	}

	if a.IRCManager == nil {
		writeError(w, http.StatusServiceUnavailable, "IRC_UNAVAILABLE", "IRC manager not available")
		return
	}

	topic, err := a.IRCManager.GetChannelTopic(serverID, channelName)
	if err != nil {
		writeError(w, http.StatusNotFound, "CHANNEL_NOT_FOUND", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"topic": topic})
}

// =========================================================================
// PATCH /api/servers/:serverID/channels/:channelName
// =========================================================================

func (a *API) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	serverID, err := parseID(chi.URLParam(r, "serverID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid server ID")
		return
	}
	channelName, err := url.PathUnescape(chi.URLParam(r, "channelName"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CHANNEL", "Invalid channel name encoding")
		return
	}

	var body struct {
		AutoJoin *bool `json:"auto_join"`
	}
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body")
		return
	}

	// Get channel from store
	ch, err := a.Store.GetChannelsByServerAndName(r.Context(), serverID, channelName)
	if err != nil || ch == nil {
		writeError(w, http.StatusNotFound, "CHANNEL_NOT_FOUND", "Channel not found")
		return
	}

	// Update auto_join if provided
	if body.AutoJoin != nil {
		ch.AutoJoin = *body.AutoJoin
		if err := a.Store.UpdateChannel(r.Context(), *ch); err != nil {
			a.logAndError(w, http.StatusInternalServerError, "UPDATE_ERROR", err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
