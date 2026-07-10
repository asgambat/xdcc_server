<script>
  import { onMount, onDestroy } from 'svelte';
  import { searchResults, pendingSearchQuery } from '../lib/stores.js';
  import { SearchAPI, DownloadsAPI, ProvidersAPI, PresetsAPI } from '../lib/api.js';
  import { formatBytes, statusBadge, escapeHtml, normalizeSize } from '../lib/utils.js';
  import { addToast } from '../lib/stores.js';
  import { SEARCH_COL_WIDTHS } from '../lib/columnDefaults.js';
  import Modal from './Modal.svelte';

  // --- Query history ---
  const HISTORY_KEY = 'xdcc-search-history';
  const PAGE_SIZE_KEY = 'xdcc-search-page-size';
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

  // Auto-focus search input when arriving via CTRL+K shortcut
  let searchMounted = $state(false);
  $effect(() => {
    if (inputRef && !searchMounted) {
      inputRef.focus();
      searchMounted = true;
    }
  });

  // --- Client-side filters ---
  let filterName = $state('');
  let filterBot = $state('');
  let filterServer = $state('');
  let filterMinMB = $state(0);
  let filterMaxMB = $state(0);
  let compactMode = $state(false);
  let mediaFilter = $state('all'); // 'all' | 'video' | 'audio' | 'books' | 'zip'
  let hqMode = $state(false);
  let prefixMode = $state(false);
  let pageSize = $state(50);

  // --- Save as preset modal ---
  let showSavePresetModal = $state(false);
  let presetName = $state('');

  // Load saved page size from localStorage on init
  function loadPageSize() {
    try {
      const saved = parseInt(localStorage.getItem(PAGE_SIZE_KEY), 10);
      if ([10, 50, 100, 200, 500].includes(saved)) pageSize = saved;
    } catch {}
  }
  loadPageSize();

  // React to hash changes when navigating from other pages (e.g., watchlists, presets)
  // This is needed because the Search component is not remounted on navigation
  $effect(() => {
    const hash = window.location.hash;
    if (hash && hash.startsWith('#search')) {
      loadFromHash();
    }
  });

  // Persist page size to localStorage on change
  $effect(() => {
    // read pageSize to track it
    const val = pageSize;
    try { localStorage.setItem(PAGE_SIZE_KEY, String(val)); } catch {}
  });

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

  // --- Column visibility / resizable widths (persisted to localStorage) ---
  const SEARCH_COL_VIS_KEY = 'xdcc-search-columns';
  const SEARCH_COL_WIDTHS_KEY = 'xdcc-search-column-widths';
  const SEARCH_MIN_COL_WIDTH = 50;
  const SEARCH_COL_ORDER = ['file', 'bot', 'channel', 'size', 'server', 'actions'];
  const SEARCH_COL_LABELS = { file: 'File', bot: 'Bot', channel: 'Channel', size: 'Size', server: 'Server', actions: 'Actions' };
  let searchColumnVis = $state({ file: true, bot: true, channel: true, size: true, server: true, actions: true });
  let searchShowColumnPicker = $state(false);
  let searchColumnWidths = $state({ ...SEARCH_COL_WIDTHS });
  let searchResizing = $state(null);
  let searchDragIndicator = $state(null);
  let searchTable = $state();

  function loadSearchColumnVis() {
    try {
      const saved = JSON.parse(localStorage.getItem(SEARCH_COL_VIS_KEY));
      if (saved && typeof saved === 'object') { searchColumnVis = { ...searchColumnVis, ...saved }; }
    } catch {}
  }

  function toggleSearchColumn(col) {
    searchColumnVis = { ...searchColumnVis, [col]: !searchColumnVis[col] };
  }

  function loadSearchColumnWidths() {
    try {
      const saved = JSON.parse(localStorage.getItem(SEARCH_COL_WIDTHS_KEY));
      if (saved && typeof saved === 'object') { searchColumnWidths = { ...searchColumnWidths, ...saved }; }
    } catch {}
  }

  function searchGetVisibleColIndex(col) {
    let idx = 0;
    for (const c of SEARCH_COL_ORDER) {
      if (c === col) return idx;
      if (searchColumnVis[c]) idx++;
    }
    return -1;
  }

  function endSearchResize() {
    if (searchResizing) {
      searchResizing = null;
      searchDragIndicator = null;
      document.body.style.userSelect = '';
      document.body.style.cursor = '';
    }
  }

  function searchStartResize(e, col) {
    e.preventDefault();
    e.stopPropagation();
    searchResizing = { col, startX: e.clientX, startWidth: searchColumnWidths[col] };
    document.body.style.userSelect = 'none';
    document.body.style.cursor = 'col-resize';
    const container = e.target.closest('.table-container');
    if (container) {
      const r = container.getBoundingClientRect();
      searchDragIndicator = { x: e.clientX, top: r.top, height: r.height };
    }
  }

  function searchAutoFitColumn(e, col) {
    const colIdx = searchGetVisibleColIndex(col);
    if (colIdx < 0) return;
    const table = e?.target?.closest?.('table') || searchTable;
    if (!table) return;
    const m = document.createElement('span');
    m.style.cssText = 'position:absolute;visibility:hidden;white-space:nowrap;font-family:Inter,-apple-system,BlinkMacSystemFont,sans-serif;';
    document.body.appendChild(m);
    let maxW = SEARCH_MIN_COL_WIDTH;
    m.style.fontWeight = '600';
    m.style.fontSize = '0.8rem';
    m.textContent = SEARCH_COL_LABELS[col] || col;
    maxW = Math.max(maxW, m.offsetWidth + 30);
    m.style.fontWeight = 'normal';
    m.style.fontSize = '0.88rem';
    for (const row of table.querySelectorAll('tbody tr')) {
      const cell = /** @type {HTMLTableRowElement} */ (row).cells[colIdx];
      if (!cell) continue;
      m.textContent = cell.textContent.trim();
      const w = m.offsetWidth + 30;
      if (w > maxW) maxW = w;
    }
    document.body.removeChild(m);
    searchColumnWidths[col] = Math.max(SEARCH_MIN_COL_WIDTH, Math.ceil(maxW));
  }

  function searchAutoFitAllColumns(e) {
    for (const col of Object.keys(searchColumnVis)) {
      if (searchColumnVis[col]) searchAutoFitColumn(e, col);
    }
  }

  function searchResetColumnWidths() {
    searchColumnWidths = { ...SEARCH_COL_WIDTHS };
  }

  // Persist column visibility to localStorage
  $effect(() => {
    const vis = searchColumnVis;
    try { localStorage.setItem(SEARCH_COL_VIS_KEY, JSON.stringify(vis)); } catch {}
  });

  // Persist column widths to localStorage
  $effect(() => {
    const w = searchColumnWidths;
    try { localStorage.setItem(SEARCH_COL_WIDTHS_KEY, JSON.stringify(w)); } catch {}
  });

  loadSearchColumnVis();
  loadSearchColumnWidths();

  onDestroy(() => {
    if (searchResizing) {
      document.body.style.userSelect = '';
      document.body.style.cursor = '';
    }
    searchResizing = null;
    searchDragIndicator = null;
  });

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
      if (minSize) { minSize = normalizeSize(minSize); params.min_size = minSize; }
      if (maxSize) { maxSize = normalizeSize(maxSize); params.max_size = maxSize; }
      if (compactMode) params.compact = 'true';
      if (mediaFilter === 'video') params.video_only = 'true';
      if (mediaFilter === 'audio') params.audio_only = 'true';
      if (mediaFilter === 'books') params.books_only = 'true';
      if (mediaFilter === 'zip') params.zip_only = 'true';
      // HQ mode: append exclusion terms to filter out low-quality packs
      if (hqMode) params.q += ' -MD -TS';
      // Prefix mode: keep only packs whose filename starts with the query
      if (prefixMode) params.prefix = query.trim();
      params.pageSize = pageSize;
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

  async function saveAsPreset() {
    const name = presetName.trim();
    if (!name) return addToast('Enter a preset name', 'warning');
    try {
      await PresetsAPI.create({
        name,
        query: query.trim(),
        providers: selectedProviders,
        min_size: normalizeSize(minSize),
        max_size: normalizeSize(maxSize),
      });
      addToast(`Preset "${name}" created`, 'success');
      showSavePresetModal = false;
      presetName = '';
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

<svelte:window
  onmousemove={(e) => {
    if (searchResizing) {
      const delta = e.clientX - searchResizing.startX;
      searchColumnWidths[searchResizing.col] = Math.max(SEARCH_MIN_COL_WIDTH, searchResizing.startWidth + delta);
      if (searchDragIndicator) searchDragIndicator.x = e.clientX;
    }
  }}
  onmouseup={endSearchResize}
  onpointerup={endSearchResize}
  onpointercancel={endSearchResize}
/>

{#if searchDragIndicator}
  <div class="drag-guide-line" style="left:{searchDragIndicator.x}px;top:{searchDragIndicator.top}px;height:{searchDragIndicator.height}px"></div>
{/if}

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
    {#if providers.length > 1}
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
    <span class="text-sm text-muted">Media:</span>
    <div class="media-filter-group" role="radiogroup" aria-label="Media type filter">
      <button class="media-filter-btn" class:active={mediaFilter === 'all'} onclick={() => mediaFilter = 'all'} title="Show all file types">All</button>
      <button class="media-filter-btn" class:active={mediaFilter === 'video'} onclick={() => mediaFilter = 'video'} title="🎬 Video (avi, mpeg, mkv, mp4, mpg, mov)">🎬</button>
      <button class="media-filter-btn" class:active={mediaFilter === 'audio'} onclick={() => mediaFilter = 'audio'} title="🎵 Audio (mp3, m4a, ogg, flac, aac)">🎵</button>
      <button class="media-filter-btn" class:active={mediaFilter === 'books'} onclick={() => mediaFilter = 'books'} title="📚 Books (epub, mobi, pdf)">📚</button>
      <button class="media-filter-btn" class:active={mediaFilter === 'zip'} onclick={() => mediaFilter = 'zip'} title="📦 Archives (zip, rar, 7z, tar, gz, bz2, xz)">📦</button>
    </div>
    <label class="toggle-label" title="Exclude low-quality packs containing MD or TS in the filename">
      <input type="checkbox" bind:checked={hqMode} />
      <span class="toggle-text">HQ</span>
    </label>
    <label class="toggle-label" title="Keep only packs whose filename starts with the search query">
      <input type="checkbox" bind:checked={prefixMode} />
      <span class="toggle-text">Prefix</span>
    </label>
    <span class="separator-dot">·</span>
    <label class="page-size-label">
      <span class="page-size-text">Results:</span>
      <select class="page-size-select" bind:value={pageSize}>
        <option value={10}>10</option>
        <option value={50}>50</option>
        <option value={100}>100</option>
        <option value={200}>200</option>
        <option value={500}>500</option>
      </select>
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
      <div style="position:relative">
        <button class="btn btn-sm btn-ghost" onclick={(e) => { e.stopPropagation(); searchShowColumnPicker = !searchShowColumnPicker; }}>⚙️ Columns</button>
        {#if searchShowColumnPicker}
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div class="column-picker" role="menu" tabindex="-1" style="right:0;min-width:160px" onclick={(e) => e.stopPropagation()} onkeydown={(e) => { if (e.key === 'Escape') searchShowColumnPicker = false; }}>
            {#each SEARCH_COL_ORDER as col}
              <label class="column-picker-item">
                <input type="checkbox" checked={searchColumnVis[col]} onchange={() => toggleSearchColumn(col)} />
                <span>{SEARCH_COL_LABELS[col] || col}</span>
              </label>
            {/each}
            <div class="column-picker-separator"></div>
            <button class="column-picker-item column-picker-action" onclick={(e) => { searchAutoFitAllColumns(e); searchShowColumnPicker = false; }}>📐 Auto-fit all</button>
            <button class="column-picker-item column-picker-action" onclick={() => { searchResetColumnWidths(); searchShowColumnPicker = false; }}>↺ Reset defaults</button>
          </div>
        {/if}
      </div>
      <button class="btn btn-sm btn-primary" onclick={() => { presetName = query.trim(); showSavePresetModal = true; }}>💾 Save as preset</button>
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
        <table style="table-layout:fixed;width:100%" bind:this={searchTable}>
          <thead>
            <tr>
              {#if searchColumnVis.file}<th class="sortable" role="button" tabindex="0" onclick={() => toggleSort('filename')} onkeydown={(e) => { if (e.key === 'Enter') toggleSort('filename'); }} style="cursor:pointer;user-select:none;width:{searchColumnWidths.file}px;position:relative">File <span class="sort-icon">{sortIcon('filename')}</span>
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <div class="resize-handle" role="separator" onmousedown={(e) => searchStartResize(e, 'file')} onclick={(e) => e.stopPropagation()} ondblclick={(e) => { e.stopPropagation(); searchAutoFitColumn(e, 'file'); }}></div>
              </th>{/if}
              {#if searchColumnVis.bot}<th class="sortable" role="button" tabindex="0" onclick={() => toggleSort('bot')} onkeydown={(e) => { if (e.key === 'Enter') toggleSort('bot'); }} style="cursor:pointer;user-select:none;width:{searchColumnWidths.bot}px;position:relative">Bot <span class="sort-icon">{sortIcon('bot')}</span>
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <div class="resize-handle" role="separator" onmousedown={(e) => searchStartResize(e, 'bot')} onclick={(e) => e.stopPropagation()} ondblclick={(e) => { e.stopPropagation(); searchAutoFitColumn(e, 'bot'); }}></div>
              </th>{/if}
              {#if searchColumnVis.channel}<th class="sortable" role="button" tabindex="0" onclick={() => toggleSort('channel')} onkeydown={(e) => { if (e.key === 'Enter') toggleSort('channel'); }} style="cursor:pointer;user-select:none;width:{searchColumnWidths.channel}px;position:relative">Channel <span class="sort-icon">{sortIcon('channel')}</span>
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <div class="resize-handle" role="separator" onmousedown={(e) => searchStartResize(e, 'channel')} onclick={(e) => e.stopPropagation()} ondblclick={(e) => { e.stopPropagation(); searchAutoFitColumn(e, 'channel'); }}></div>
              </th>{/if}
              {#if searchColumnVis.size}<th class="sortable" role="button" tabindex="0" onclick={() => toggleSort('size')} onkeydown={(e) => { if (e.key === 'Enter') toggleSort('size'); }} style="cursor:pointer;user-select:none;width:{searchColumnWidths.size}px;position:relative">Size <span class="sort-icon">{sortIcon('size')}</span>
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <div class="resize-handle" role="separator" onmousedown={(e) => searchStartResize(e, 'size')} onclick={(e) => e.stopPropagation()} ondblclick={(e) => { e.stopPropagation(); searchAutoFitColumn(e, 'size'); }}></div>
              </th>{/if}
              {#if searchColumnVis.server}<th class="sortable" role="button" tabindex="0" onclick={() => toggleSort('server')} onkeydown={(e) => { if (e.key === 'Enter') toggleSort('server'); }} style="cursor:pointer;user-select:none;width:{searchColumnWidths.server}px;position:relative">Server <span class="sort-icon">{sortIcon('server')}</span>
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <div class="resize-handle" role="separator" onmousedown={(e) => searchStartResize(e, 'server')} onclick={(e) => e.stopPropagation()} ondblclick={(e) => { e.stopPropagation(); searchAutoFitColumn(e, 'server'); }}></div>
              </th>{/if}
              {#if searchColumnVis.actions}<th style="width:{searchColumnWidths.actions}px;position:relative">Actions
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <div class="resize-handle" role="separator" onmousedown={(e) => searchStartResize(e, 'actions')} ondblclick={(e) => { e.stopPropagation(); searchAutoFitColumn(e, 'actions'); }}></div>
              </th>{/if}
            </tr>
          </thead>
          <tbody>
            {#each sortedPacks as pack}
              <tr>
                {#if searchColumnVis.file}<td class="truncate" title={pack.filename}>{pack.filename || 'Unknown'}</td>{/if}
                {#if searchColumnVis.bot}<td>{pack.bot || '—'}</td>{/if}
                {#if searchColumnVis.channel}<td>{pack.channel || '—'}</td>{/if}
                {#if searchColumnVis.size}<td class="text-sm">{formatBytes(pack.size)}</td>{/if}
                {#if searchColumnVis.server}<td><span class="badge badge-info">{pack.server?.address || '?'}</span></td>{/if}
                {#if searchColumnVis.actions}<td>
                  <button class="btn btn-sm btn-primary" onclick={() => downloadPack(pack)}>⬇️ <span class="hide-mobile">Download</span></button>
                </td>{/if}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if results.packs.length >= pageSize}
        <div class="pagination">
          <span class="page-info">Showing first {pageSize} results. Refine your search for more specific results.</span>
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

<Modal title="Save Search as Preset" visible={showSavePresetModal} on:close={() => showSavePresetModal = false}>
  <div class="form-group">
    <label class="form-label" for="save-preset-name">Preset Name</label>
    <input id="save-preset-name" class="form-input" bind:value={presetName} placeholder="e.g. Ubuntu ISOs" />
  </div>
  <div class="text-sm text-muted mb-1">
    Query: <code>{query.trim()}</code>
  </div>
  <div class="modal-actions">
    <button class="btn btn-ghost" onclick={() => showSavePresetModal = false}>Cancel</button>
    <button class="btn btn-primary" onclick={saveAsPreset}>Save Preset</button>
  </div>
</Modal>

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

  /* --- Media filter button group --- */
  .media-filter-group {
    display: inline-flex;
    border-radius: var(--radius-sm);
    overflow: hidden;
    border: 1px solid var(--border-color);
  }
  .media-filter-btn {
    padding: 0.25rem 0.55rem;
    font-size: 0.78rem;
    font-weight: 500;
    color: var(--text-muted);
    background: var(--bg-secondary);
    border: none;
    border-right: 1px solid var(--border-color);
    cursor: pointer;
    transition: all 0.15s ease;
    user-select: none;
    white-space: nowrap;
  }
  .media-filter-btn:last-child {
    border-right: none;
  }
  .media-filter-btn:hover {
    color: var(--text);
    background: var(--bg-hover);
  }
  .media-filter-btn.active {
    color: var(--bg-card);
    background: var(--accent);
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

  /* --- Page size selector --- */
  .page-size-label {
    display: flex;
    align-items: center;
    gap: 0.35rem;
    cursor: pointer;
    user-select: none;
    font-size: 0.82rem;
    color: var(--text-muted);
    white-space: nowrap;
    padding: 0.25rem 0.4rem;
    border-radius: var(--radius-sm);
    border: 1px solid transparent;
    transition: all 0.15s ease;
  }
  .page-size-label:hover {
    color: var(--text);
    border-color: var(--border-color);
  }
  .page-size-text {
    font-weight: 500;
  }
  .page-size-select {
    background: var(--bg-input);
    color: var(--text);
    border: 1px solid var(--border-color);
    border-radius: var(--radius-sm);
    padding: 0.2rem 0.35rem;
    font-size: 0.82rem;
    cursor: pointer;
    outline: none;
    transition: border-color 0.15s ease;
  }
  .page-size-select:focus {
    border-color: var(--accent);
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
