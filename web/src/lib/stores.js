import { writable, derived } from 'svelte/store';

export const currentView = writable('dashboard');
export const servers = writable([]);
export const channels = writable({});
export const downloads = writable([]);
export const searchResults = writable(null);
export const presets = writable([]);
export const watchlists = writable([]);
export const providers = writable([]);
export const config = writable(null);
export const stats = writable(null);
export const status = writable(null);
export const version = writable(null);
export const selectedDownloads = writable(new Set());
export const sseStatus = writable('disconnected');
export const theme = writable(localStorage.getItem('xdcc-theme') || 'dark');
export const toasts = writable([]);
export const pendingSearchQuery = writable('');

// Views that require admin authentication
export const SYSTEM_VIEWS = new Set(['logs', 'providers', 'settings']);

// ── Admin token (for SYSTEM section endpoints) ──────────────────────
const TOKEN_STORAGE_KEY = 'xdcc-admin-token';

/**
 * Sets the admin token with a configurable TTL.
 * @param {string} token
 * @param {number} ttlMinutes - default 15
 */
export function setAdminToken(token, ttlMinutes = 15) {
  const expires = Date.now() + ttlMinutes * 60 * 1000;
  localStorage.setItem(TOKEN_STORAGE_KEY, JSON.stringify({ token, expires }));
}

/**
 * Retrieves the stored admin token, if still valid.
 * @returns {string|null}
 */
export function getAdminToken() {
  try {
    const raw = localStorage.getItem(TOKEN_STORAGE_KEY);
    if (!raw) return null;
    const { token, expires } = JSON.parse(raw);
    if (expires > Date.now()) return token;
    // Expired — clean up
    localStorage.removeItem(TOKEN_STORAGE_KEY);
  } catch {}
  return null;
}

/**
 * Clears the stored admin token.
 */
export function clearAdminToken() {
  localStorage.removeItem(TOKEN_STORAGE_KEY);
}

// Reactive flag for whether a valid token is currently available.
export const hasAdminToken = writable(!!getAdminToken());

// Periodic check: sync hasAdminToken with actual token validity
// so the UI reflects expiry without waiting for a failed API call.
let _tokenExpiryTimer = null;
export function startTokenExpiryCheck() {
  if (_tokenExpiryTimer) return;
  _tokenExpiryTimer = setInterval(() => {
    const hasToken = !!getAdminToken();
    hasAdminToken.update(current => {
      if (current !== hasToken) return hasToken;
      return current;
    });
  }, 5000);
}
export function stopTokenExpiryCheck() {
  if (_tokenExpiryTimer) { clearInterval(_tokenExpiryTimer); _tokenExpiryTimer = null; }
}

// ── 401 callback: api.js calls this when a protected endpoint returns 401
//    so App.svelte can show the token modal automatically.
let _onAuthFailure = null;
export function onAuthFailure(cb) { _onAuthFailure = cb; }
export function notifyAuthFailure() {
  hasAdminToken.set(false);
  if (_onAuthFailure) _onAuthFailure();
}

export function navigateTo(view) {
  currentView.set(view);
  window.location.hash = view;
}

/**
 * Navigate to the Search page with query and optional size filters.
 * Uses hash params so the URL is shareable/bookmarkable.
 */
export function navigateToSearch(query, minSize, maxSize) {
  currentView.set('search');
  window.location.hash = `search?q=${encodeURIComponent(query)}${minSize ? `&min=${encodeURIComponent(minSize)}` : ''}${maxSize ? `&max=${encodeURIComponent(maxSize)}` : ''}`;
}

export const activeDownloads = derived(downloads, $dls =>
  $dls.filter(d => d.status === 'downloading')
);

export const queuedDownloads = derived(downloads, $dls =>
  $dls.filter(d => d.status === 'queued')
);

export const pausedDownloads = derived(downloads, $dls =>
  $dls.filter(d => d.status === 'paused')
);

export const completedDownloads = derived(downloads, $dls =>
  $dls.filter(d => ['completed', 'failed', 'skipped_existing'].includes(d.status))
);

export const downloadsBadge = derived([activeDownloads, queuedDownloads], ([$a, $q]) =>
  $a.length + $q.length
);

export function addToast(message, type = 'info') {
  const id = Date.now() + Math.random();
  toasts.update(t => [...t, { id, message, type }]);
  setTimeout(() => {
    toasts.update(t => t.filter(x => x.id !== id));
  }, 3000);
}
