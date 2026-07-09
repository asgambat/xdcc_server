<script>
  import { onMount } from 'svelte';
  import { downloads, selectedDownloads, navigateTo } from '../lib/stores.js';
  import { DownloadsAPI, ServersAPI } from '../lib/api.js';
  import { formatBytes, formatSpeed, formatETA, statusBadge, debounce } from '../lib/utils.js';
  import { addToast } from '../lib/stores.js';
  import DownloadTable from './DownloadTable.svelte';
  import Modal from './Modal.svelte';

  let loading = $state(true);
  let activeTab = $state('active'); // 'active' | 'history'

  // Manual download modal state
  let showManualModal = $state(false);
  let manualServers = $state([]);
  let manualLoading = $state(false);
  // Form fields
  let manualServerId = $state('');
  let manualChannel = $state('');
  let manualBotname = $state('');
  let manualPackNumber = $state('');

  let historyData = $state({ downloads: [], total: 0, page: 1, pageSize: 20 });
  let historyLoading = $state(false);

  // History filters
  let historyFilterName = $state('');
  let historyFilterBot = $state('');
  let historyFilterMinMB = $state('');
  let historyFilterMaxMB = $state('');
  let historyFilterDateFrom = $state('');
  let historyFilterDateTo = $state('');
  let historyFilterStatus = $state(['completed', 'failed', 'skipped_existing']);

  // History selection
  let selectedHistoryIDs = $state(new Set());

  // Delete all confirmation
  let showDeleteAllConfirm = $state(false);

  // Debounced history filter loader (300ms)
  const debouncedLoadHistory = debounce(() => loadHistoryPage(1), 300);

  // Recent bots for manual download modal (extracted from download history)
  let recentBots = $state([]);

  let active = $derived($downloads.filter(d => d.status === 'downloading'));
  let queued = $derived($downloads.filter(d => d.status === 'queued'));
  let paused = $derived($downloads.filter(d => d.status === 'paused'));
  let completed = $derived($downloads.filter(d => ['completed', 'failed', 'skipped_existing'].includes(d.status)));

  onMount(async () => { await refresh(); loading = false; });

  async function refresh() {
    try {
      const dls = await DownloadsAPI.list();
      downloads.set(dls?.downloads || dls || []);
    } catch (e) { console.warn(e); }
  }

  async function loadHistoryPage(page) {
    historyLoading = true;
    try {
      const minMB = parseInt(historyFilterMinMB, 10);
      const maxMB = parseInt(historyFilterMaxMB, 10);
      const minBytes = !isNaN(minMB) && minMB > 0 ? minMB * 1024 * 1024 : 0;
      const maxBytes = !isNaN(maxMB) && maxMB > 0 ? maxMB * 1024 * 1024 : 0;
      const filters = {
        filename: historyFilterName.trim(),
        bot: historyFilterBot.trim(),
        min_bytes: minBytes,
        max_bytes: maxBytes,
        date_from: historyFilterDateFrom,
        date_to: historyFilterDateTo,
        status_list: historyFilterStatus,
      };
      const res = await DownloadsAPI.history(page, historyData.pageSize, filters);
      historyData = {
        downloads: res?.downloads || [],
        total: res?.total || 0,
        page: res?.page || page,
        pageSize: historyData.pageSize
      };
      // Clear selection when page changes to avoid stale selections
      selectedHistoryIDs = new Set();
    } catch (e) { addToast(e.message, 'error'); }
    historyLoading = false;
  }

  function switchTab(tab) {
    activeTab = tab;
    if (tab === 'history') {
      loadHistoryPage(1);
    }
  }

  function totalPages() {
    return Math.max(1, Math.ceil(historyData.total / historyData.pageSize));
  }

  // --- History selection helpers ---
  function toggleHistorySelection(id) {
    selectedHistoryIDs = new Set(selectedHistoryIDs);
    if (selectedHistoryIDs.has(id)) {
      selectedHistoryIDs.delete(id);
    } else {
      selectedHistoryIDs.add(id);
    }
    selectedHistoryIDs = selectedHistoryIDs;
  }

  function toggleSelectAllHistory(e) {
    if (e.target.checked) {
      selectedHistoryIDs = new Set(historyData.downloads.map(d => d.id));
    } else {
      selectedHistoryIDs = new Set();
    }
  }

  function isAllHistorySelected() {
    return historyData.downloads.length > 0 && historyData.downloads.every(d => selectedHistoryIDs.has(d.id));
  }

  function clearHistoryFilters() {
    historyFilterName = '';
    historyFilterBot = '';
    historyFilterMinMB = '';
    historyFilterMaxMB = '';
    historyFilterDateFrom = '';
    historyFilterDateTo = '';
    historyFilterStatus = ['completed', 'failed', 'skipped_existing'];
    loadHistoryPage(1);
  }

  async function bulkDeleteHistory() {
    const ids = Array.from(selectedHistoryIDs);
    if (!ids.length) return addToast('No history items selected', 'warning');
    if (!window.confirm(`Delete ${ids.length} selected history items? This cannot be undone.`)) return;
    try {
      const result = await DownloadsAPI.bulk(ids, 'remove');
      const success = Object.values(result || {}).filter(v => v === 'success').length;
      addToast(`Deleted ${success}/${ids.length} items`, 'success');
      selectedHistoryIDs = new Set();
      await loadHistoryPage(historyData.page);
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function deleteAllHistory() {
    showDeleteAllConfirm = false;
    try {
      const resp = await DownloadsAPI.deleteAllHistory();
      addToast(`Deleted ${resp.deleted || 0} history items`, 'success');
      await loadHistoryPage(1);
    } catch (e) { addToast(e.message, 'error'); }
  }

  function toggleDownload(id) {
    selectedDownloads.update(s => {
      const next = new Set(s);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  }

  function toggleSelectAll(e) {
    if (e.target.checked) {
      selectedDownloads.set(new Set($downloads.filter(d => !['completed', 'failed', 'skipped_existing'].includes(d.status)).map(d => d.id)));
    } else {
      selectedDownloads.set(new Set());
    }
  }

  async function bulkAction(action) {
    const ids = Array.from($selectedDownloads);
    if (!ids.length) return addToast('No downloads selected', 'warning');
    try {
      const result = await DownloadsAPI.bulk(ids, action);
      const success = Object.values(result || {}).filter(v => v === 'success').length;
      addToast(`${action}: ${success}/${ids.length} succeeded`, 'success');
      selectedDownloads.set(new Set());
      await refresh();
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function pauseDownload(id) { try { await DownloadsAPI.pause(id); addToast('Paused', 'info'); await refresh(); } catch (e) { addToast(e.message, 'error'); } }
  async function resumeDownload(id) { try { await DownloadsAPI.resume(id); addToast('Resumed', 'success'); await refresh(); } catch (e) { addToast(e.message, 'error'); } }
  async function retryDownload(id) { try { await DownloadsAPI.retry(id); addToast('Retrying', 'info'); await refresh(); if (activeTab === 'history') await loadHistoryPage(historyData.page); } catch (e) { addToast(e.message, 'error'); } }
  async function removeDownload(id) { try { await DownloadsAPI.remove(id); addToast('Removed', 'info'); await refresh(); if (activeTab === 'history') await loadHistoryPage(historyData.page); } catch (e) { addToast(e.message, 'error'); } }

  async function pauseAll() {
    const ids = $downloads.filter(d => d.status === 'downloading' || d.status === 'queued').map(d => d.id);
    if (ids.length === 0) return addToast('No downloads to pause', 'warning');
    try {
      await DownloadsAPI.bulk(ids, 'pause');
      addToast(`Paused ${ids.length} downloads`, 'success');
      await refresh();
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function resumeAll() {
    const ids = $downloads.filter(d => d.status === 'paused').map(d => d.id);
    if (ids.length === 0) return addToast('No paused downloads to resume', 'warning');
    try {
      await DownloadsAPI.bulk(ids, 'resume');
      addToast(`Resumed ${ids.length} downloads`, 'success');
      await refresh();
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function moveUp(id) {
    const idx = $downloads.findIndex(d => d.id === id);
    if (idx <= 0) return;
    const prev = $downloads[idx - 1];
    try { await DownloadsAPI.position(id, prev.priority + 1); addToast('Moved up', 'success'); await refresh(); }
    catch (e) { addToast(e.message, 'error'); }
  }
  async function moveDown(id) {
    const idx = $downloads.findIndex(d => d.id === id);
    if (idx < 0 || idx >= $downloads.length - 1) return;
    const next = $downloads[idx + 1];
    try { await DownloadsAPI.position(id, Math.max(1, next.priority - 1)); addToast('Moved down', 'success'); await refresh(); }
    catch (e) { addToast(e.message, 'error'); }
  }

  // ---- Manual Download Modal ----
  async function openManualModal() {
    showManualModal = true;
    manualChannel = '';
    manualBotname = '';
    manualPackNumber = '';
    manualServerId = '';
    manualLoading = true;
    try {
      const [servers, hist] = await Promise.all([
        ServersAPI.list(),
        DownloadsAPI.history(1, 100),
      ]);
      // Only show connected servers
      manualServers = (servers || []).filter(s => s.status === 'connected');
      // Extract unique bot names from history, most recent first
      const seen = new Set();
      const bots = [];
      const histList = hist?.downloads || [];
      for (const d of histList) {
        if (d.bot && !seen.has(d.bot)) {
          seen.add(d.bot);
          bots.push(d.bot);
        }
      }
      recentBots = bots.slice(0, 8); // keep at most 8 recent bots
    } catch (e) { addToast(e.message, 'error'); }
    manualLoading = false;
  }

  function closeManualModal() {
    showManualModal = false;
  }

  async function submitManualDownload() {
    if (!manualServerId) return addToast('Select a server', 'warning');
    if (!manualBotname.trim()) return addToast('Bot name is required', 'warning');
    const packNum = parseInt(manualPackNumber, 10);
    if (isNaN(packNum) || packNum <= 0) return addToast('Pack number must be a positive number', 'warning');

    const packMessage = `xdcc send #${packNum}`;
    const server = manualServers.find(s => s.id === manualServerId);
    if (!server) return addToast('Selected server not found', 'error');

    try {
      await DownloadsAPI.enqueue({
        pack_message:   packMessage,
        bot:            manualBotname.trim(),
        server_address: server.address,
        channel:        manualChannel.trim(),
        filename:       '',
        file_size:      0,
      });
      addToast('Download enqueued', 'success');
      closeManualModal();
      await refresh();
    } catch (e) { addToast(e.message, 'error'); }
  }
</script>

{#if loading}
  <div class="spinner"></div>
{:else}
  <div class="tab-bar mb-2" style="display:flex; gap:0.5rem; border-bottom:1px solid var(--border-color); padding-bottom:0.5rem;">
    <button class="btn btn-sm" class:btn-primary={activeTab === 'active'} onclick={() => switchTab('active')}>Active</button>
    <button class="btn btn-sm" class:btn-primary={activeTab === 'history'} onclick={() => switchTab('history')}>History</button>
    <div style="flex:1"></div>
    <button class="btn btn-sm btn-primary" onclick={openManualModal}>+ Manual Download</button>
  </div>

  {#if activeTab === 'active'}
    {#if $selectedDownloads.size > 0}
      <div class="flex gap-1 mb-2" style="align-items:center">
        <span class="text-sm">{$selectedDownloads.size} selected</span>
        <button class="btn btn-sm btn-warning" onclick={() => bulkAction('pause')}>Pause</button>
        <button class="btn btn-sm btn-success" onclick={() => bulkAction('resume')}>Resume</button>
        <button class="btn btn-sm btn-danger" onclick={() => bulkAction('remove')}>Remove</button>
        <button class="btn btn-sm btn-ghost" onclick={() => selectedDownloads.set(new Set())}>Clear</button>
      </div>
    {/if}

    {#if active.length + queued.length > 0 || paused.length > 0}
      <div class="flex gap-1 mb-2" style="align-items:center">
        {#if active.length + queued.length > 0}
          <button class="btn btn-sm btn-warning" onclick={pauseAll}>⏸️ Pause All</button>
        {/if}
        {#if paused.length > 0}
          <button class="btn btn-sm btn-success" onclick={resumeAll}>▶️ Resume All</button>
        {/if}
      </div>
    {/if}

    {#if active.length > 0}
      <div class="card mb-2">
        <div class="card-header"><span class="card-title">⬇️ Downloading ({active.length})</span></div>
        <DownloadTable items={active} selectedDownloads={$selectedDownloads} {toggleDownload} {toggleSelectAll}
          {formatBytes} {formatSpeed} {formatETA} {statusBadge}
          onPause={pauseDownload} onResume={resumeDownload} onRetry={retryDownload} onRemove={removeDownload} onMoveUp={moveUp} onMoveDown={moveDown} />
      </div>
    {/if}

    {#if paused.length > 0}
      <div class="card mb-2">
        <div class="card-header"><span class="card-title">⏸️ Paused ({paused.length})</span></div>
        <DownloadTable items={paused} selectedDownloads={$selectedDownloads} {toggleDownload} {toggleSelectAll}
          {formatBytes} {formatSpeed} {formatETA} {statusBadge}
          onPause={pauseDownload} onResume={resumeDownload} onRetry={retryDownload} onRemove={removeDownload} onMoveUp={moveUp} onMoveDown={moveDown} />
      </div>
    {/if}

    {#if queued.length > 0}
      <div class="card mb-2">
        <div class="card-header"><span class="card-title">📋 Queued ({queued.length})</span></div>
        <DownloadTable items={queued} selectedDownloads={$selectedDownloads} {toggleDownload} {toggleSelectAll}
          {formatBytes} {formatSpeed} {formatETA} {statusBadge}
          onPause={pauseDownload} onResume={resumeDownload} onRetry={retryDownload} onRemove={removeDownload} onMoveUp={moveUp} onMoveDown={moveDown} />
      </div>
    {/if}

    {#if active.length === 0 && queued.length === 0 && paused.length === 0}
      <div class="empty-state">
        <div class="empty-state-icon">🔍</div>
        <div class="empty-state-text">Avvia una ricerca per trovare qualcosa da scaricare</div>
        <button class="btn btn-primary mt-2" onclick={() => navigateTo('search')}>Search</button>
      </div>
    {/if}
  {:else}
    <!-- History Tab -->
    <div class="card">
      <div class="card-header" style="display:flex; align-items:center; gap:0.75rem; flex-wrap:wrap;">
        <span class="card-title">📜 Download History ({historyData.total})</span>
        <button class="btn btn-sm btn-danger" style="margin-left:auto" onclick={() => showDeleteAllConfirm = true} title="Delete all history">🗑️ Delete All</button>
        {#if selectedHistoryIDs.size > 0}
          <div class="flex gap-1" style="align-items:center; margin-left:auto;">
            <span class="text-sm">{selectedHistoryIDs.size} selected</span>
            <button class="btn btn-sm btn-danger" onclick={bulkDeleteHistory}>🗑️ Delete</button>
            <button class="btn btn-sm btn-ghost" onclick={() => selectedHistoryIDs = new Set()}>Clear</button>
          </div>
        {/if}
      </div>

      <!-- Filter bar -->
      <div class="history-filter-bar">
        <div class="history-filter-inputs">
          <input class="form-input" bind:value={historyFilterName} placeholder="Filter by name..." oninput={debouncedLoadHistory} />
          <input class="form-input" bind:value={historyFilterBot} placeholder="Filter by bot..." oninput={debouncedLoadHistory} />
          <input class="form-input" type="number" min="0" bind:value={historyFilterMinMB} placeholder="Min MB" oninput={debouncedLoadHistory} />
          <input class="form-input" type="number" min="0" bind:value={historyFilterMaxMB} placeholder="Max MB" oninput={debouncedLoadHistory} />
          <input class="form-input" type="date" bind:value={historyFilterDateFrom} onchange={() => loadHistoryPage(1)} />
          <input class="form-input" type="date" bind:value={historyFilterDateTo} onchange={() => loadHistoryPage(1)} />
          <div class="status-toggles">
            {#each ['completed', 'failed', 'skipped_existing'] as st}
              <button class="btn btn-xs" class:btn-primary={historyFilterStatus.includes(st)} class:btn-ghost={!historyFilterStatus.includes(st)} onclick={() => {
                if (historyFilterStatus.includes(st)) {
                  historyFilterStatus = historyFilterStatus.filter(s => s !== st);
                } else {
                  historyFilterStatus = [...historyFilterStatus, st];
                }
                loadHistoryPage(1);
              }}>
                {st}
              </button>
            {/each}
          </div>
        </div>
        {#if historyFilterName || historyFilterBot || historyFilterMinMB || historyFilterMaxMB || historyFilterDateFrom || historyFilterDateTo || historyFilterStatus.length !== 3}
          <button class="btn btn-sm btn-ghost" onclick={clearHistoryFilters}>✕ Clear filters</button>
        {/if}
      </div>

      {#if historyLoading}
        <div class="spinner" style="margin:1rem"></div>
      {:else if historyData.downloads.length > 0}
        <div class="table-container">
          <table>
            <thead>
              <tr>
                <th class="checkbox-cell">
                  <input type="checkbox" onchange={toggleSelectAllHistory} checked={isAllHistorySelected()} />
                </th>
                <th>File</th>
                <th>Bot</th>
                <th>Status</th>
                <th>Size</th>
                <th>Date</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {#each historyData.downloads as d (d.id)}
                <tr>
                  <td class="checkbox-cell">
                    <input type="checkbox" checked={selectedHistoryIDs.has(d.id)} onchange={() => toggleHistorySelection(d.id)} />
                  </td>
                  <td class="truncate" style="max-width:200px" title={d.filename}>{d.filename || 'Unknown'}</td>
                  <td>{d.bot || '—'}</td>
                  <td><span class="badge badge-{statusBadge(d.status).cls}"><span class="badge-dot"></span>{d.status}</span></td>
                  <td class="text-sm">{formatBytes(d.file_size)}</td>
                  <td class="text-sm">{new Date(d.completed_at || d.created_at).toLocaleDateString()}</td>
                  <td>
                    <div class="btn-group">
                      <button class="btn btn-sm btn-primary" onclick={() => retryDownload(d.id)} title="Retry">🔄</button>
                      <button class="btn btn-sm btn-ghost" onclick={() => removeDownload(d.id)} title="Remove">🗑️</button>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        {#if totalPages() > 1}
          <div class="flex gap-1 mt-2" style="justify-content:center; align-items:center;">
            <button class="btn btn-sm btn-ghost" disabled={historyData.page <= 1} onclick={() => loadHistoryPage(historyData.page - 1)}>← Prev</button>
            <span class="text-sm">Page {historyData.page} of {totalPages()}</span>
            <button class="btn btn-sm btn-ghost" disabled={historyData.page >= totalPages()} onclick={() => loadHistoryPage(historyData.page + 1)}>Next →</button>
          </div>
        {/if}
      {:else}
        <div class="empty-state">
          <div class="empty-state-text">No history found</div>
          <div class="empty-state-sub">
            {#if historyFilterName || historyFilterBot || historyFilterMinMB || historyFilterMaxMB || historyFilterDateFrom || historyFilterDateTo || historyFilterStatus.length !== 3}
              <button class="btn btn-sm btn-ghost" onclick={clearHistoryFilters}>✕ Clear filters to see results</button>
            {:else}
              No downloads completed yet
            {/if}
          </div>
        </div>
      {/if}
    </div>
  {/if}
{/if}

<!-- Delete All History Confirmation Modal -->
<Modal title="🗑️ Delete All History" visible={showDeleteAllConfirm} on:close={() => showDeleteAllConfirm = false}>
  <div style="display:flex; flex-direction:column; gap:1rem;">
    <p class="text-sm" style="margin:0; line-height:1.5;">
      Are you sure you want to delete <strong>all</strong> download history?
      This will permanently remove all completed, failed, and skipped downloads.
      Active downloads (queued, downloading, paused) will not be affected.
    </p>
    <p class="text-sm" style="margin:0; color:var(--text-error);">
      This action cannot be undone.
    </p>
  </div>
  <div class="modal-actions">
    <button class="btn btn-ghost" onclick={() => showDeleteAllConfirm = false}>Cancel</button>
    <button class="btn btn-danger" onclick={deleteAllHistory}>🗑️ Delete All</button>
  </div>
</Modal>

<!-- Manual Download Modal -->
<Modal title="📥 Manual XDCC Download" visible={showManualModal} on:close={closeManualModal}>
  <div style="display:flex; flex-direction:column; gap:1rem;">
    <!-- Server -->
    <div class="form-group">
      <label class="form-label" for="manual-server">Server <span style="color:var(--text-error)">*</span></label>
      {#if manualLoading}
        <div class="text-sm text-muted">Loading servers...</div>
      {:else if manualServers.length === 0}
        <div class="text-sm text-muted">No connected servers. Connect to a server first.</div>
      {:else}
        <select class="form-input" id="manual-server" bind:value={manualServerId}>
          <option value="">— Select a server —</option>
          {#each manualServers as srv}
            <option value={srv.id}>{srv.address}:{srv.port || 6667}</option>
          {/each}
        </select>
      {/if}
    </div>

    <!-- Channel (optional) -->
    <div class="form-group">
      <label class="form-label" for="manual-channel">Channel <span style="color:var(--text-muted); font-weight:normal">(optional)</span></label>
      <input class="form-input" id="manual-channel" bind:value={manualChannel} placeholder="#channel (leave empty to auto-discover via WHOIS)" />
    </div>

    <!-- Bot name -->
    <div class="form-group">
      <label class="form-label" for="manual-bot">Bot Name <span style="color:var(--text-error)">*</span></label>
      <input class="form-input" id="manual-bot" bind:value={manualBotname} placeholder="BotName" />
      {#if recentBots.length > 0}
        <div style="display:flex; flex-wrap:wrap; gap:0.4rem; margin-top:0.4rem;">
          <span class="text-sm text-muted">Recent:</span>
          {#each recentBots as bot}
            <button class="bot-chip" onclick={() => { manualBotname = bot; }}>{bot}</button>
          {/each}
        </div>
      {/if}
    </div>

    <!-- Pack number -->
    <div class="form-group">
      <label class="form-label" for="manual-pack">Pack Number <span style="color:var(--text-error)">*</span></label>
      <input class="form-input" id="manual-pack" type="number" min="1" bind:value={manualPackNumber} placeholder="123" />
    </div>
  </div>

  <div class="modal-actions">
    <button class="btn btn-ghost" onclick={closeManualModal}>Cancel</button>
    <button class="btn btn-primary" onclick={submitManualDownload} disabled={!manualServerId || !manualBotname.trim() || !manualPackNumber}>ADD</button>
  </div>
</Modal>

<style>
  .bot-chip {
    background: var(--bg-tertiary);
    border: 1px solid var(--border-color);
    border-radius: var(--radius);
    padding: 0.2rem 0.6rem;
    font-size: 0.75rem;
    cursor: pointer;
    color: var(--text-secondary);
    transition: background 0.15s, color 0.15s, border-color 0.15s;
  }
  .bot-chip:hover {
    background: var(--bg-hover);
    color: var(--text-primary);
    border-color: var(--text-muted);
  }

  .history-filter-bar {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
    background: var(--bg-secondary);
    border-bottom: 1px solid var(--border-color);
    flex-wrap: wrap;
  }
  .history-filter-inputs {
    display: flex;
    gap: 0.5rem;
    flex: 1;
    flex-wrap: wrap;
    align-items: center;
  }
  .history-filter-inputs .form-input {
    width: 160px;
    padding: 0.4rem 0.6rem;
    font-size: 0.82rem;
    height: 34px;
  }
  .history-filter-inputs input[type="date"] {
    width: 150px;
  }
  .history-filter-inputs input[type="number"] {
    width: 100px;
  }
  .status-toggles {
    display: flex;
    gap: 0.3rem;
    align-items: center;
  }
  .status-toggles .btn-xs {
    padding: 0.2rem 0.5rem;
    font-size: 0.72rem;
    border-radius: var(--radius-sm);
    text-transform: capitalize;
  }
  .checkbox-cell {
    width: 40px;
    text-align: center;
  }
  .checkbox-cell input[type="checkbox"] {
    accent-color: var(--accent);
    width: 16px;
    height: 16px;
    cursor: pointer;
  }
</style>
