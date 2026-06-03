<script>
  import { onMount } from 'svelte';
  import { searchResults, pendingSearchQuery } from '../lib/stores.js';
  import { SearchAPI, DownloadsAPI, ProvidersAPI } from '../lib/api.js';
  import { formatBytes, statusBadge, escapeHtml } from '../lib/utils.js';
  import { addToast } from '../lib/stores.js';

  // --- Query history ---
  const HISTORY_KEY = 'xdcc-search-history';
  const MAX_HISTORY = 10;
  let queryHistory = $state([]);

  function loadHistory() {
    try {
      queryHistory = JSON.parse(localStorage.getItem(HISTORY_KEY) || '[]');
    } catch { queryHistory = []; }
  }

  function saveQueryToHistory(q) {
    const trimmed = q.trim();
    if (!trimmed) return;
    let hist = queryHistory.filter(h => h !== trimmed);
    hist.unshift(trimmed);
    if (hist.length > MAX_HISTORY) hist = hist.slice(0, MAX_HISTORY);
    queryHistory = hist;
    try { localStorage.setItem(HISTORY_KEY, JSON.stringify(hist)); } catch {}
  }

  let query = $state('');
  let searching = $state(false);
  let providers = $state([]);
  let selectedProviders = $state([]);
  let minSize = $state('');
  let maxSize = $state('');
  let error = $state('');
  let results = $derived($searchResults);
  let lastProcessedHash = '';
  let pendingQuery = '';

  // --- Custom dropdown state ---
  let showDropdown = $state(false);
  let dropdownIndex = $state(-1);
  let inputRef = $state(null);

  // --- Client-side filters ---
  let filterName = $state('');
  let filterBot = $state('');
  let filterServer = $state('');
  let filterMinMB = $state(0);
  let filterMaxMB = $state(0);
  let compactMode = $state(false);

  // --- Slider range (min/max MB from current results) ---
  let sliderRange = $derived.by(() => {
    const packs = results?.packs;
    if (!packs?.length) return { min: 0, max: 0, hasData: false };
    let minB = Infinity, maxB = 0;
    for (const p of packs) {
      const sz = p.size ?? 0;
      if (sz > 0) {
        if (sz < minB) minB = sz;
        if (sz > maxB) maxB = sz;
      }
    }
    if (minB === Infinity) return { min: 0, max: 0, hasData: false };
    const minMB = Math.floor(minB / (1024 * 1024));
    const maxMB = Math.ceil(maxB / (1024 * 1024));
    return { min: minMB, max: maxMB, hasData: true };
  });

  // --- Sorting state ---
  let sortColumn = $state('');
  let sortDirection = $state('asc');

  // --- Active filter count for display
  let activeFilterCount = $derived.by(() => {
    let count = 0;
    if (filterName.trim()) count++;
    if (filterBot.trim()) count++;
    if (filterServer.trim()) count++;
    if (sliderRange.hasData) {
      const minActive = filterMinMB > sliderRange.min;
      const maxActive = filterMaxMB < sliderRange.max;
      if (minActive || maxActive) count++;
    }
    return count;
  });

  // --- Derived: sorted & filtered packs ---
  let sortedPacks = $derived.by(() => {
    if (!results?.packs?.length) return [];
    let packs = results.packs;

    // Apply client-side filters
    const fn = filterName.trim().toLowerCase();
    const fb = filterBot.trim().toLowerCase();
    const fs = filterServer.trim().toLowerCase();

    if (fn) packs = packs.filter(p => (p.filename || '').toLowerCase().includes(fn));
    if (fb) packs = packs.filter(p => (p.bot || '').toLowerCase().startsWith(fb));
    if (fs) packs = packs.filter(p => (p.server?.address || '').toLowerCase().includes(fs));
    if (sliderRange.hasData) {
      const minBytes = filterMinMB > sliderRange.min ? filterMinMB * 1024 * 1024 : 0;
      const maxBytes = filterMaxMB < sliderRange.max ? filterMaxMB * 1024 * 1024 : 0;
      packs = packs.filter(p => {
        const sz = p.size ?? 0;
        if (sz <= 0) return true;
        if (minBytes > 0 && sz < minBytes) return false;
        if (maxBytes > 0 && sz > maxBytes) return false;
        return true;
      });
    }

    if (!sortColumn) return packs;
    const dir = sortDirection === 'asc' ? 1 : -1;
    const sorted = [...packs].sort((a, b) => {
      let valA, valB;
      switch (sortColumn) {
        case 'filename':
          valA = (a.filename || '').toLowerCase();
          valB = (b.filename || '').toLowerCase();
          return valA < valB ? -dir : valA > valB ? dir : 0;
        case 'bot':
          valA = (a.bot || '').toLowerCase();
          valB = (b.bot || '').toLowerCase();
          return valA < valB ? -dir : valA > valB ? dir : 0;
        case 'channel':
          valA = (a.channel || '').toLowerCase();
          valB = (b.channel || '').toLowerCase();
          return valA < valB ? -dir : valA > valB ? dir : 0;
        case 'size':
          valA = a.size ?? 0;
          valB = b.size ?? 0;
          return (valA - valB) * dir;
        case 'server':
          valA = (a.server?.address || '').toLowerCase();
          valB = (b.server?.address || '').toLowerCase();
          return valA < valB ? -dir : valA > valB ? dir : 0;
        default:
          return 0;
      }
    });
    return sorted;
  });

  function toggleSort(column) {
    if (sortColumn === column) {
      // Toggle direction
      sortDirection = sortDirection === 'asc' ? 'desc' : 'asc';
    } else {
      sortColumn = column;
      sortDirection = 'asc';
    }
  }

  function sortIcon(column) {
    if (sortColumn !== column) return '↕';
    return sortDirection === 'asc' ? '▲' : '▼';
  }

  // Parse query params from URL hash (e.g., #search?q=ubuntu&min=100MB)
  function loadFromHash() {
    const hash = window.location.hash;
    if (!hash || !hash.includes('?')) return;
    if (hash === lastProcessedHash) return;
    lastProcessedHash = hash;
    const viewPart = hash.split('?')[0];
    if (!viewPart.replace('#', '').startsWith('search')) return;
    const params = new URLSearchParams(hash.split('?')[1]);
    const q = params.get('q');
    if (q) query = decodeURIComponent(q);
    const min = params.get('min');
    if (min) minSize = decodeURIComponent(min);
    const max = params.get('max');
    if (max) maxSize = decodeURIComponent(max);
    if (q) {
      if (providers.length === 0) {
        pendingQuery = q;
      } else {
        triggerSearch(q);
      }
    }
  }

  onMount(() => {
    loadHistory();
    loadProviders().then(() => {
      // Auto-search if we landed here with query params from a preset
      loadFromHash();
      // If a cross-component search was requested before providers were ready
      if (pendingQuery) {
        triggerSearch(pendingQuery);
        pendingQuery = '';
      }
    });

    // Listen for cross-component search requests via store
    const unsub = pendingSearchQuery.subscribe(q => {
      if (q) {
        pendingSearchQuery.set('');
        if (providers.length === 0) {
          pendingQuery = q;
        } else {
          triggerSearch(q);
        }
      }
    });

    // Handle hash changes when already on the Search page (e.g. from presets)
    const handleHashChange = () => { loadFromHash(); };
    window.addEventListener('hashchange', handleHashChange);

    return () => {
      unsub();
      window.removeEventListener('hashchange', handleHashChange);
    };
  });

  function triggerSearch(q) {
    query = q;
    doSearch();
  }

  async function loadProviders() {
    try {
      const data = await ProvidersAPI.list();
      // Backend returns { states: [...], insights: [...] }
      const states = data?.states || [];
      const insights = data?.insights || [];

      // Only show globally enabled providers
      providers = states
        .filter(state => {
          const insight = insights.find(i => i.name === state.name);
          if (insight !== undefined) return insight.enabled;
          return !(state.error && state.error.includes('disabled'));
        })
        .map(state => ({ name: state.name, enabled: true }));

      // All shown providers are enabled — pre-select all of them
      selectedProviders = providers.map(p => p.name);
    } catch (e) {
      console.error('Failed to load providers:', e);
    }
  }

  function clearFilters() {
    filterName = '';
    filterBot = '';
    filterServer = '';
    if (sliderRange.hasData) {
      filterMinMB = sliderRange.min;
      filterMaxMB = sliderRange.max;
    } else {
      filterMinMB = 0;
      filterMaxMB = 0;
    }
  }

  async function doSearch() {
    if (!query.trim()) return addToast('Enter a search query', 'warning');
    // Auto-select all available providers if none were pre-selected
    if (selectedProviders.length === 0 && providers.length > 0) {
      selectedProviders = providers.map(p => p.name);
    }
    if (selectedProviders.length === 0) return addToast('Enable at least one provider to search', 'warning');
    searching = true;
    error = '';
    clearFilters();
    saveQueryToHistory(query);
    try {
      const params = { q: query.trim(), providers: selectedProviders };
      if (minSize) params.min_size = minSize;
      if (maxSize) params.max_size = maxSize;
      if (compactMode) params.compact = 'true';
      const data = await SearchAPI.search(params);
      searchResults.set(data);
      // Initialize both size sliders to full range so all results are included at start
      if (data?.packs?.length) {
        let minB = Infinity, maxB = 0;
        for (const p of data.packs) {
          const sz = p.size ?? 0;
          if (sz > 0) {
            if (sz < minB) minB = sz;
            if (sz > maxB) maxB = sz;
          }
        }
        if (maxB > 0) {
          filterMinMB = Math.floor(minB / (1024 * 1024));
          filterMaxMB = Math.ceil(maxB / (1024 * 1024));
        }
      }
    } catch (e) {
      error = e.message;
      addToast(e.message, 'error');
    }
    searching = false;
  }

  async function parseAndDownload() {
    const msg = prompt('Paste an XDCC pack message to parse:');
    if (!msg) return;
    try {
      const parsed = await SearchAPI.parse(msg);
      if (!parsed?.parsed) return addToast(parsed?.error || 'Could not parse pack info', 'error');

      const { bot, pack_message, pack_number, server_address } = parsed;

      // Resolve missing bot or server interactively
      let resolvedBot = bot;
      let resolvedServer = server_address;

      if (!resolvedBot) {
        resolvedBot = prompt('Bot name not found — enter bot name (or leave empty to cancel):');
        if (!resolvedBot) { addToast('Parse cancelled — bot name required', 'info'); return; }
      }
      if (!resolvedServer) {
        resolvedServer = prompt('Server not found — enter server address (e.g. irc.rizon.net:6667):');
        if (!resolvedServer) { addToast('Parse cancelled — server address required', 'info'); return; }
      }

      await DownloadsAPI.enqueue({
        pack_message,
        bot: resolvedBot,
        server_address: resolvedServer,
        channel: '',
        filename: '',
        file_size: 0,
      });
      addToast(`Download queued: ${escapeHtml(resolvedBot)} pack #${pack_number}`, 'success');
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function downloadPack(pack) {
    try {
      // Generate pack_message in the format "xdcc send #<number>"
      const packMessage = `xdcc send #${pack.pack_number}`;
      
      await DownloadsAPI.enqueue({
        pack_message: packMessage,
        bot: pack.bot,
        channel: pack.channel || '',  // Empty channel - WHOIS will discover it
        filename: pack.filename,
        file_size: pack.size,
        server_address: pack.server?.address || 'unknown',
      });
      addToast(`Download queued: ${escapeHtml(pack.filename)}`, 'success');
    } catch (e) { addToast(e.message, 'error'); }
  }

  function toggleProvider(name) {
    if (selectedProviders.includes(name)) {
      selectedProviders = selectedProviders.filter(p => p !== name);
    } else {
      selectedProviders = [...selectedProviders, name];
    }
  }

  // Filtered history for dropdown
  let filteredHistory = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!q) return queryHistory;
    return queryHistory.filter(h => h.toLowerCase().includes(q));
  });

  function handleInputFocus() {
    if (queryHistory.length > 0) {
      showDropdown = true;
      dropdownIndex = -1;
    }
  }

  function handleInputBlur(e) {
    // Delay to allow click on dropdown item to register first
    setTimeout(() => { showDropdown = false; dropdownIndex = -1; }, 150);
  }

  function selectHistoryItem(item) {
    query = item;
    showDropdown = false;
    dropdownIndex = -1;
  }

  function handleDropdownKeydown(e) {
    if (!showDropdown || filteredHistory.length === 0) {
      if (e.key === 'Enter') { doSearch(); }
      return;
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      dropdownIndex = (dropdownIndex + 1) % filteredHistory.length;
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      dropdownIndex = dropdownIndex <= 0 ? filteredHistory.length - 1 : dropdownIndex - 1;
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (dropdownIndex >= 0) {
        selectHistoryItem(filteredHistory[dropdownIndex]);
      } else {
        doSearch();
      }
    } else if (e.key === 'Escape') {
      showDropdown = false;
      dropdownIndex = -1;
    }
  }

  // Keep dropdownIndex in bounds when filtered list changes
  $effect(() => {
    if (dropdownIndex >= filteredHistory.length) {
      dropdownIndex = filteredHistory.length > 0 ? filteredHistory.length - 1 : -1;
    }
  });

</script>

<div class="card mb-2">
  <div class="filters-bar">
    <div class="form-group" style="flex:1;min-width:250px;position:relative">
      <label class="form-label" for="search-query">Search Query</label>
      <input
        id="search-query"
        class="form-input"
        bind:value={query}
        bind:this={inputRef}
        placeholder="e.g. Ubuntu 24.04"
        onfocus={handleInputFocus}
        onblur={handleInputBlur}
        onkeydown={handleDropdownKeydown}
        autocomplete="off" />
      {#if showDropdown && filteredHistory.length > 0}
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <ul class="history-dropdown" role="listbox">
          {#each filteredHistory as h, i}
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
            <li
              class="history-item"
              class:highlighted={i === dropdownIndex}
              role="option"
              aria-selected={i === dropdownIndex}
              tabindex="-1"
              onmousedown={(e) => { e.preventDefault(); selectHistoryItem(h); }}>
              {h}
            </li>
          {/each}
        </ul>
      {/if}
    </div>
    <div class="form-group" style="min-width:120px">
      <label class="form-label" for="search-min-size">Min Size</label>
      <input id="search-min-size" class="form-input" bind:value={minSize} placeholder="e.g. 100MB" />
    </div>
    <div class="form-group" style="min-width:120px">
      <label class="form-label" for="search-max-size">Max Size</label>
      <input id="search-max-size" class="form-input" bind:value={maxSize} placeholder="e.g. 4GB" />
    </div>
    <div class="form-group" style="display:flex;align-items:end">
      <button class="btn btn-primary btn-lg" onclick={doSearch} disabled={searching}>
        {searching ? '🔍 Searching...' : '🔍 Search'}
      </button>
    </div>
  </div>

  <div class="flex gap-1" style="flex-wrap:wrap;align-items:center">
    {#if providers.length > 0}
      <span class="text-sm text-muted">Providers:</span>
      {#each providers as p}
        <button class="btn btn-sm" class:btn-primary={selectedProviders.includes(p.name)} class:btn-ghost={!selectedProviders.includes(p.name)} onclick={() => toggleProvider(p.name)}>
          {p.name}
        </button>
      {/each}
      <span class="separator-dot">·</span>
    {/if}
    <label class="toggle-label" title="Collapse duplicate results sharing the same filename, size, and bot family">
      <input type="checkbox" bind:checked={compactMode} />
      <span class="toggle-text">Compact</span>
    </label>
  </div>
</div>

<div class="flex gap-1 mb-2" style="align-items:center">
  <button class="btn btn-sm btn-ghost" onclick={parseAndDownload}>📋 Parse & Download from IRC message</button>
</div>

{#if searching}
  <div class="spinner"></div>
{:else if error}
  <div class="empty-state">
    <div class="empty-state-icon">⚠️</div>
    <div class="empty-state-text">Search failed</div>
    <div class="empty-state-sub">{error}</div>
  </div>
{:else if results}
  <div class="card">
    <div class="card-header">
      <span class="card-title">Results</span>
      <span class="text-sm text-muted">{results.total_results || results.packs?.length || 0} packs found</span>
    </div>
    {#if results.packs?.length > 0}
      <!-- Client-side filter bar — always visible when results exist, even if all filtered out -->
      <div class="filter-bar">
        <div class="filter-inputs">
          <input class="form-input" bind:value={filterName} placeholder="Filter by name..." />
          <input class="form-input" bind:value={filterBot} placeholder="Filter by bot..." />
          <input class="form-input" bind:value={filterServer} placeholder="Filter by server..." />
          {#if sliderRange.hasData}
            <div class="dual-slider-group">
              <div class="dual-slider-labels">
                <span class="slider-label">Min {filterMinMB} MB</span>
                <span class="slider-label">Max {filterMaxMB} MB</span>
              </div>
              <div class="dual-slider-tracks">
                <input type="range" class="size-slider size-slider-min"
                  min={sliderRange.min} max={sliderRange.max}
                  bind:value={filterMinMB}
                  oninput={() => { if (filterMinMB > filterMaxMB) filterMinMB = filterMaxMB; }} />
                <input type="range" class="size-slider size-slider-max"
                  min={sliderRange.min} max={sliderRange.max}
                  bind:value={filterMaxMB}
                  oninput={() => { if (filterMaxMB < filterMinMB) filterMaxMB = filterMinMB; }} />
              </div>
            </div>
          {/if}
        </div>
        {#if activeFilterCount > 0}
          <button class="btn btn-sm btn-ghost" onclick={clearFilters}>
            ✕ Clear {activeFilterCount} filter{activeFilterCount !== 1 ? 's' : ''}
          </button>
        {/if}
      </div>

      {#if sortedPacks.length > 0}
      <div class="table-container">
        <table>
          <thead>
            <tr>
              <th class="sortable" onclick={() => toggleSort('filename')}>
                File <span class="sort-icon">{sortIcon('filename')}</span>
              </th>
              <th class="sortable" onclick={() => toggleSort('bot')}>
                Bot <span class="sort-icon">{sortIcon('bot')}</span>
              </th>
              <th class="sortable" onclick={() => toggleSort('channel')}>
                Channel <span class="sort-icon">{sortIcon('channel')}</span>
              </th>
              <th class="sortable" onclick={() => toggleSort('size')}>
                Size <span class="sort-icon">{sortIcon('size')}</span>
              </th>
              <th class="sortable" onclick={() => toggleSort('server')}>
                Server <span class="sort-icon">{sortIcon('server')}</span>
              </th>
              <th>Actions</th>
            </tr>
          </thead>
          <tbody>
            {#each sortedPacks as pack}
              <tr>
                <td class="truncate" style="max-width:250px" title={pack.filename}>{pack.filename || 'Unknown'}</td>
                <td>{pack.bot || '—'}</td>
                <td>{pack.channel || '—'}</td>
                <td class="text-sm">{formatBytes(pack.size)}</td>
                <td><span class="badge badge-info">{pack.server?.address || '?'}</span></td>
                <td>
                  <button class="btn btn-sm btn-primary" onclick={() => downloadPack(pack)}>⬇️ Download</button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if results.packs.length >= 50}
        <div class="pagination">
          <span class="page-info">Showing first 50 results. Refine your search for more specific results.</span>
        </div>
      {/if}
      {:else if results.packs?.length > 0}
      <div class="empty-state">
        <div class="empty-state-text">All {results.packs.length} results filtered out</div>
        <div class="empty-state-sub">
          {#if activeFilterCount > 0}
            <button class="btn btn-sm btn-ghost" onclick={clearFilters}>✕ Clear filters to see results</button>
          {:else}
            Try adjusting the client-side filters above
          {/if}
        </div>
      </div>
    {:else}
      <div class="empty-state">
        <div class="empty-state-text">No results found</div>
        <div class="empty-state-sub">Try a different search query</div>
      </div>
    {/if}
    {/if}
  </div>
{:else}
  <div class="empty-state">
    <div class="empty-state-icon">🔍</div>
    <div class="empty-state-text">Search for XDCC packs</div>
    <div class="empty-state-sub">Search across multiple providers (NIBL, SubSplease, XDCC.eu)</div>
  </div>
{/if}

<style>
  th.sortable {
    cursor: pointer;
    user-select: none;
    transition: color 0.15s ease;
  }
  th.sortable:hover {
    color: var(--accent-light);
  }
  .sort-icon {
    display: inline-block;
    font-size: 0.7rem;
    margin-left: 0.25rem;
    opacity: 0.4;
    transition: opacity 0.15s ease, transform 0.15s ease;
  }
  th.sortable:hover .sort-icon {
    opacity: 0.8;
  }
  th.sortable:active .sort-icon {
    transform: scale(0.85);
  }

  .size-slider {
    -webkit-appearance: none;
    appearance: none;
    width: 100%;
    height: 6px;
    background: var(--bg-tertiary);
    border-radius: 3px;
    outline: none;
    cursor: pointer;
  }
  .size-slider::-webkit-slider-thumb {
    -webkit-appearance: none;
    appearance: none;
    width: 18px;
    height: 18px;
    border-radius: 50%;
    background: var(--accent);
    border: 2px solid var(--accent-light);
    cursor: pointer;
    transition: transform 0.15s ease;
  }
  .size-slider::-webkit-slider-thumb:hover {
    transform: scale(1.2);
  }
  .size-slider::-moz-range-thumb {
    width: 18px;
    height: 18px;
    border-radius: 50%;
    background: var(--accent);
    border: 2px solid var(--accent-light);
    cursor: pointer;
  }

  /* --- Filter bar above results table --- */
  .filter-bar {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem 1rem;
    background: var(--bg-secondary);
    border: 1px solid var(--border-color);
    border-radius: var(--radius-lg);
    margin-bottom: 0.75rem;
    flex-wrap: wrap;
  }
  .filter-bar .filter-inputs {
    display: flex;
    gap: 0.5rem;
    flex: 1;
    flex-wrap: wrap;
    align-items: center;
  }
  .filter-bar .form-input {
    width: 180px;
    padding: 0.4rem 0.6rem;
    font-size: 0.82rem;
    height: 34px;
  }

  /* --- Dual range slider --- */
  .dual-slider-group {
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    min-width: 220px;
  }
  .dual-slider-labels {
    display: flex;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .slider-label {
    font-size: 0.7rem;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    white-space: nowrap;
  }
  .dual-slider-tracks {
    position: relative;
    height: 6px;
  }
  .dual-slider-tracks .size-slider {
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
    height: 6px;
    -webkit-appearance: none;
    appearance: none;
    background: transparent;
    pointer-events: none;
    outline: none;
    margin: 0;
  }
  .dual-slider-tracks .size-slider::-webkit-slider-thumb {
    -webkit-appearance: none;
    appearance: none;
    width: 18px;
    height: 18px;
    border-radius: 50%;
    background: var(--accent);
    border: 2px solid var(--accent-light);
    cursor: pointer;
    pointer-events: auto;
    transition: transform 0.15s ease;
  }
  .dual-slider-tracks .size-slider::-webkit-slider-thumb:hover {
    transform: scale(1.2);
  }
  .dual-slider-tracks .size-slider::-moz-range-thumb {
    width: 18px;
    height: 18px;
    border-radius: 50%;
    background: var(--accent);
    border: 2px solid var(--accent-light);
    cursor: pointer;
    pointer-events: auto;
  }
  .dual-slider-tracks .size-slider::-webkit-slider-runnable-track {
    height: 6px;
    background: transparent;
  }
  .dual-slider-tracks .size-slider::-moz-range-track {
    height: 6px;
    background: transparent;
  }
  /* Visual track behind both sliders */
  .dual-slider-tracks::before {
    content: '';
    position: absolute;
    top: 50%;
    left: 0;
    right: 0;
    height: 6px;
    background: var(--bg-tertiary);
    border-radius: 3px;
    transform: translateY(-50%);
    pointer-events: none;
  }

  /* --- Compact toggle --- */
  .toggle-label {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    cursor: pointer;
    user-select: none;
    font-size: 0.82rem;
    color: var(--text-muted);
    transition: color 0.15s ease;
    white-space: nowrap;
    padding: 0.25rem 0.4rem;
    border-radius: var(--radius-sm);
    border: 1px solid transparent;
    transition: all 0.15s ease;
  }
  .toggle-label:hover {
    color: var(--text);
    border-color: var(--border-color);
  }
  .toggle-label input[type="checkbox"] {
    accent-color: var(--accent);
    width: 15px;
    height: 15px;
    cursor: pointer;
  }
  .toggle-text {
    font-weight: 500;
  }

  /* --- Custom history dropdown --- */
  .history-dropdown {
    position: absolute;
    top: 100%;
    left: 0;
    right: 0;
    z-index: 100;
    margin: 0;
    padding: 0.35rem 0;
    list-style: none;
    background: var(--bg-card);
    border: 1px solid var(--accent);
    border-top: none;
    border-radius: 0 0 var(--radius) var(--radius);
    box-shadow: var(--shadow-lg);
    max-height: 240px;
    overflow-y: auto;
  }
  .history-item {
    padding: 0.5rem 0.75rem;
    cursor: pointer;
    font-size: 0.88rem;
    color: var(--text-primary);
    transition: background var(--transition);
  }
  .history-item:hover,
  .history-item.highlighted {
    background: var(--bg-hover);
    color: var(--accent-light);
  }
</style>
