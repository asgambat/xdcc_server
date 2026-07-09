// ============================================================
// XDCC Download Manager — API Client
// ============================================================

import { getAdminToken, notifyAuthFailure } from './stores.js';

const API_BASE = '/api';

// ---- REST Client ----
export const api = {
  async request(method, path, body = null, /** @type {{ timeoutMs?: number, adminToken?: string, contentType?: string, rawBody?: boolean, rawResponse?: boolean }} */ { timeoutMs = 30000, adminToken, contentType, rawBody, rawResponse } = {}) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);

    const headers = { 'Content-Type': contentType || 'application/json' };
    // Inject admin token if provided (for protected SYSTEM section endpoints)
    if (adminToken) {
      headers['X-Admin-Token'] = adminToken;
    }

    const opts = {
      method,
      headers,
      signal: controller.signal,
    };
    if (body !== null) {
      opts.body = rawBody ? body : JSON.stringify(body);
    }
    try {
      const res = await fetch(`${API_BASE}${path}`, opts);
      clearTimeout(timer);
      if (!res.ok) {
        // On 401, notify auth failure so the UI can prompt for re-authentication
        if (res.status === 401) {
          notifyAuthFailure();
        }
        const err = await res.json().catch(() => ({ error: { message: res.statusText } }));
        throw new Error(err.error?.message || `HTTP ${res.status}`);
      }
      if (res.status === 204) return null;
      // For raw text responses (e.g. config YAML), return text instead of JSON.
      if (rawResponse) return res.text();
      return res.json();
    } catch (e) {
      clearTimeout(timer);
      if (e.name === 'AbortError') {
        throw new Error(`Request timeout after ${timeoutMs / 1000}s`);
      }
      throw e;
    }
  },
  get(path, opts)       { return this.request('GET', path, null, opts); },
  post(path, b, opts)   { return this.request('POST', path, b, opts); },
  put(path, b, opts)    { return this.request('PUT', path, b, opts); },
  patch(path, b, opts)  { return this.request('PATCH', path, b, opts); },
  del(path, opts)       { return this.request('DELETE', path, null, opts); },
};

// ---- Server API ----
export const ServersAPI = {
  list()          { return api.get('/servers'); },
  connect(idOrData) {
    if (typeof idOrData === 'number' || typeof idOrData === 'string') {
      return api.post('/servers', { id: idOrData });
    }
    return api.post('/servers', idOrData);
  },
  disconnect(id)  { return api.del(`/servers/${id}`); },
  remove(id)      { return api.del(`/servers/${id}/remove`); },
  listChannels(id){ return api.get(`/servers/${id}/channels`); },
  joinChannel(sid, ch)  { return api.post(`/servers/${sid}/channels`, { name: ch }); },
  leaveChannel(sid, ch) { return api.del(`/servers/${sid}/channels/${encodeURIComponent(ch)}`); },
  topic(sid, ch)  { return api.get(`/servers/${sid}/channels/${encodeURIComponent(ch)}/topic`); },
  setChannelAutoJoin(sid, ch, autoJoin) { return api.patch(`/servers/${sid}/channels/${encodeURIComponent(ch)}`, { auto_join: autoJoin }); },
  /** Send a message (PRIVMSG) to a channel. Multi-line messages are accepted. */
  sendMessage(sid, ch, message) { return api.post(`/servers/${sid}/channels/${encodeURIComponent(ch)}/messages`, { message }); },
};

// ---- Download API ----
export const DownloadsAPI = {
  list()          { return api.get('/downloads'); },
  history(page, pageSize, filters = {}) {
    const q = new URLSearchParams();
    q.set('page', String(page || 1));
    q.set('pageSize', String(pageSize || 50));
    if (filters.filename) q.set('filename', filters.filename);
    if (filters.bot) q.set('bot', filters.bot);
    if (filters.min_bytes) q.set('min_bytes', String(filters.min_bytes));
    if (filters.max_bytes) q.set('max_bytes', String(filters.max_bytes));
    if (filters.date_from) q.set('date_from', filters.date_from);
    if (filters.date_to) q.set('date_to', filters.date_to);
    if (filters.status_list?.length) {
      for (const st of filters.status_list) q.append('status', st);
    }
    return api.get(`/downloads/history?${q.toString()}`);
  },
  enqueue(d)      { return api.post('/downloads', d); },
  get(id)         { return api.get(`/downloads/${id}`); },
  remove(id)      { return api.del(`/downloads/${id}`); },
  pause(id)       { return api.post(`/downloads/${id}/pause`); },
  resume(id)      { return api.post(`/downloads/${id}/resume`); },
  retry(id)       { return api.post(`/downloads/${id}/retry`); },
  position(id, p) { return api.patch(`/downloads/${id}/position`, { priority: p }); },
  bulk(ids, action) { return api.post('/downloads/bulk', { ids, action }); },
  deleteAllHistory() { return api.del('/downloads/history', { adminToken: getAdminToken() }); },
};

// ---- Search API ----
export const SearchAPI = {
  search(params) {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== null && v !== '') {
        if (Array.isArray(v)) v.forEach(x => q.append(k, x));
        else q.set(k, v);
      }
    }
    return api.get(`/search?${q.toString()}`);
  },
  parse(msg) { return api.post('/xdcc/parse', { command: msg }); },
};

// ---- Preset API ----
export const PresetsAPI = {
  list()          { return api.get('/search/presets'); },
  create(p)       { return api.post('/search/presets', p); },
  update(id, p)   { return api.put(`/search/presets/${id}`, p); },
  remove(id)      { return api.del(`/search/presets/${id}`); },
};

// ---- Watchlist API ----
export const WatchlistsAPI = {
  list()          { return api.get('/watchlists'); },
  create(w)       { return api.post('/watchlists', w); },
  update(id, w)   { return api.put(`/watchlists/${id}`, w); },
  remove(id)      { return api.del(`/watchlists/${id}`); },
  run(id)         { return api.post(`/watchlists/${id}/run`); },
};

// ---- Provider API ----
export const ProvidersAPI = {
  list()          { return api.get('/search/providers'); },
  toggle(name, enabled) { return api.patch(`/search/providers/${name}`, { enabled }, { adminToken: getAdminToken() }); },
};

// ---- System API (mixed: public + protected endpoints) ----
export const SystemAPI = {
  // ── Protected endpoints (require admin token) ──
  config()        { return api.get('/config', { adminToken: getAdminToken() }); },
  /** Update only the theme — does not validate the full config. */
  updateTheme(theme) { return api.patch('/config/theme', { theme }, { adminToken: getAdminToken() }); },
  /** Fetch raw config.yaml content for the YAML editor. */
  rawConfig()     { return api.request('GET', '/config?format=yaml', null, { adminToken: getAdminToken(), rawResponse: true }); },
  /** Save config from the YAML editor — sends raw YAML body. */
  updateRawConfig(yamlText) { return api.request('PUT', '/config', yamlText, { adminToken: getAdminToken(), contentType: 'text/yaml', rawBody: true }); },
  /** Save config from the structured Form — sends JSON body (backward compat). */
  updateConfig(c) { return api.put('/config', c, { adminToken: getAdminToken() }); },
  exportData()    { return api.post('/admin/export', null, { adminToken: getAdminToken() }); },
  importData(d)   { return api.post('/admin/import', d, { adminToken: getAdminToken() }); },
  setupStatus()   { return api.get('/setup/status', { adminToken: getAdminToken() }); },
  bootstrap(c)    { return api.post('/setup/bootstrap', c, { adminToken: getAdminToken() }); },
  logs(count)     { return api.get(`/logs?count=${count || 100}`, { adminToken: getAdminToken() }); },
  // ── Public endpoints ──
  stats()         { return api.get('/stats'); },
  status()        { return api.get('/status'); },
  version()       { return api.get('/version'); },
  health()        { return api.get('/healthz'); },
  ready()         { return api.get('/readyz'); },
};

// ---- SSE Client ----
export class SSEClient {
  constructor() {
    this.eventSource = null;
    this.lastEventId = 0;
    this.listeners = {};
    this.connected = false;
    this.onStatusChange = null;
    
    // Exponential backoff for reconnection (Fase 1: SSE stability fix)
    this.reconnectDelay = 1000; // Start at 1s
    this.maxReconnectDelay = 30000; // Max 30s
    this.reconnectAttempts = 0;
    this.reconnectTimer = null;

    // Track last event time to detect live connections even if onopen fails
    this.lastEventTime = 0;
    this._heartbeatTimer = null;
  }

  connect() {
    // Clear any pending reconnect timer
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }

    if (this.eventSource) this.eventSource.close();

    const url = `${API_BASE}/events`;
    this.eventSource = new EventSource(url);

    this.eventSource.addEventListener('connected', (e) => {
      try {
        const data = JSON.parse(e.data);
        this.lastEventId = data.server_id || 0;
      } catch {}
      this.connected = true;
      this.reconnectDelay = 1000;
      this.reconnectAttempts = 0;
      this._updateStatus('connected');
      console.log('[SSE] ✅ connected event received');
    });

    this.eventSource.onopen = () => {
      this.connected = true;
      this.reconnectDelay = 1000;
      this.reconnectAttempts = 0;
      this._updateStatus('connected');
      console.log('[SSE] ✅ onopen fired');
    };

    this.eventSource.onerror = () => {
      console.log('[SSE] ⚠️ onerror fired, readyState=' +
        (this.eventSource ? this.eventSource.readyState : 'null'));

      if (this.eventSource && this.eventSource.readyState !== EventSource.CLOSED) {
        this.connected = false;
        this._updateStatus('reconnecting');
        return;
      }

      this.connected = false;
      this._updateStatus('reconnecting');

      if (this.eventSource) {
        this.eventSource.close();
        this.eventSource = null;
      }

      this.reconnectAttempts++;
      const delay = Math.min(
        this.reconnectDelay * Math.pow(2, this.reconnectAttempts - 1),
        this.maxReconnectDelay
      );

      console.log(`[SSE] 🔄 Connection lost. Reconnecting in ${delay / 1000}s (attempt ${this.reconnectAttempts})`);

      this.reconnectTimer = setTimeout(() => {
        console.log(`[SSE] 🔄 Attempting reconnection (attempt ${this.reconnectAttempts})`);
        this.connect();
      }, delay);
    };

    const eventTypes = [
      'keepalive',
      'server_connected', 'server_disconnected', 'server_reconnecting',
      'channel_joined', 'channel_left', 'channel_topic_updated',
      'download_queued', 'download_started', 'download_progress',
      'download_completed', 'download_skipped', 'download_failed',
      'download_paused', 'download_removed', 'download_bulk_action_result',
      'download_alternative_found',
      'disk_space_low', 'disk_space_ok',
      'watchlist_new_results',
      'provider_health_changed',
      'log_entry',
      'resync_required',
    ];

    for (const type of eventTypes) {
      this.eventSource.addEventListener(type, (e) => {
        // Any received event proves the connection is alive.
        // Update heartbeat and force status to 'connected' if it wasn't.
        this.lastEventTime = Date.now();
        if (!this.connected) {
          this.connected = true;
          this._updateStatus('connected');
        }
        try {
          const data = JSON.parse(e.data);
          if (e.lastEventId) {
            data._eventId = parseInt(e.lastEventId);
            this.lastEventId = parseInt(e.lastEventId);
          }
          this._dispatch(type, data);
        } catch (err) {
          console.warn('SSE parse error:', err);
        }
      });
    }

    // Start heartbeat: if no event received for 30s, mark as disconnected
    this._startHeartbeat();
  }

  disconnect() {
    this._stopHeartbeat();
    // Clear any pending reconnect timer
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    
    if (this.eventSource) {
      this.eventSource.close();
      this.eventSource = null;
    }
    
    this.connected = false;
    this.reconnectAttempts = 0;
    this.reconnectDelay = 1000;
    this._updateStatus('disconnected');
    console.log('[SSE] Disconnected');
  }

  on(type, callback) {
    if (!this.listeners[type]) this.listeners[type] = [];
    this.listeners[type].push(callback);
    return () => {
      this.listeners[type] = this.listeners[type].filter(cb => cb !== callback);
    };
  }

  _dispatch(type, data) {
    const handlers = this.listeners[type] || [];
    for (const cb of handlers) {
      try { cb(data); } catch (e) { console.error('SSE handler error:', e); }
    }
    const wildcard = this.listeners['*'] || [];
    for (const cb of wildcard) {
      try { cb(type, data); } catch (e) { console.error('SSE wildcard error:', e); }
    }
  }

  _updateStatus(status) {
    if (this._prevStatus !== status) {
      console.log(`[SSE] 📡 status ${this._prevStatus || 'initial'} → ${status}`);
      this._prevStatus = status;
    }
    if (this.onStatusChange) this.onStatusChange(status);
  }

  _startHeartbeat() {
    if (this._heartbeatTimer) clearInterval(this._heartbeatTimer);
    this._heartbeatTimer = setInterval(() => {
      if (!this.connected) return;
      const elapsed = Date.now() - this.lastEventTime;
      if (elapsed > 30000) {
        console.log('[SSE] 💔 No events for 30s, marking disconnected');
        this.connected = false;
        this._updateStatus('disconnected');
      }
    }, 10000);
  }

  _stopHeartbeat() {
    if (this._heartbeatTimer) {
      clearInterval(this._heartbeatTimer);
      this._heartbeatTimer = null;
    }
  }

  isConnected() { return this.connected; }
}

// Singleton SSE client
export const sseClient = new SSEClient();
