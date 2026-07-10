<script>
  let { items = [], selectedDownloads, toggleDownload, toggleSelectAll, formatBytes, formatSpeed, formatETA, statusBadge, showSelect = true, onPause, onResume, onRetry, onRemove, onMoveUp, onMoveDown } = $props();

  function handlePause(id) { onPause?.(id); }
  function handleResume(id) { onResume?.(id); }
  function handleRetry(id) { onRetry?.(id); }
  function handleRemove(id) { onRemove?.(id); }
  function handleMoveUp(id) { onMoveUp?.(id); }
  function handleMoveDown(id) { onMoveDown?.(id); }
</script>

<style>
  .download-card {
    background: var(--bg-card);
    border: 1px solid var(--border-color);
    border-radius: var(--radius);
    padding: 1rem;
    margin-bottom: 0.75rem;
  }
  .card-row { display: flex; justify-content: space-between; align-items: center; margin-bottom: 0.5rem; }
  .card-row:last-child { margin-bottom: 0; }
  .mobile-only { display: none; }

  @media (max-width: 600px) {
    .table-container table { display: none; }
    .mobile-only { display: block; }
  }
  @media (max-width: 400px) {
    .download-card { padding: 0.75rem; }
  }
</style>

{#if items.length > 0}
  <div class="table-container">
    <table>
      <thead>
        <tr>
          {#if showSelect}
            <th class="checkbox-cell">
              <input type="checkbox" onchange={toggleSelectAll} checked={items.length > 0 && items.every(d => selectedDownloads?.has(d.id))} />
            </th>
          {/if}
          <th>File</th>
          <th>Bot</th>
          <th>Size</th>
          <th>Progress</th>
          <th>Speed</th>
          <th>ETA</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody>
        {#each items as item (item.id)}
          <tr>
            {#if showSelect}
              <td class="checkbox-cell">
                <input type="checkbox" checked={selectedDownloads?.has(item.id)} onchange={() => toggleDownload(item.id)} />
              </td>
            {/if}
            <td class="truncate" style="max-width:200px" title={item.filename}>{item.filename || 'Unknown'}</td>
            <td>{item.bot || '—'}</td>
            <td class="text-sm">{formatBytes(item.file_size)}</td>
            <td style="min-width:120px">
              {#if item.status === 'downloading'}
                <div class="text-sm" style="display:flex;justify-content:space-between">
                  <span>{formatBytes(item.progress_bytes)} / {formatBytes(item.file_size)}</span>
                  <span>{item.file_size > 0 ? Math.round((item.progress_bytes / item.file_size) * 100) : 0}%</span>
                </div>
                <div class="progress-bar">
                  <div class="progress-fill" style="width:{item.file_size > 0 ? Math.min(100, (item.progress_bytes / item.file_size) * 100) : 0}%"></div>
                </div>
              {:else}
                <span class="badge badge-{statusBadge(item.status).cls}"><span class="badge-dot"></span>{item.status}</span>
              {/if}
            </td>
            <td class="text-sm">{formatSpeed(item.speed_bps)}</td>
            <td class="text-sm">{formatETA(item.file_size - item.progress_bytes, item.speed_bps)}</td>
            <td>
              <div class="btn-group">
                {#if item.status === 'queued' || item.status === 'paused'}
                  <button class="btn btn-sm btn-ghost" onclick={() => handleMoveUp(item.id)} title="Move Up">↑</button>
                  <button class="btn btn-sm btn-ghost" onclick={() => handleMoveDown(item.id)} title="Move Down">↓</button>
                {/if}
                {#if item.status === 'downloading'}
                  <button class="btn btn-sm btn-warning" onclick={() => handlePause(item.id)} title="Pause">⏸️</button>
                {/if}
                {#if item.status === 'paused'}
                  <button class="btn btn-sm btn-success" onclick={() => handleResume(item.id)} title="Resume">▶️</button>
                {/if}
                {#if ['failed', 'skipped_existing', 'completed'].includes(item.status)}
                  <button class="btn btn-sm btn-primary" onclick={() => handleRetry(item.id)} title="Retry">🔄</button>
                {/if}
                <button class="btn btn-sm btn-ghost" onclick={() => handleRemove(item.id)} title="Remove">🗑️</button>
              </div>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>

    <div class="mobile-only">
      {#each items as item (item.id)}
        <div class="download-card">
          <div class="card-row">
            <div style="display:flex; align-items:center; gap:0.5rem; min-width:0; overflow:hidden">
              {#if showSelect}
                <input type="checkbox" checked={selectedDownloads?.has(item.id)} onchange={() => toggleDownload(item.id)} />
              {/if}
              <strong class="truncate" style="min-width:0">{item.filename || 'Unknown'}</strong>
            </div>
            <button class="btn btn-sm btn-ghost" onclick={() => handleRemove(item.id)}>🗑️</button>
          </div>
          <div class="card-row">
            <span class="text-sm">Bot: {item.bot || '—'}</span>
            <span class="text-sm">{formatBytes(item.file_size)}</span>
          </div>
          <div style="margin: 0.5rem 0;">
            {#if item.status === 'downloading'}
              <div class="progress-bar">
                <div class="progress-fill" style="width:{item.file_size > 0 ? Math.min(100, (item.progress_bytes / item.file_size) * 100) : 0}%"></div>
              </div>
            {:else}
              <span class="badge badge-{statusBadge(item.status).cls}">{item.status}</span>
            {/if}
          </div>
          <div class="card-row">
            <span class="text-sm">{formatSpeed(item.speed_bps)} | {formatETA(item.file_size - item.progress_bytes, item.speed_bps)}</span>
            <div class="btn-group">
                {#if item.status === 'downloading'}
                  <button class="btn btn-sm btn-warning" onclick={() => handlePause(item.id)}>⏸️</button>
                {:else if item.status === 'paused'}
                  <button class="btn btn-sm btn-success" onclick={() => handleResume(item.id)}>▶️</button>
                {:else if ['failed', 'skipped_existing', 'completed'].includes(item.status)}
                  <button class="btn btn-sm btn-primary" onclick={() => handleRetry(item.id)}>🔄</button>
                {/if}
            </div>
          </div>
        </div>
      {/each}
    </div>
  </div>
{/if}
