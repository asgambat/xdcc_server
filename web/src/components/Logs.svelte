<script>
  import { onMount, onDestroy } from 'svelte';
  import { sseClient, SystemAPI } from '../lib/api.js';

  // ---- Configuration ----
  const MAX_LINES = 10000;
  const AUTO_SCROLL_THRESHOLD = 50; // px from bottom to resume auto-scroll

  // ---- Log buffer (circular array) ----
  let logs = $state([]);
  let logCount = $state(0);
  let seenTimestamps = new Set(); // deduplicate SSE replay vs REST fetch

  // ---- Filter & count ----
  let filterText = $state('');
  let requestCount = $state(100);

  // ---- Derived: filtered logs ----
  let displayedLogs = $derived(
    filterText.trim() === ''
      ? logs
      : logs.filter(entry => {
          const lower = filterText.toLowerCase();
          return entry.message.toLowerCase().includes(lower)
              || entry.level.toLowerCase().includes(lower);
        })
  );

  // ---- Auto-scroll state ----
  let containerEl;
  let autoScroll = $state(true);
  let paused = $state(false);
  let unsubLogEntry;

  // ---- Level colors ----
  const levelStyles = {
    'DEBUG': 'color: var(--text-muted)',
    'INFO':  'color: var(--text-primary)',
    'WARN':  'color: #f59e0b',
    'ERROR': 'color: #ef4444',
  };

  function formatTime(ts) {
    if (!ts) return '';
    const d = new Date(ts);
    return d.toLocaleTimeString('it-IT', { hour12: false });
  }

  function addLog(entry) {
    // Deduplicate: skip if we've already added a log with the same timestamp
    if (seenTimestamps.has(entry.timestamp)) return;
    seenTimestamps.add(entry.timestamp);
    // Keep set bounded
    if (seenTimestamps.size > MAX_LINES) {
      // Evict oldest ~10% when set grows too large
      const arr = [...seenTimestamps];
      seenTimestamps = new Set(arr.slice(-Math.floor(MAX_LINES * 0.9)));
    }
    logs.push(entry);
    logCount++;
    // Evict oldest entries if over limit
    while (logs.length > MAX_LINES) {
      logs.shift();
    }
  }

  function scrollToBottom() {
    if (containerEl) {
      // Use requestAnimationFrame to ensure DOM is updated
      requestAnimationFrame(() => {
        containerEl.scrollTop = containerEl.scrollHeight;
      });
    }
  }

  function handleScroll() {
    if (!containerEl) return;
    const distFromBottom = containerEl.scrollHeight - containerEl.scrollTop - containerEl.clientHeight;
    const wasPaused = paused;

    if (distFromBottom <= AUTO_SCROLL_THRESHOLD) {
      autoScroll = true;
      paused = false;
    } else {
      autoScroll = false;
      paused = true;
    }
  }

  function togglePause() {
    paused = !paused;
    if (!paused) {
      autoScroll = true;
      scrollToBottom();
    }
  }

  function clearLogs() {
    logs = [];
    logCount = 0;
  }

  async function fetchLogs(count) {
    try {
      const resp = await SystemAPI.logs(count);
      if (resp.logs && resp.logs.length > 0) {
        // Replace current buffer with fetched logs
        logs = resp.logs.map(e => ({
          timestamp: e.timestamp,
          level:     e.level || 'INFO',
          message:   e.message || '',
        }));
        logCount = logs.length;
        // Reset dedup set and populate from fetched entries to prevent
        // SSE replay from creating duplicates after a manual fetch.
        seenTimestamps = new Set(logs.map(e => e.timestamp));
        scrollToBottom();
      }
    } catch (e) {
      console.warn('Failed to fetch logs:', e);
    }
  }

  function handleRequestCountSubmit(e) {
    e.preventDefault();
    fetchLogs(requestCount);
  }

  onMount(async () => {
    // Fetch initial log entries
    await fetchLogs(requestCount);

    // Subscribe to log_entry SSE events for live updates
    unsubLogEntry = sseClient.on('log_entry', (data) => {
      addLog({
        timestamp: data.timestamp,
        level:     data.level || 'INFO',
        message:   data.message || '',
      });

      // Auto-scroll if enabled
      if (autoScroll) {
        scrollToBottom();
      }
    });
  });

  onDestroy(() => {
    if (unsubLogEntry) unsubLogEntry();
  });
</script>

<div class="log-viewer">
  <div class="log-toolbar">
    <div class="log-toolbar-left">
      <span class="log-title">📜 Server Logs</span>
      <span class="log-count">{logCount.toLocaleString()} lines</span>
    </div>
    <div class="log-toolbar-center">
      <input
        type="text"
        class="log-filter-input"
        placeholder="Filter logs..."
        bind:value={filterText}
      />
      {#if filterText}
        <span class="log-filter-count">{displayedLogs.length}/{logCount}</span>
      {/if}
      <form class="log-fetch-form" onsubmit={handleRequestCountSubmit}>
        <input
          type="number"
          class="log-count-input"
          min="10"
          max="2000"
          step="10"
          bind:value={requestCount}
          title="Number of log lines to fetch"
        />
        <button type="submit" class="btn btn-sm btn-ghost" title="Fetch latest N log lines">↻ Fetch</button>
      </form>
    </div>
    <div class="log-toolbar-right">
      <button class="btn btn-sm" class:btn-warning={paused} class:btn-ghost={!paused}
              onclick={togglePause} title={paused ? 'Resume auto-scroll' : 'Pause auto-scroll'}>
        {paused ? '▶ Resume' : '⏸ Pause'}
      </button>
      <button class="btn btn-sm btn-ghost" onclick={clearLogs} title="Clear logs">🗑️ Clear</button>
      <button class="btn btn-sm btn-ghost" onclick={scrollToBottom} title="Scroll to bottom">⬇ Bottom</button>
    </div>
  </div>

  <div
    class="log-container"
    bind:this={containerEl}
    onscroll={handleScroll}
    role="log"
    aria-label="Server logs"
    aria-live="polite"
  >
    {#if logs.length === 0}
      <div class="log-empty">Waiting for log entries...</div>
    {:else if displayedLogs.length === 0}
      <div class="log-empty">No log entries match filter "{filterText}"</div>
    {:else}
      {#each displayedLogs as entry, i (i)}
        <div class="log-line">
          <span class="log-time">{formatTime(entry.timestamp)}</span>
          <span class="log-level" style={levelStyles[entry.level] || ''}>[{entry.level}]</span>
          <span class="log-msg">{entry.message}</span>
        </div>
      {/each}
    {/if}
  </div>

  {#if paused}
    <div
      class="log-pause-indicator"
      role="button"
      tabindex="0"
      onclick={togglePause}
      onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && togglePause()}
    >
      ⏸ Scroll paused — click or press Enter/Space to resume
    </div>
  {/if}
</div>

<style>
  .log-viewer {
    display: flex;
    flex-direction: column;
    height: calc(100vh - var(--header-height) - 8rem);
    min-height: 400px;
    border: 1px solid var(--border-color);
    border-radius: 8px;
    overflow: hidden;
    background: var(--bg-primary);
  }

  .log-toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.5rem 0.75rem;
    background: var(--bg-secondary);
    border-bottom: 1px solid var(--border-color);
    flex-shrink: 0;
  }

  .log-toolbar-left {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .log-toolbar-center {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .log-filter-input {
    width: 180px;
    padding: 0.25rem 0.5rem;
    font-size: 0.75rem;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--bg-primary);
    color: var(--text-primary);
    outline: none;
    transition: border-color 0.15s;
  }

  .log-filter-input:focus {
    border-color: var(--accent);
  }

  .log-filter-input::placeholder {
    color: var(--text-muted);
  }

  .log-filter-count {
    font-size: 0.7rem;
    color: var(--text-muted);
    white-space: nowrap;
  }

  .log-fetch-form {
    display: flex;
    align-items: center;
    gap: 0.25rem;
  }

  .log-count-input {
    width: 60px;
    padding: 0.25rem 0.35rem;
    font-size: 0.75rem;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--bg-primary);
    color: var(--text-primary);
    outline: none;
    text-align: center;
    transition: border-color 0.15s;
    /* Hide spinner for cleaner look */
    -webkit-appearance: none;
    appearance: none;
  }

  .log-count-input::-webkit-inner-spin-button,
  .log-count-input::-webkit-outer-spin-button {
    -webkit-appearance: none;
    margin: 0;
  }

  .log-count-input:focus {
    border-color: var(--accent);
  }

  .log-toolbar-right {
    display: flex;
    gap: 0.25rem;
  }

  .log-title {
    font-weight: 600;
    font-size: 0.9rem;
  }

  .log-count {
    font-size: 0.75rem;
    color: var(--text-muted);
    background: var(--bg-hover);
    padding: 0.15rem 0.5rem;
    border-radius: 999px;
  }

  .log-container {
    flex: 1;
    overflow-y: auto;
    overflow-x: hidden;
    padding: 0.5rem 0;
    font-family: 'JetBrains Mono', 'Fira Code', 'Cascadia Code', 'Consolas', 'Monaco', monospace;
    font-size: 0.8rem;
    line-height: 1.6;
    background: #0d1117;
    color: #c9d1d9;
    scroll-behavior: auto;
  }

  .log-empty {
    display: flex;
    align-items: center;
    justify-content: center;
    height: 100%;
    color: var(--text-muted);
    font-size: 0.85rem;
  }

  .log-line {
    display: flex;
    gap: 0.5rem;
    padding: 0 0.75rem;
    white-space: pre-wrap;
    word-break: break-all;
  }

  .log-line:hover {
    background: rgba(255, 255, 255, 0.03);
  }

  .log-time {
    color: #6e7681;
    flex-shrink: 0;
    min-width: 5.5rem;
  }

  .log-level {
    flex-shrink: 0;
    min-width: 4.5rem;
    font-weight: 600;
    font-size: 0.75rem;
  }

  .log-msg {
    color: #c9d1d9;
  }

  .log-pause-indicator {
    background: #f59e0b;
    color: #000;
    text-align: center;
    padding: 0.35rem;
    font-size: 0.8rem;
    font-weight: 600;
    cursor: pointer;
    flex-shrink: 0;
    transition: background 0.15s;
  }

  .log-pause-indicator:hover {
    background: #d97706;
  }

  /* Dark-adapted scrollbar for log container */
  .log-container::-webkit-scrollbar {
    width: 8px;
  }
  .log-container::-webkit-scrollbar-track {
    background: transparent;
  }
  .log-container::-webkit-scrollbar-thumb {
    background: #30363d;
    border-radius: 4px;
  }
  .log-container::-webkit-scrollbar-thumb:hover {
    background: #484f58;
  }

  /* Override button sizes inside toolbar */
  .log-toolbar :global(.btn-sm) {
    font-size: 0.75rem;
    padding: 0.25rem 0.6rem;
  }
</style>
