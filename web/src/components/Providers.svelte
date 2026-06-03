<script>
  import { onMount } from 'svelte';
  import { providers } from '../lib/stores.js';
  import { ProvidersAPI } from '../lib/api.js';
  import { addToast } from '../lib/stores.js';

  let loading = $state(true);

  onMount(async () => { await load(); loading = false; });

  async function load() {
    try {
      const data = await ProvidersAPI.list();
      // Backend returns { states: [...], insights: [...] }
      // Merge them for display
      const states = data?.states || [];
      const insights = data?.insights || [];
      
      // Merge states with insights by name
      const merged = states.map(state => {
        const insight = insights.find(i => i.name === state.name);
        return {
          name: state.name,
          status: state.status,
          enabled: insight?.enabled !== false,
          latency_ms: state.latency_ms || insight?.avg_latency_ms_24h,
          result_count: insight?.successes_24h || 0,
        };
      });
      
      providers.set(merged);
    } catch (e) {
      console.error('Failed to load providers:', e);
    }
  }

  async function toggleProvider(name, enabled) {
    try {
      await ProvidersAPI.toggle(name, enabled);
      addToast(`${name}: ${enabled ? 'Enabled' : 'Disabled'}`, enabled ? 'success' : 'info');
      await load();
    } catch (e) { addToast(e.message, 'error'); }
  }

  let ok = $derived($providers.filter(p => p.status === 'ok' || p.status === 'healthy'));
  let failing = $derived($providers.filter(p => p.status !== 'ok' && p.status !== 'healthy' && p.status !== 'disabled'));
</script>

{#if loading}
  <div class="spinner"></div>
{:else}
  <div class="stats-grid" style="grid-template-columns:repeat(auto-fit,minmax(150px,1fr))">
    <div class="stat-card">
      <div class="stat-label">Total Providers</div>
      <div class="stat-value">{$providers.length}</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Healthy</div>
      <div class="stat-value success">{ok.length}</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Issues</div>
      <div class="stat-value" class:danger={failing.length > 0}>{failing.length}</div>
    </div>
  </div>

  <div class="card">
    <div class="card-header"><span class="card-title">Provider Status</span></div>
    {#if $providers.length > 0}
      <div class="table-container">
        <table>
          <thead><tr><th>Provider</th><th>Status</th><th>Enabled</th><th>Latency</th><th>Results</th><th>Actions</th></tr></thead>
          <tbody>
            {#each $providers as p}
              <tr>
                <td><strong>{p.name}</strong></td>
                <td>
                  {#if p.enabled === false}
                    <span class="text-muted">—</span>
                  {:else}
                    <span class="badge" class:badge-ok={p.status === 'ok' || p.status === 'healthy'} class:badge-danger={p.status !== 'ok' && p.status !== 'healthy'}>
                      <span class="badge-dot"></span>{p.status || 'unknown'}
                    </span>
                  {/if}
                </td>
                <td>
                  <span class="badge" class:badge-ok={p.enabled !== false} class:badge-warning={p.enabled === false}>
                    {p.enabled !== false ? 'Yes' : 'No'}
                  </span>
                </td>
                <td class="text-sm">{p.latency_ms ? `${p.latency_ms}ms` : '—'}</td>
                <td class="text-sm">{p.result_count || p.total_results || 0}</td>
                <td>
                  <button class="btn btn-sm" class:btn-success={p.enabled === false} class:btn-warning={p.enabled !== false}
                    onclick={() => toggleProvider(p.name, p.enabled === false)}>
                    {p.enabled !== false ? 'Disable' : 'Enable'}
                  </button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else}
      <div class="empty-state">
        <div class="empty-state-text">No providers loaded</div>
      </div>
    {/if}
  </div>
{/if}
