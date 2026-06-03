<script>
  import { onMount, onDestroy } from 'svelte';
  import { stats, status, downloads, activeDownloads, servers, navigateTo } from '../lib/stores.js';
  import { ServersAPI, DownloadsAPI, sseClient } from '../lib/api.js';
  import { formatBytes, formatSpeed, formatUptime, statusBadge } from '../lib/utils.js';
  import { addToast } from '../lib/stores.js';

  let connectingServers = $state(new Set());
  // Timeout IDs for connect fallback (if SSE doesn't respond within 15s)
  let connectTimeouts = new Map();
  let unsubServerConnected, unsubServerDisconnected;

  onMount(() => {
    // Helper: clear a connect timeout and remove from connecting set
    function onConnectResolved(serverId) {
      if (connectTimeouts.has(serverId)) {
        clearTimeout(connectTimeouts.get(serverId));
        connectTimeouts.delete(serverId);
      }
      if (connectingServers.has(serverId)) {
        connectingServers = new Set([...connectingServers].filter(x => x !== serverId));
      }
    }

    // Server status store updates are handled in App.svelte (always mounted).
    // We only manage toast notifications and connecting state here.
    unsubServerConnected = sseClient.on('server_connected', (data) => {
      const serverId = data.server_id;
      if (serverId && connectingServers.has(serverId)) {
        onConnectResolved(serverId);
        const addr = data.server_addr || '';
        addToast(addr ? `Connected to ${addr}` : 'Server connected', 'success');
      }
    });

    unsubServerDisconnected = sseClient.on('server_disconnected', (data) => {
      const serverId = data.server_id;
      if (serverId && connectingServers.has(serverId)) {
        onConnectResolved(serverId);
        const addr = data.server_addr || '';
        addToast(addr ? `Connection to ${addr} failed` : 'Connection failed', 'error');
      }
    });
  });

  onDestroy(() => {
    // Clear all pending connect timeouts to prevent memory leaks
    for (const tid of connectTimeouts.values()) clearTimeout(tid);
    connectTimeouts.clear();
    if (unsubServerConnected) unsubServerConnected();
    if (unsubServerDisconnected) unsubServerDisconnected();
  });

  async function loadServers() {
    try {
      servers.set(await ServersAPI.list());
    } catch (e) {
      // Silently ignore; servers may not be loaded yet
    }
  }

  function scheduleConnectTimeout(serverId) {
    if (connectTimeouts.has(serverId)) {
      clearTimeout(connectTimeouts.get(serverId));
    }
    const tid = setTimeout(async () => {
      connectTimeouts.delete(serverId);
      if (connectingServers.has(serverId)) {
        connectingServers = new Set([...connectingServers].filter(x => x !== serverId));
        await loadServers();
      }
    }, 15000);
    connectTimeouts.set(serverId, tid);
  }

  async function connectServer(id) {
    connectingServers = new Set([...connectingServers, id]);
    scheduleConnectTimeout(id);
    try {
      await ServersAPI.connect(id);
      // Wait for SSE server_connected (success) or server_disconnected (failure)
      // before showing any toast or removing from the connecting set.
      // The scheduleConnectTimeout provides a 15s fallback if SSE doesn't respond.
    } catch (e) {
      if (connectTimeouts.has(id)) {
        clearTimeout(connectTimeouts.get(id));
        connectTimeouts.delete(id);
      }
      connectingServers = new Set([...connectingServers].filter(x => x !== id));
      addToast(e.message, 'error');
    }
  }

  async function disconnectServer(id) {
    // Clean up any pending connect timeout and remove from connecting set
    if (connectTimeouts.has(id)) {
      clearTimeout(connectTimeouts.get(id));
      connectTimeouts.delete(id);
    }
    connectingServers = new Set([...connectingServers].filter(x => x !== id));
    try {
      await ServersAPI.disconnect(id);
      addToast('Server disconnected', 'info');
      await loadServers();
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function stopAll() {
    if (!window.confirm('Stop everything? This will pause all downloads and disconnect from all servers.')) return;
    const dlIds = $downloads.filter(d => d.status === 'downloading' || d.status === 'queued').map(d => d.id);
    const svIds = $servers.filter(s => s.status === 'connected').map(s => s.id);

    // Clean up all pending connect timeouts and connecting state
    for (const tid of connectTimeouts.values()) clearTimeout(tid);
    connectTimeouts.clear();
    connectingServers = new Set();

    let messages = [];

    // Pause all downloads
    if (dlIds.length > 0) {
      try {
        await DownloadsAPI.bulk(dlIds, 'pause');
        messages.push(`Paused ${dlIds.length} downloads`);
      } catch (e) { messages.push(`Download pause failed: ${e.message}`); }
    }

    // Disconnect all servers
    for (const id of svIds) {
      try {
        await ServersAPI.disconnect(id);
      } catch (e) { /* best-effort */ }
    }
    if (svIds.length > 0) messages.push(`Disconnected ${svIds.length} servers`);

    if (messages.length > 0) {
      addToast(messages.join(' — '), 'success');
    } else {
      addToast('Nothing to stop', 'info');
    }

    await loadServers();
  }

  let s = $derived($stats || {});
  let st = $derived($status || {});
  let connectedCount = $derived($servers.filter(s => s.status === 'connected').length);
  let serverTotal = $derived($servers.length);
  let completedToday = $derived($downloads.filter(d =>
    d.status === 'completed' && d.completed_at &&
    new Date(d.completed_at) > new Date(Date.now() - 86400000)
  ).length);
</script>

<div style="display:flex; align-items:center; justify-content:flex-end; margin-bottom:1rem">
    {#if $downloads.filter(d => d.status === 'downloading' || d.status === 'queued').length > 0 || $servers.filter(s => s.status === 'connected').length > 0}
      <button class="btn btn-danger" onclick={stopAll}>🛑 Stop All</button>
    {/if}
  </div>

<div class="stats-grid">
  <div class="stat-card">
    <div class="stat-label">Servers Online</div>
    <div class="stat-value" class:success={connectedCount > 0} class:warning={connectedCount === 0}>
      {connectedCount}/{serverTotal}
    </div>
  </div>
  <div class="stat-card">
    <div class="stat-label">Active Downloads</div>
    <div class="stat-value info">{$activeDownloads.length}</div>
  </div>
  <div class="stat-card">
    <div class="stat-label">Queued</div>
    <div class="stat-value warning">{Math.max(0, $downloads.filter(d => d.status === 'queued').length)}</div>
  </div>
  <div class="stat-card">
    <div class="stat-label">Completed Today</div>
    <div class="stat-value success">{completedToday}</div>
  </div>
</div>

<div class="stats-grid">
  <div class="stat-card">
    <div class="stat-label">Total Downloaded</div>
    <div class="stat-value">{formatBytes(s.total_downloaded_bytes || 0)}</div>
  </div>
  <div class="stat-card">
    <div class="stat-label">Download Speed</div>
    <div class="stat-value">{formatSpeed(s.average_speed_bps || 0)}</div>
  </div>
  <div class="stat-card">
    <div class="stat-label">Server Uptime</div>
    <div class="stat-value">{formatUptime(s.uptime_seconds || st.uptime_seconds || 0)}</div>
  </div>
  <div class="stat-card">
    <div class="stat-label">Disk Free</div>
    <div class="stat-value">{formatBytes(s.disk_free_bytes || st.disk_free_bytes || 0)}</div>
  </div>
</div>

{#if $activeDownloads.length > 0}
  <div class="card mt-2">
    <div class="card-header">
      <span class="card-title">⬇️ Currently Downloading</span>
    </div>
    <div class="table-container">
      <table>
        <thead><tr><th>File</th><th>Bot</th><th>Progress</th><th>Speed</th><th>ETA</th></tr></thead>
        <tbody>
          {#each $activeDownloads as d}
            <tr>
              <td class="truncate" style="max-width:250px">{d.filename || 'Unknown'}</td>
              <td>{d.bot || '—'}</td>
              <td style="min-width:140px">
                <div class="text-sm" style="display:flex;justify-content:space-between">
                  <span>{formatBytes(d.progress_bytes)} / {formatBytes(d.file_size)}</span>
                  <span>{d.file_size > 0 ? Math.round((d.progress_bytes / d.file_size) * 100) : 0}%</span>
                </div>
                <div class="progress-bar">
                  <div class="progress-fill" style="width:{d.file_size > 0 ? Math.min(100, (d.progress_bytes / d.file_size) * 100) : 0}%"></div>
                </div>
              </td>
              <td class="text-sm">{formatSpeed(d.speed_bps)}</td>
              <td class="text-sm">{d.file_size ? formatBytes(d.file_size - d.progress_bytes) + ' left' : '—'}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>
{/if}

<div class="card mt-2">
  <div class="card-header">
    <span class="card-title">🖥️ Servers</span>
    <button class="btn btn-sm btn-primary" onclick={() => navigateTo('servers')}>Manage</button>
  </div>
  {#if $servers.length > 0}
    <div class="table-container">
      <table>
        <thead><tr><th>Server</th><th>Status</th><th>Channels</th><th>Uptime</th><th>Actions</th></tr></thead>
        <tbody>
          {#each $servers as srv}
            <tr>
              <td>{srv.address || srv.server_address}:{srv.port || 6667}</td>
              <td><span class="badge badge-{statusBadge(srv.status).cls}"><span class="badge-dot"></span>{srv.status}</span></td>
              <td>{srv.channel_count || 0}</td>
              <td>{formatUptime(srv.uptime_seconds || 0)}</td>
              <td>
                {#if connectingServers.has(srv.id)}
                  <button class="btn btn-sm btn-success" disabled>Connecting...</button>
                {:else if srv.status !== 'connected'}
                  <button class="btn btn-sm btn-success" onclick={() => connectServer(srv.id)}>Connect</button>
                {:else}
                  <button class="btn btn-sm btn-danger" onclick={() => disconnectServer(srv.id)}>Disconnect</button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else}
    <div class="empty-state"><div class="empty-state-text">No servers configured</div></div>
  {/if}
</div>
