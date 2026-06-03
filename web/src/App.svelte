<script>
  import { onMount, onDestroy, tick } from 'svelte';
  import { currentView, toasts, sseStatus, stats, status, config, downloads, servers, hasAdminToken, setAdminToken, getAdminToken, onAuthFailure, startTokenExpiryCheck, stopTokenExpiryCheck, theme, SYSTEM_VIEWS } from './lib/stores.js';
  import { sseClient, SystemAPI, DownloadsAPI, ServersAPI } from './lib/api.js';
  import { debounce, escapeHtml } from './lib/utils.js';
  import Sidebar from './components/Sidebar.svelte';
  import ConnectionStatus from './components/ConnectionStatus.svelte';
  import Toast from './components/Toast.svelte';
  import Modal from './components/Modal.svelte';
  import Dashboard from './components/Dashboard.svelte';
  import Servers from './components/Servers.svelte';
  import Downloads from './components/Downloads.svelte';
  import Search from './components/Search.svelte';
  import Presets from './components/Presets.svelte';
  import Watchlists from './components/Watchlists.svelte';
  import Providers from './components/Providers.svelte';
  import Settings from './components/Settings.svelte';
  import Logs from './components/Logs.svelte';

  let sidebarOpen = $state(false);

  // ── Admin token prompt state ──
  let showTokenModal = $state(false);
  let tokenInput = $state('');
  let tokenInputEl = $state(null); // ref for autofocus
  let tokenError = $state('');
  let pendingSystemView = $state('');
  let tokenTTLMinutes = $state(15);


  // Suppress auth modal during initial page mount (the initial data fetch
  // may return 401 — we don't want to interrupt the user before they even
  // try to access a protected view).
  let _authModalSuppressed = true;

  function toggleSidebar() { sidebarOpen = !sidebarOpen; }

  function navigateTo(view) {
    currentView.set(view);
    window.location.hash = view;
    sidebarOpen = false;
  }

  /** Handle requestToken event from sidebar — show token prompt modal */
  function onRequestToken(e) {
    pendingSystemView = e.detail;
    tokenInput = '';
    tokenError = '';
    showTokenModal = true;
  }

  /** Submit the admin token — validate via a test call, store on success */
  async function submitToken() {
    const token = tokenInput.trim();
    if (!token) {
      tokenError = 'Please enter a token';
      return;
    }
    // Validate token by making a test call to a protected endpoint
    try {
      const res = await fetch('/api/logs?count=1', {
        headers: { 'X-Admin-Token': token }
      });
      if (res.status === 401) {
        tokenError = 'Invalid token — check server logs for the admin token';
        return;
      }
      if (!res.ok) {
        tokenError = `Server error (${res.status})`;
        return;
      }
    } catch (e) {
      tokenError = `Connection error: ${e.message}`;
      return;
    }
    // Token is valid — store
    setAdminToken(token, tokenTTLMinutes);
    hasAdminToken.set(true);
    showTokenModal = false;

    const targetView = pendingSystemView || $currentView;
    pendingSystemView = '';

    if (targetView && SYSTEM_VIEWS.has(targetView)) {
      // Force remount of the child component so it retries data loading
      // with the fresh token (Settings → SystemAPI.config(), Logs → SystemAPI.logs(), etc.).
      currentView.set('dashboard');
      await tick();
      navigateTo(targetView);
    } else if (targetView) {
      navigateTo(targetView);
    }
  }

  function cancelToken() {
    showTokenModal = false;
    tokenInput = '';
    tokenError = '';
    pendingSystemView = '';
  }

  function getViewFromHash() {
    const hash = window.location.hash.replace('#', '') || 'dashboard';
    return hash.split('?')[0];
  }

  // ---- Browser Notifications (Fase 9.5) ----
  function showNotification(title, body, icon = '⚡') {
    if (!('Notification' in window)) return;
    if (Notification.permission === 'granted') {
      new Notification(`${icon} ${title}`, { body, icon: '/favicon.ico' });
    }
  }

  // ---- SSE Event -> Notification mapping ----
  let notificationHandlers = [];
  let statsInterval; // periodic stats refresh timer
  let newDownloadTimer = null; // timer for batching download_queued fetches (cleared in onDestroy)

  // Autofocus the token input when the modal appears
  $effect(() => {
    if (showTokenModal && tokenInputEl) {
      // Small delay to let the modal's slideUp animation start
      requestAnimationFrame(() => tokenInputEl.focus());
    }
  });

  onMount(async () => {
    // Show admin token modal whenever a protected API returns 401,
    // but NOT during the initial data fetch (avoid interrupting first paint).
    _authModalSuppressed = true;
    onAuthFailure(() => {
      if (_authModalSuppressed) return;
      showTokenModal = true;
      tokenError = 'Session expired — please re-authenticate';
    });

    // Periodically check if the cached token has expired
    startTokenExpiryCheck();

    // Register service worker for PWA
    if ('serviceWorker' in navigator) {
      navigator.serviceWorker.register('/sw.js')
        .then(reg => console.log('[App] Service worker registered:', reg))
        .catch(err => console.warn('[App] SW registration failed:', err));
    }

    // Request notification permission on first visit
    if ('Notification' in window && Notification.permission === 'default') {
      Notification.requestPermission();
    }

    // Load initial data
    try {
      const [statsData, statusData, cfg, dls, srvs] = await Promise.all([
        SystemAPI.stats().catch(() => null),
        SystemAPI.status().catch(() => null),
        SystemAPI.config().catch(() => null),
        DownloadsAPI.list().catch(() => []),
        ServersAPI.list().catch(() => []),
      ]);
      if (dls?.downloads || dls) downloads.set(dls?.downloads || dls);
      if (srvs) servers.set(srvs);
      if (statsData) stats.set(statsData);
      if (statusData) {
        status.set(statusData);
        // Extract token TTL from status response (public endpoint)
        if (statusData.token_ttl_minutes) tokenTTLMinutes = statusData.token_ttl_minutes;
        // Apply theme from /api/status (public endpoint — works without auth)
        if (statusData.ui_theme && (statusData.ui_theme === 'dark' || statusData.ui_theme === 'light')) {
          theme.set(statusData.ui_theme);
          localStorage.setItem('xdcc-theme', statusData.ui_theme);
          document.documentElement.setAttribute('data-theme', statusData.ui_theme);
        }
      }
      if (cfg) {
        config.set(cfg);
      }
    } catch (e) { console.warn('Initial data load:', e); }

    // Initial data fetch done — allow the auth modal for subsequent 401s
    _authModalSuppressed = false;

    // Apply theme from localStorage as fallback (covers offline / no status response)
    const savedTheme = localStorage.getItem('xdcc-theme') || 'dark';
    theme.set(savedTheme);
    document.documentElement.setAttribute('data-theme', savedTheme);

    // Initialize SSE
    sseClient.onStatusChange = (s) => { sseStatus.set(s); };
    sseClient.connect();

    // Handle resync
    sseClient.on('resync_required', async () => {
      try {
        const [dls, s, st] = await Promise.all([
          DownloadsAPI.list(),
          SystemAPI.stats(),
          SystemAPI.status(),
        ]);
        downloads.set(dls?.downloads || dls || []);
        stats.set(s);
        status.set(st);
      } catch {}
    });

    // ---- Register SSE notification handlers (Fase 9.5) ----
    const notifyMap = {
      'download_completed': (d) => showNotification('Download Complete', `${d.filename || 'File'} downloaded successfully`, '✅'),
      'download_failed': (d) => showNotification('Download Failed', `${escapeHtml(d.filename) || 'File'}: ${escapeHtml(d.error_message) || 'Unknown error'}`, '❌'),
      'disk_space_low': () => showNotification('Low Disk Space', 'Download queue paused — running low on disk space', '⚠️'),
      'watchlist_new_results': (d) => showNotification('Watchlist: New Results', `New packs found for "${d.watchlist_name || 'watchlist'}"`, '🔔'),
    };

    for (const [eventType, handler] of Object.entries(notifyMap)) {
      const unsub = sseClient.on(eventType, handler);
      notificationHandlers.push(unsub);
    }

    // ---- SSE event -> downloads store updates ----
    // Most events carry full data in the SSE payload — we update the store
    // directly without REST calls, eliminating redundant API traffic.
    // Only complex multi-change events (bulk_action_result, alternative_found)
    // trigger a full list refresh.

    // Helper: update a single download in the store by ID (no REST call)
    function updateDownload(id, updates) {
      downloads.update($dls => $dls.map(d => d.id === id ? { ...d, ...updates } : d));
    }

    // Helper: remove a download from the store by ID (no REST call)
    function removeDownload(id) {
      downloads.update($dls => $dls.filter(d => d.id !== id));
    }

    // ── Direct store updates (SSE payload has all needed data) ──

    sseClient.on('download_progress', (d) => {
      updateDownload(d.download_id, {
        progress_bytes: d.progress_bytes,
        speed_bps: d.speed_bps,
        file_size: d.file_size || undefined  // DCC total bytes (may be more accurate)
      });
    });

    sseClient.on('download_started', (d) => {
      // If the download isn't in the store yet (e.g., download_queued's
      // targeted fetch failed), fall back to a single-download REST fetch.
      let downloadFound = false;
      downloads.update($dls => {
        const exists = $dls.some(dl => dl.id === d.download_id);
        if (exists) {
          downloadFound = true;
          return $dls.map(dl => dl.id === d.download_id ? { ...dl, status: 'downloading' } : dl);
        }
        return $dls; // not in store — fallback: fetch it
      });
      if (!downloadFound) {
        DownloadsAPI.get(d.download_id).then(dl => {
          if (dl) {
            downloads.update($dls => {
              const idx = $dls.findIndex(d => d.id === dl.id);
              if (idx >= 0) return [...$dls.slice(0, idx), { ...$dls[idx], ...dl, status: 'downloading' }, ...$dls.slice(idx + 1)];
              return [...$dls, { ...dl, status: 'downloading' }];
            });
          }
        }).catch(() => {});
      }
    });

    sseClient.on('download_completed', (d) => {
      removeDownload(d.download_id); // completed → gone from active list
    });

    sseClient.on('download_failed', (d) => {
      removeDownload(d.download_id); // failed → gone from active list
    });

    sseClient.on('download_skipped', (d) => {
      removeDownload(d.download_id); // skipped → gone from active list
    });

    sseClient.on('download_paused', (d) => {
      updateDownload(d.download_id, { status: 'paused' });
    });

    sseClient.on('download_removed', (d) => {
      removeDownload(d.download_id);
    });

    sseClient.on('download_metadata_update', (d) => {
      updateDownload(d.download_id, {
        filename: d.filename,
        file_size: d.file_size
      });
    });

    // ── Targeted single-download fetch (download_queued needs full record) ──
    // Debounced to batch rapid successive enqueues into a single fetch per ID.
    const pendingNewIds = new Set();
    newDownloadTimer = null;
    sseClient.on('download_queued', (d) => {
      pendingNewIds.add(d.download_id);
      if (newDownloadTimer) clearTimeout(newDownloadTimer);
      newDownloadTimer = setTimeout(async () => {
        const ids = [...pendingNewIds];
        pendingNewIds.clear();
        for (const id of ids) {
          try {
            const dl = await DownloadsAPI.get(id);
            if (dl) {
              downloads.update($dls => {
                const idx = $dls.findIndex(d => d.id === id);
                if (idx >= 0) {
                  const next = [...$dls];
                  next[idx] = { ...next[idx], ...dl };
                  return next;
                }
                return [...$dls, dl];
              });
            }
          } catch { /* transient — download will appear on next full refresh */ }
        }
      }, 300);
    });

    // ── Full REST refresh (complex multi-change events only) ──
    const refreshDownloads = debounce(async () => {
      try {
        const dls = await DownloadsAPI.list();
        downloads.set(dls?.downloads || dls || []);
      } catch {}
    }, 500);

    sseClient.on('download_bulk_action_result', refreshDownloads);
    sseClient.on('download_alternative_found', refreshDownloads);

    // ---- SSE event -> servers store: direct, immediate updates ----
    // These run in App.svelte (always mounted) so the store is updated
    // regardless of which view is active. Child components (Dashboard,
    // Servers) also listen for toast/connecting-state management.
    // Updates are done via servers.update() to avoid replacing the entire
    // array, which would race with child-component state.
    sseClient.on('server_connected', (data) => {
      const serverId = data.server_id;
      if (serverId) {
        servers.update(list => list.map(s =>
          s.id === serverId ? { ...s, status: 'connected' } : s
        ));
      }
    });

    sseClient.on('server_disconnected', (data) => {
      const serverId = data.server_id;
      if (serverId) {
        servers.update(list => list.map(s =>
          s.id === serverId ? { ...s, status: 'disconnected' } : s
        ));
      }
    });

    sseClient.on('server_reconnecting', (data) => {
      const serverId = data.server_id;
      if (serverId) {
        servers.update(list => list.map(s =>
          s.id === serverId ? { ...s, status: 'reconnecting' } : s
        ));
      }
    });

    // ---- SSE event -> servers/status debounced refresh (channels only) ----
    // Channel events (join/leave/topic) affect channel_count and channel
    // lists, so they need a REST fetch for consistency.  We merge instead
    // of replacing so that direct store updates (server status) applied by
    // the handlers above are never overwritten.
    // Debounce to prevent connection pool saturation.
    const refreshServers = debounce(async () => {
      try {
        const [serversData, statsData, statusData] = await Promise.all([
          ServersAPI.list().catch(() => null),
          SystemAPI.stats().catch(() => null),
          SystemAPI.status().catch(() => null),
        ]);
        if (serversData) {
          servers.update(current => {
            const restMap = new Map(serversData.map(s => [s.id, s]));
            // Preserve existing servers, overlay REST fields (channel_count etc.)
            const merged = current.map(s => {
              const rest = restMap.get(s.id);
              return rest ? { ...rest, status: s.status } : s;
            });
            // Add servers from REST not yet in the store (e.g. added by another client)
            const currentIds = new Set(current.map(cs => cs.id));
            for (const s of serversData) {
              if (!currentIds.has(s.id)) merged.push(s);
            }
            return merged;
          });
        }
        if (statsData) stats.set(statsData);
        if (statusData) status.set(statusData);
      } catch {}
    }, 1000); // 1s debounce

    const channelEvents = ['channel_joined', 'channel_left', 'channel_topic_updated'];
    for (const evt of channelEvents) {
      sseClient.on(evt, refreshServers);
    }

    // ---- Periodic stats refresh (uptime, disk, speed counters) ----
    // Stats are computed server-side and need periodic polling to stay fresh.
    const refreshStatsOnly = async () => {
      try {
        const [statsData, statusData] = await Promise.all([
          SystemAPI.stats().catch(() => null),
          SystemAPI.status().catch(() => null),
        ]);
        if (statsData) stats.set(statsData);
        if (statusData) status.set(statusData);
      } catch {}
    };
    statsInterval = setInterval(refreshStatsOnly, 30000); // every 30s

    // Setup hash routing
    const view = getViewFromHash();
    currentView.set(view);

    window.addEventListener('hashchange', () => {
      currentView.set(getViewFromHash());
    });

    // Keyboard shortcuts
    window.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') { /* modals handle their own Escape */ }
      if ((e.ctrlKey || e.metaKey) && e.key === 'k') {
        e.preventDefault();
        navigateTo('search');
      }
      if ((e.ctrlKey || e.metaKey) && e.key === 'l') {
        e.preventDefault();
        tokenInput = '';
        tokenError = '';
        pendingSystemView = '';
        showTokenModal = true;
      }
    });
  });

  onDestroy(() => {
    if (newDownloadTimer) clearTimeout(newDownloadTimer);
    sseClient.disconnect();
    notificationHandlers.forEach(fn => fn());
    clearInterval(statsInterval);
    stopTokenExpiryCheck();
  });
</script>

<div class="app-layout">
  <Sidebar {sidebarOpen} {toggleSidebar}
    on:navigate={(e) => navigateTo(e.detail)}
    on:requestToken={onRequestToken} />

  <div class="mobile-header">
    <button class="hamburger" onclick={toggleSidebar} aria-label="Open Menu">☰</button>
    <div class="header-title">XDCC Manager</div>
  </div>

  <main class="main-content">
    <div class="page-header">
      {#if $currentView === 'dashboard'}
        <h1 class="page-title">Dashboard</h1>
        <p class="page-subtitle">Server overview and download statistics</p>
      {:else if $currentView === 'servers'}
        <h1 class="page-title">Servers</h1>
        <p class="page-subtitle">Manage IRC server connections</p>
      {:else if $currentView === 'downloads'}
        <h1 class="page-title">Downloads</h1>
        <p class="page-subtitle">Manage download queue</p>
      {:else if $currentView === 'search'}
        <h1 class="page-title">Search</h1>
        <p class="page-subtitle">Find XDCC packs across providers</p>
      {:else if $currentView === 'presets'}
        <h1 class="page-title">Search Presets</h1>
        <p class="page-subtitle">Save and reuse search configurations</p>
      {:else if $currentView === 'watchlists'}
        <h1 class="page-title">Watchlists</h1>
        <p class="page-subtitle">Monitor searches for new results</p>
      {:else if $currentView === 'providers'}
        <h1 class="page-title">Search Providers</h1>
        <p class="page-subtitle">Monitor and manage search provider health</p>
      {:else if $currentView === 'settings'}
        <h1 class="page-title">Settings</h1>
        <p class="page-subtitle">Configure the XDCC server</p>
      {:else if $currentView === 'logs'}
        <h1 class="page-title">Logs</h1>
        <p class="page-subtitle">Real-time server log viewer</p>
      {/if}
    </div>

    {#if $currentView === 'dashboard'}
      <Dashboard />
    {:else if $currentView === 'servers'}
      <Servers on:navigate />
    {:else if $currentView === 'downloads'}
      <Downloads />
    {:else if $currentView === 'search'}
      <Search />
    {:else if $currentView === 'presets'}
      <Presets />
    {:else if $currentView === 'watchlists'}
      <Watchlists />
    {:else if $currentView === 'providers'}
      <Providers />
    {:else if $currentView === 'settings'}
      <Settings />
    {:else if $currentView === 'logs'}
      <Logs />
    {/if}
  </main>
</div>

<!-- Admin token prompt modal (shown when accessing SYSTEM section without token) -->
<Modal title="🔐 Admin Authentication Required" visible={showTokenModal} on:close={cancelToken}>
  <div class="token-prompt">
    <p class="text-sm text-muted mb-1">
      The System section requires an admin token for authentication.
      Find the token in the server startup logs or set it via <code>XDCC_SECURITY_ADMIN_TOKEN</code> environment variable.
    </p>
    <input
      type="password"
      class="form-input"
      placeholder="Paste admin token..."
      bind:value={tokenInput}
      bind:this={tokenInputEl}
      onkeydown={(e) => e.key === 'Enter' && submitToken()}
    />
    {#if tokenError}
      <p class="text-sm text-danger mt-1">{tokenError}</p>
    {/if}
    <p class="text-xs text-muted mt-1">
      Token is stored locally for {tokenTTLMinutes} minutes, then expires.
    </p>
    <div class="modal-actions">
      <button class="btn btn-sm btn-ghost" onclick={cancelToken}>Cancel</button>
      <button class="btn btn-sm btn-primary" onclick={submitToken}>Authenticate</button>
    </div>
  </div>
</Modal>

<ConnectionStatus />
<Toast />

<style>
  .app-layout { display: flex; width: 100%; min-height: 100vh; }
  .main-content {
    margin-left: var(--sidebar-width);
    flex: 1;
    padding: 1.5rem 2rem;
    max-width: calc(100vw - var(--sidebar-width));
  }
  .page-header { margin-bottom: 1.5rem; }
  .page-title { font-size: 1.5rem; font-weight: 700; margin-bottom: 0.25rem; }
  .page-subtitle { color: var(--text-secondary); font-size: 0.9rem; }
  
  /* Mobile adjustments */
  .mobile-header {
    display: none;
    align-items: center;
    padding: 0 1rem;
    background: var(--bg-secondary);
    border-bottom: 1px solid var(--border-color);
    position: sticky;
    top: 0;
    z-index: 99;
    height: 56px;
    box-shadow: 0 1px 3px rgba(0,0,0,0.1);
    width: 100%;
  }

  .header-title {
    font-size: 1.25rem;
    font-weight: 700;
    margin-left: 1rem;
    color: var(--text-primary);
  }
  .hamburger { 
    display: none; 
    background: none; 
    border: none; 
    color: var(--text-primary); 
    font-size: 1.5rem; 
    cursor: pointer; 
    padding: 0.5rem;
    border-radius: 8px;
  }
  .hamburger:hover { background: var(--bg-hover); }

  @media (max-width: 768px) {
    .app-layout { flex-direction: column; }
    .main-content { margin-left: 0; padding: 1rem; max-width: 100vw; }
    .mobile-header { display: flex; }
    .hamburger { display: block; }
  }
</style>
