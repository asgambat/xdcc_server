<script>
  import { onMount } from 'svelte';
  import { presets, watchlists, addToast, navigateToSearch } from '../lib/stores.js';
  import { PresetsAPI, WatchlistsAPI } from '../lib/api.js';
  import Modal from './Modal.svelte';

  let loading = $state(true);
  let editingId = $state(null);
  let form = $state({ name: '', query: '', providers: [], min_size: '', max_size: '' });

  // --- Create watchlist from preset modal state ---
  let showWlModal = $state(false);
  let wlPreset = $state(null);
  let wlForm = $state({ name: '', query: '', interval_minutes: 60, min_size: '', max_size: '', notify_enabled: true, auto_enqueue: false, enabled: true });

  onMount(async () => { await load(); loading = false; });

  async function load() {
    try { presets.set(await PresetsAPI.list()); } catch {}
  }

  function resetForm() { form = { name: '', query: '', providers: [], min_size: '', max_size: '' }; editingId = null; }

  function startEdit(preset) {
    editingId = preset.id;
    form = {
      name: preset.name || '',
      query: preset.query || '',
      providers: preset.providers || [],
      min_size: preset.min_size || '',
      max_size: preset.max_size || '',
    };
  }

  async function save() {
    if (!form.name.trim()) return addToast('Enter a name', 'warning');
    const payload = {
      name: form.name.trim(),
      query: form.query.trim(),
      providers: form.providers,
      min_size: form.min_size,
      max_size: form.max_size,
    };
    try {
      if (editingId) {
        await PresetsAPI.update(editingId, payload);
        addToast('Preset updated', 'success');
      } else {
        await PresetsAPI.create(payload);
        addToast('Preset created', 'success');
      }
      resetForm();
      await load();
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function remove(id) {
    try { await PresetsAPI.remove(id); addToast('Preset removed', 'info'); await load(); }
    catch (e) { addToast(e.message, 'error'); }
  }

  async function applyPreset(preset) {
    navigateToSearch(preset.query, String(preset.min_size || ''), String(preset.max_size || ''));
    addToast('Preset applied to search', 'success');
  }

  async function loadWatchlists() {
    try { watchlists.set(await WatchlistsAPI.list()); } catch {}
  }

  function openWlModal(preset) {
    wlPreset = preset;
    wlForm = {
      name: `${preset.name} Watchlist`,
      query: preset.query,
      interval_minutes: 60,
      min_size: preset.min_size || '',
      max_size: preset.max_size || '',
      notify_enabled: true,
      auto_enqueue: false,
      enabled: true,
    };
    showWlModal = true;
  }

  async function createWatchlistFromModal() {
    const { name, query, interval_minutes, min_size, max_size, notify_enabled, auto_enqueue, enabled } = wlForm;
    const trimmedName = name?.trim();
    const trimmedQuery = query?.trim();
    if (!trimmedName || !trimmedQuery) {
      addToast('Name and query are required', 'warning');
      return;
    }
    try {
      await WatchlistsAPI.create({
        name: trimmedName,
        query: trimmedQuery,
        // @ts-ignore - svelte-check type inference conflict
        interval_minutes: parseInt(interval_minutes) || 60,
        min_size: min_size?.trim() || '',
        max_size: max_size?.trim() || '',
        notify_enabled,
        auto_enqueue,
        enabled,
      });
      addToast(`Watchlist "${trimmedName}" created from preset`, 'success');
      showWlModal = false;
      await loadWatchlists();
    } catch (e) {
      addToast(e.message, 'error');
    }
  }
</script>

<Modal title={`Create Watchlist from "${wlPreset?.name || ''}"`} visible={showWlModal} on:close={() => showWlModal = false}>
  <div class="form-group">
    <label class="form-label" for="wl-modal-name">Name</label>
    <input id="wl-modal-name" class="form-input" bind:value={wlForm.name} placeholder="e.g. Ubuntu ISOs Monitor" />
  </div>
  <div class="form-group">
    <label class="form-label" for="wl-modal-query">Query</label>
    <input id="wl-modal-query" class="form-input" bind:value={wlForm.query} placeholder="e.g. Ubuntu 24.04" />
  </div>
  <div class="form-row">
    <div class="form-group">
      <label class="form-label" for="wl-modal-interval">Interval (minutes)</label>
      <input id="wl-modal-interval" class="form-input" type="number" min="5" bind:value={wlForm.interval_minutes} />
    </div>
    <div class="form-group">
      <label class="form-label" for="wl-modal-min">Min Size</label>
      <input id="wl-modal-min" class="form-input" bind:value={wlForm.min_size} placeholder="optional" />
    </div>
    <div class="form-group">
      <label class="form-label" for="wl-modal-max">Max Size</label>
      <input id="wl-modal-max" class="form-input" bind:value={wlForm.max_size} placeholder="optional" />
    </div>
  </div>
  <div class="flex gap-2 mb-1" style="align-items:center">
    <label style="display:flex;align-items:center;gap:0.4rem;cursor:pointer">
      <input type="checkbox" bind:checked={wlForm.notify_enabled} /> Notify on new results
    </label>
    <label style="display:flex;align-items:center;gap:0.4rem;cursor:pointer">
      <input type="checkbox" bind:checked={wlForm.auto_enqueue} /> Auto-enqueue
    </label>
    <label style="display:flex;align-items:center;gap:0.4rem;cursor:pointer">
      <input type="checkbox" bind:checked={wlForm.enabled} /> Enabled
    </label>
  </div>
  <div class="modal-actions">
    <button class="btn btn-ghost" onclick={() => showWlModal = false}>Cancel</button>
    <button class="btn btn-primary" onclick={createWatchlistFromModal}>Create Watchlist</button>
  </div>
</Modal>

{#if loading}
  <div class="spinner"></div>
{:else}
  <div class="card mb-2">
    <div class="card-header">
      <span class="card-title">{editingId ? 'Edit Preset' : 'Create Preset'}</span>
    </div>
    <div class="form-row">
      <div class="form-group">
        <label class="form-label" for="preset-name">Name</label>
        <input id="preset-name" class="form-input" bind:value={form.name} placeholder="e.g. Ubuntu ISOs" />
      </div>
      <div class="form-group">
        <label class="form-label" for="preset-query">Query</label>
        <input id="preset-query" class="form-input" bind:value={form.query} placeholder="e.g. Ubuntu 24.04" />
      </div>
    </div>
    <div class="form-row">
      <div class="form-group">
        <label class="form-label" for="preset-min">Min Size</label>
        <input id="preset-min" class="form-input" bind:value={form.min_size} placeholder="optional" />
      </div>
      <div class="form-group">
        <label class="form-label" for="preset-max">Max Size</label>
        <input id="preset-max" class="form-input" bind:value={form.max_size} placeholder="optional" />
      </div>
    </div>
    <div class="btn-group">
      <button class="btn btn-primary" onclick={save}>{editingId ? 'Update' : 'Create'}</button>
      {#if editingId}<button class="btn btn-ghost" onclick={resetForm}>Cancel</button>{/if}
    </div>
  </div>

  {#if $presets.length > 0}
    <div class="table-container">
      <table>
        <thead><tr><th>Name</th><th>Query</th><th>Filters</th><th>Actions</th></tr></thead>
        <tbody>
          {#each $presets as p}
            <tr>
              <td><strong>{p.name}</strong></td>
              <td class="text-sm"><code>{p.query}</code></td>
              <td class="text-sm text-muted">
                {#if p.min_size}≥{p.min_size}{/if}
                {#if p.max_size} ≤{p.max_size}{/if}
                {#if !p.min_size && !p.max_size}none{/if}
              </td>
              <td>
                <div class="btn-group">
                  <button class="btn btn-sm btn-primary" onclick={() => applyPreset(p)}>🔍 Search</button>
                  <button class="btn btn-sm btn-ghost" onclick={() => startEdit(p)}>✏️</button>
                  <button class="btn btn-sm btn-ghost" onclick={() => openWlModal(p)} title="Create watchlist from preset">👁️</button>
                  <button class="btn btn-sm btn-ghost" onclick={() => remove(p.id)}>🗑️</button>
                </div>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else}
    <div class="empty-state">
      <div class="empty-state-text">No presets yet</div>
      <div class="empty-state-sub">Create search presets to quickly search for common queries</div>
    </div>
  {/if}
{/if}
