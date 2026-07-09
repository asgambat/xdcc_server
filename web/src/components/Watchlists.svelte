<script>
  import { onMount } from 'svelte';
  import { watchlists, addToast, navigateToSearch } from '../lib/stores.js';
  import { WatchlistsAPI, DownloadsAPI } from '../lib/api.js';
  import { escapeHtml, createEditFormFocuser } from '../lib/utils.js';
  import Modal from './Modal.svelte';

  let loading = $state(true);
  let editingId = $state(null);
  let form = $state({ name: '', query: '', interval_minutes: 60, providers: [], min_size: '', max_size: '', notify_enabled: true, auto_enqueue: false, enabled: true });
  let formCard = $state(null);
  let queryInput = $state(null);
  let formHighlight = $state(false);

  // ---- Sort state ----
  let sortKey = $state('name');
  let sortAsc = $state(true);

  let sortedWatchlists = $derived.by(() => {
    const list = [...$watchlists];
    const dir = sortAsc ? 1 : -1;
    list.sort((a, b) => {
      let va, vb;
      switch (sortKey) {
        case 'name':
          va = (a.name || '').toLowerCase();
          vb = (b.name || '').toLowerCase();
          break;
        case 'query':
          va = (a.query || '').toLowerCase();
          vb = (b.query || '').toLowerCase();
          break;
        case 'interval':
          va = a.interval_minutes ?? 60;
          vb = b.interval_minutes ?? 60;
          break;
        case 'last_run':
          va = a.last_checked_at ? new Date(a.last_checked_at).getTime() : 0;
          vb = b.last_checked_at ? new Date(b.last_checked_at).getTime() : 0;
          break;
        case 'new':
          va = (a.last_results?.length || 0);
          vb = (b.last_results?.length || 0);
          break;
        default:
          return 0;
      }
      if (va < vb) return -1 * dir;
      if (va > vb) return 1 * dir;
      return 0;
    });
    return list;
  });

  function toggleSort(key) {
    if (sortKey === key) {
      sortAsc = !sortAsc;
    } else {
      sortKey = key;
      sortAsc = true;
    }
  }

  function sortArrow(key) {
    if (sortKey !== key) return '';
    return sortAsc ? ' ▲' : ' ▼';
  }

  const focusEditForm = createEditFormFocuser();

  // --- Results modal state ---
  let showResultsModal = $state(false);
  let resultsWatchlist = $state(null);
  let resultsItems = $state([]);
  let resultsFilterFilename = $state('');
  let resultsFilterBot = $state('');

  let filteredResults = $derived.by(() => {
    const ff = resultsFilterFilename.trim().toLowerCase();
    const bf = resultsFilterBot.trim().toLowerCase();
    if (!ff && !bf) return resultsItems;
    return resultsItems.filter(item => {
      const fn = (item.filename || '').toLowerCase();
      const bn = (item.bot || '').toLowerCase();
      return fn.includes(ff) && bn.includes(bf);
    });
  });

  onMount(async () => { await load(); loading = false; });

  async function load() {
    try { watchlists.set(await WatchlistsAPI.list()); } catch {}
  }

  function resetForm() {
    form = { name: '', query: '', interval_minutes: 60, providers: [], min_size: '', max_size: '', notify_enabled: true, auto_enqueue: false, enabled: true };
    editingId = null;
  }

  function startEdit(w) {
    editingId = w.id;
    // Scroll to the edit form at the top of the page, highlight it, and focus the query input
    focusEditForm(formCard, queryInput, (v) => { formHighlight = v; });
    form = {
      name: w.name || '',
      query: w.query || '',
      interval_minutes: w.interval_minutes || 60,
      providers: w.providers || [],
      min_size: w.min_size || '',
      max_size: w.max_size || '',
      notify_enabled: w.notify_enabled !== false,
      auto_enqueue: w.auto_enqueue || false,
      enabled: w.enabled !== false,
    };
  }

  async function save() {
    if (!form.name.trim() || !form.query.trim()) return addToast('Name and query required', 'warning');
    // @ts-ignore - svelte-check type inference conflict
    const payload = { ...form, name: form.name.trim(), query: form.query.trim(), interval_minutes: parseInt(form.interval_minutes) || 60 };
    try {
      if (editingId) {
        await WatchlistsAPI.update(editingId, payload);
        addToast('Watchlist updated', 'success');
      } else {
        await WatchlistsAPI.create(payload);
        addToast('Watchlist created', 'success');
      }
      resetForm();
      await load();
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function remove(id) {
    try { await WatchlistsAPI.remove(id); addToast('Watchlist removed', 'info'); await load(); }
    catch (e) { addToast(e.message, 'error'); }
  }

  async function runWatchlist(id) {
    try {
      const result = await WatchlistsAPI.run(id);
      addToast(`Watchlist run complete: found ${result?.new_packs?.length || result?.new_results || 0} new packs`, 'success');
      await load(); // reload to get updated results
    } catch (e) { addToast(e.message, 'error'); }
  }

  function goToSearch(w) {
    navigateToSearch(w.query, String(w.min_size || ''), String(w.max_size || ''));
  }

  async function downloadResult(item) {
    try {
      await DownloadsAPI.enqueue({
        pack_message: item.pack_message,
        bot: item.bot,
        server_address: item.server_address,
        channel: item.channel || '',
        filename: item.filename,
        file_size: item.size,
      });
      addToast(`Enqueued: ${escapeHtml(item.filename)}`, 'success');
    } catch (e) { addToast(e.message, 'error'); }
  }

  function openResultsModal(w) {
    const items = w.last_results || [];
    if (!items.length) return addToast('No results yet', 'info');
    // Sort: filename (asc, case-insensitive), size (asc), bot name (asc, case-insensitive)
    resultsItems = [...items].sort((a, b) => {
      const fnA = (a.filename || '').toLowerCase();
      const fnB = (b.filename || '').toLowerCase();
      if (fnA < fnB) return -1;
      if (fnA > fnB) return 1;
      const szA = a.size || 0;
      const szB = b.size || 0;
      if (szA !== szB) return szA - szB;
      const botA = (a.bot || '').toLowerCase();
      const botB = (b.bot || '').toLowerCase();
      if (botA < botB) return -1;
      if (botA > botB) return 1;
      return 0;
    });
    resultsFilterFilename = '';
    resultsFilterBot = '';
    resultsWatchlist = w;
    showResultsModal = true;
  }

  async function downloadAllResults() {
    for (const item of filteredResults) {
      await downloadResult(item);
    }
    addToast(`Download all completed: ${filteredResults.length} packs enqueued`, 'success');
  }
</script>

<Modal title={`Watchlist: ${resultsWatchlist?.name || ''} (${filteredResults.length} / ${resultsItems.length} results)`} visible={showResultsModal} on:close={() => showResultsModal = false}>
  <div class="wl-filters" style="display:flex;gap:8px;margin-bottom:8px;align-items:center">
    <input class="form-input" style="flex:1" placeholder="Filter by filename…" bind:value={resultsFilterFilename} />
    <input class="form-input" style="flex:1" placeholder="Filter by bot…" bind:value={resultsFilterBot} />
    <span class="text-sm text-muted" style="white-space:nowrap;display:flex;align-items:center">
      {filteredResults.length} / {resultsItems.length}
    </span>
  </div>
  <div class="table-container" style="max-height:500px;overflow-y:auto">
    <table>
      <thead><tr><th>File</th><th>Bot</th><th>Size</th><th>Action</th></tr></thead>
      <tbody>
        {#each filteredResults as r}
          <tr>
            <td class="truncate" style="max-width:200px" title={r.filename || 'Unknown'}>{r.filename || 'Unknown'}</td>
            <td class="text-sm">{r.bot || '—'}</td>
            <td class="text-sm">{r.size ? (r.size / 1024 / 1024).toFixed(1) + ' MB' : '—'}</td>
            <td><button class="btn btn-sm btn-primary" onclick={() => downloadResult(r)} title="Download">⬇️</button></td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  <div class="flex gap-1 mt-1" style="justify-content:flex-end">
    <button class="btn btn-sm btn-primary" onclick={downloadAllResults}>⬇️ Download All ({filteredResults.length})</button>
  </div>
</Modal>

{#if loading}
  <div class="spinner"></div>
{:else}
  <div class="card mb-2" class:card-highlight={formHighlight} bind:this={formCard}>
    <div class="card-header">
      <span class="card-title">{editingId ? 'Edit Watchlist' : 'Create Watchlist'}</span>
    </div>
    <div class="form-row">
      <div class="form-group">
        <label class="form-label" for="wl-form-name">Name</label>
        <input id="wl-form-name" class="form-input" bind:value={form.name} placeholder="e.g. Ubuntu ISOs Monitor" />
      </div>
      <div class="form-group">
        <label class="form-label" for="wl-form-query">Query</label>
        <input id="wl-form-query" class="form-input" bind:value={form.query} bind:this={queryInput} placeholder="e.g. Ubuntu 24.04" />
      </div>
      <div class="form-group">
        <label class="form-label" for="wl-form-interval">Interval (minutes)</label>
        <input id="wl-form-interval" class="form-input" bind:value={form.interval_minutes} type="number" min="5" />
      </div>
    </div>
    <div class="form-row">
      <div class="form-group">
        <label class="form-label" for="wl-form-min">Min Size</label>
        <input id="wl-form-min" class="form-input" bind:value={form.min_size} placeholder="optional" />
      </div>
      <div class="form-group">
        <label class="form-label" for="wl-form-max">Max Size</label>
        <input id="wl-form-max" class="form-input" bind:value={form.max_size} placeholder="optional" />
      </div>
    </div>
    <div class="flex gap-2 mb-1" style="align-items:center">
      <label class="form-label" style="margin:0;display:flex;align-items:center;gap:0.4rem;cursor:pointer">
        <input type="checkbox" bind:checked={form.notify_enabled} /> Notify on new results
      </label>
      <label class="form-label" style="margin:0;display:flex;align-items:center;gap:0.4rem;cursor:pointer">
        <input type="checkbox" bind:checked={form.auto_enqueue} /> Auto-enqueue
      </label>
      <label class="form-label" style="margin:0;display:flex;align-items:center;gap:0.4rem;cursor:pointer">
        <input type="checkbox" bind:checked={form.enabled} /> Enabled
      </label>
    </div>
    <div class="btn-group">
      <button class="btn btn-primary" onclick={save}>{editingId ? 'Update' : 'Create'}</button>
      {#if editingId}<button class="btn btn-ghost" onclick={resetForm}>Cancel</button>{/if}
    </div>
  </div>

  <div class="card">
    <div class="card-header"><span class="card-title">Active Watchlists ({$watchlists.length})</span></div>
    {#if $watchlists.length > 0}
      <div class="table-container">
        <table>
          <thead><tr>
            <th role="button" tabindex="0" onclick={() => toggleSort('name')} onkeydown={(e) => { if (e.key === 'Enter') toggleSort('name'); }} style="cursor:pointer;user-select:none">Name{sortArrow('name')}</th>
            <th role="button" tabindex="0" onclick={() => toggleSort('query')} onkeydown={(e) => { if (e.key === 'Enter') toggleSort('query'); }} style="cursor:pointer;user-select:none">Query{sortArrow('query')}</th>
            <th role="button" tabindex="0" onclick={() => toggleSort('interval')} onkeydown={(e) => { if (e.key === 'Enter') toggleSort('interval'); }} style="cursor:pointer;user-select:none">Interval{sortArrow('interval')}</th>
            <th role="button" tabindex="0" onclick={() => toggleSort('last_run')} onkeydown={(e) => { if (e.key === 'Enter') toggleSort('last_run'); }} style="cursor:pointer;user-select:none">Last Run{sortArrow('last_run')}</th>
            <th role="button" tabindex="0" onclick={() => toggleSort('new')} onkeydown={(e) => { if (e.key === 'Enter') toggleSort('new'); }} style="cursor:pointer;user-select:none">New{sortArrow('new')}</th>
            <th>Actions</th>
          </tr></thead>
          <tbody>
            {#each sortedWatchlists as w}
              <tr>
                <td><strong>{w.name}</strong></td>
                <td><span class="text-sm" role="button" tabindex="0" onclick={() => goToSearch(w)} onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); goToSearch(w); } }} style="font-family:monospace;background:var(--bg-tertiary);padding:0.1rem 0.3rem;border-radius:4px;cursor:pointer;text-decoration:underline;text-decoration-style:dotted;text-underline-offset:2px" title="Click to search">{w.query}</span></td>
                <td class="text-sm">{w.interval_minutes || 60}m</td>
                <td class="text-sm text-muted">{w.last_checked_at ? new Date(w.last_checked_at).toLocaleString() : 'never'}</td>
                <td class="text-sm" role="button" tabindex="0" onclick={() => (w.last_results?.length || 0) > 0 && openResultsModal(w)} onkeydown={(e) => { if ((e.key === 'Enter' || e.key === ' ') && (w.last_results?.length || 0) > 0) { e.preventDefault(); openResultsModal(w); } }} style="cursor:pointer;text-decoration:underline;text-decoration-style:dotted;text-underline-offset:2px" title="Click to view results">{w.last_results?.length || 0}</td>
                <td>
                  <div class="btn-group">
                    <button class="btn btn-sm btn-primary" onclick={() => runWatchlist(w.id)} title="Run now">▶️</button>
                    {#if (w.last_results?.length || 0) > 0}
                      <button class="btn btn-sm btn-ghost" onclick={() => openResultsModal(w)} title="Results">📋</button>
                    {/if}
                    <button class="btn btn-sm btn-ghost" onclick={() => startEdit(w)}>✏️</button>
                    <button class="btn btn-sm btn-ghost" onclick={() => remove(w.id)}>🗑️</button>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else}
      <div class="empty-state">
        <div class="empty-state-text">No watchlists configured</div>
        <div class="empty-state-sub">Watchlists periodically search for new packs matching your criteria</div>
      </div>
    {/if}
  </div>
{/if}
