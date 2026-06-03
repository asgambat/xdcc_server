<script>
  import { onMount } from 'svelte';
  import { config, theme } from '../lib/stores.js';
  import { SystemAPI } from '../lib/api.js';
  import { addToast } from '../lib/stores.js';
  import Modal from './Modal.svelte';

  let loading = $state(true);
  let configLoadFailed = $state(false);
  let loadingConfig = $state(false); // prevents concurrent loadConfig() calls
  let activeTab = $state('form'); // 'form' | 'advanced'

  // ── Form tab state ──
  let editing = $state(false);
  let formIrcNick = $state('');
  let formHttpPort = $state(8080);
  let formBindAddr = $state('127.0.0.1');
  let formMaxParallel = $state(5);
  let formMaxRateBPS = $state(0);
  let formConflictPolicy = $state('skip');
  let formFailFallback = $state('suggest_only');
  let formDestDir = $state('./downloads/complete');
  let formTempDir = $state('./downloads/tmp');
  let formLogLevel = $state('info');
  let formThemeDark = $state(true);

  // ── Advanced tab state ──
  let yamlContent = $state('');
  let yamlDirty = $state(false);

  // ── Other state ──
  let exportData = $state('');
  let showExportModal = $state(false);

  onMount(async () => {
    console.log('[Settings] onMount called, loading config...');
    await loadConfig();
    loading = false;
    console.log('[Settings] onMount complete, $config:', $config ? 'set' : 'null', 'formIrcNick:', formIrcNick);
  });

  function populateFormFromConfig(cfg) {
    console.log('[Settings] populateFormFromConfig called, cfg:', cfg ? 'set' : 'null/undefined');
    formIrcNick = cfg?.irc?.nickname || 'xdcc-user';
    formHttpPort = cfg?.http?.port || 8080;
    formBindAddr = cfg?.http?.bind_address || '127.0.0.1';
    formMaxParallel = cfg?.download?.max_parallel_total ?? 5;
    formMaxRateBPS = cfg?.download?.max_rate_bps ?? 0;
    formConflictPolicy = cfg?.download?.conflict_policy || 'skip';
    formFailFallback = cfg?.download?.fail_fallback || 'suggest_only';
    formDestDir = cfg?.download?.dest_dir || './downloads/complete';
    formTempDir = cfg?.download?.temp_dir || './downloads/tmp';
    formLogLevel = cfg?.logging?.level || 'info';
    formThemeDark = cfg?.ui?.theme !== 'light';
  }

  async function loadConfig() {
    console.log('[Settings] loadConfig called, current $config:', $config ? 'set' : 'null');
    try {
      console.log('[Settings] Calling SystemAPI.config()...');
      const cfg = await SystemAPI.config();
      console.log('[Settings] SystemAPI.config() returned:', cfg ? typeof cfg : 'null/undefined', cfg ? JSON.stringify(cfg).substring(0, 200) : 'N/A');
      // Accept any truthy object returned from the API
      if (cfg && typeof cfg === 'object') {
        console.log('[Settings] Setting config store and populating form');
        config.set(cfg);
        populateFormFromConfig(cfg);
        configLoadFailed = false;
        console.log('[Settings] After set: $config.irc.nickname =', $config?.irc?.nickname);
      } else {
        console.warn('[Settings] loadConfig returned non-object cfg:', cfg);
        configLoadFailed = true;
        addToast('Failed to load configuration — invalid response from server', 'error');
      }
    } catch (e) {
      console.error('[Settings] loadConfig failed:', e.message);
      configLoadFailed = true;
      addToast(`Failed to load configuration: ${e.message}`, 'error');
    }
  }

  async function loadRawYAML() {
    try {
      yamlContent = await SystemAPI.rawConfig();
      yamlDirty = false;
    } catch (e) { addToast(`Failed to load raw config: ${e.message}`, 'error'); }
  }

  // ── Form tab actions ──
  function startEdit() {
    populateFormFromConfig($config);
    editing = true;
  }

  async function saveForm() {
    try {
      // Build full config by merging form edits into the current config.
      // The backend unmarshals into a blank Config struct, so all required
      // fields must be present.
      const themeStr = formThemeDark ? 'dark' : 'light';
      const current = $config || {};
      const fullConfig = {
        ...current,
        irc: { ...current.irc, nickname: formIrcNick },
        http: { ...current.http, port: formHttpPort, bind_address: formBindAddr },
        download: {
          ...current.download,
          max_parallel_total: formMaxParallel,
          max_rate_bps: formMaxRateBPS,
          conflict_policy: formConflictPolicy,
          fail_fallback: formFailFallback,
          dest_dir: formDestDir,
          temp_dir: formTempDir,
        },
        logging: { ...current.logging, level: formLogLevel },
        ui: { ...current.ui, theme: themeStr },
      };
      await SystemAPI.updateConfig(fullConfig);
      config.set(fullConfig);
      editing = false;
      addToast('Config saved', 'success');
    } catch (e) { addToast(`Save failed: ${e.message}`, 'error'); }
  }

  // ── Advanced tab actions ──
  function onYAMLInput() { yamlDirty = true; }

  async function saveYAML() {
    try {
      await SystemAPI.updateRawConfig(yamlContent);
      yamlDirty = false;
      // Reload the structured config to keep stores in sync
      await loadConfig();
      addToast('Config saved (with .bak backup)', 'success');
    } catch (e) { addToast(`Save failed: ${e.message}`, 'error'); }
  }

  // ── Theme toggle ──
  async function toggleTheme() {
    const newTheme = $theme === 'dark' ? 'light' : 'dark';
    theme.set(newTheme);
    formThemeDark = newTheme === 'dark';
    localStorage.setItem('xdcc-theme', newTheme);
    document.documentElement.setAttribute('data-theme', newTheme);
    try {
      // Use the dedicated theme endpoint — does not validate the full config.
      await SystemAPI.updateTheme(newTheme);
    } catch (e) {
      addToast(`Theme saved locally but failed to persist: ${e.message}`, 'warning');
    }
  }

  // ── Data management ──
  async function doExport() {
    try {
      const data = await SystemAPI.exportData();
      exportData = JSON.stringify(data, null, 2);
      showExportModal = true;
      addToast('Export ready', 'success');
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function doImport() {
    const text = prompt('Paste JSON export data:');
    if (!text) return;
    try {
      const parsed = JSON.parse(text);
      await SystemAPI.importData(parsed);
      addToast('Import successful', 'success');
    } catch (e) { addToast(`Import failed: ${e.message}`, 'error'); }
  }

  async function resetSetup() {
    if (!confirm('This will reset the application setup. Continue?')) return;
    try {
      await SystemAPI.bootstrap({ address: 'irc.rizon.net', port: 6667, channels: ['#linux'] });
      addToast('Setup reset complete', 'success');
    } catch (e) { addToast(e.message, 'error'); }
  }
</script>

{#if loading}
  <div class="spinner"></div>
{:else if configLoadFailed}
  <div class="card">
    <div class="card-header"><span class="card-title">⚠️ Configuration Unavailable</span></div>
    <p class="text-sm text-muted mb-1">
      Could not load the current configuration. This usually means you need to authenticate with an admin token.
    </p>
    <p class="text-sm text-muted mb-1">
      You can still use the Advanced (YAML) tab to view and edit the raw config file directly.
    </p>
    <button class="btn" onclick={loadConfig}>↻ Try Again</button>
  </div>
{:else}
  <div class="card mb-2">
    <div class="card-header"><span class="card-title">🎨 Appearance</span></div>
    <div class="flex gap-1" style="align-items:center">
      <span class="text-sm">Theme:</span>
      <button class="btn" onclick={toggleTheme}>
        {$theme === 'dark' ? '☀️ Light Mode' : '🌙 Dark Mode'}
      </button>
    </div>
  </div>

  <!-- ── Configuration tabs ── -->
  <div class="card mb-2">
    <div class="card-header">
      <span class="card-title">⚙️ Configuration</span>
      <div class="btn-group">
        {#if editing}
          <button class="btn btn-sm btn-primary" onclick={saveForm}>Save</button>
          <button class="btn btn-sm btn-ghost" onclick={() => { editing = false; loadConfig(); }}>Cancel</button>
        {:else}
          <button class="btn btn-sm btn-primary" onclick={startEdit}>Edit</button>
          <button class="btn btn-sm btn-ghost" onclick={loadConfig}>Refresh</button>
        {/if}
      </div>
    </div>

    <!-- Tab selector -->
    <div class="tab-bar">
      <button class="tab-btn" class:active={activeTab === 'form'} onclick={() => { activeTab = 'form'; if (!$config && !loadingConfig) { loadingConfig = true; loadConfig().finally(() => loadingConfig = false); } }}>📋 Form</button>
      <button class="tab-btn" class:active={activeTab === 'advanced'} onclick={() => { activeTab = 'advanced'; if (!yamlContent) loadRawYAML(); }}>📝 Advanced (YAML)</button>
    </div>

    <!-- ── Tab: Form ── -->
    {#if activeTab === 'form'}
      <div class="tab-content">
        {#if editing}
          <div class="form-grid">
            <div class="form-group">
              <label class="form-label" for="f-irc-nick">IRC Nickname</label>
              <input class="form-input" id="f-irc-nick" bind:value={formIrcNick} placeholder="xdcc-user" />
            </div>
            <div class="form-group">
              <label class="form-label" for="f-http-port">HTTP Port</label>
              <input class="form-input" id="f-http-port" type="number" min="1" max="65535" bind:value={formHttpPort} />
            </div>
            <div class="form-group">
              <label class="form-label" for="f-bind-addr">Bind Address</label>
              <input class="form-input" id="f-bind-addr" bind:value={formBindAddr} placeholder="127.0.0.1" />
            </div>
            <div class="form-group">
              <label class="form-label" for="f-max-parallel">Max Parallel Downloads</label>
              <input class="form-input" id="f-max-parallel" type="number" min="1" max="50" bind:value={formMaxParallel} />
            </div>
            <div class="form-group">
              <label class="form-label" for="f-max-rate">Max Download Rate (B/s)</label>
              <input class="form-input" id="f-max-rate" type="number" min="0" bind:value={formMaxRateBPS} placeholder="0 = unlimited" />
            </div>
            <div class="form-group">
              <label class="form-label" for="f-conflict">Conflict Policy</label>
              <select class="form-input" id="f-conflict" bind:value={formConflictPolicy}>
                <option value="skip">Skip</option>
                <option value="overwrite">Overwrite</option>
                <option value="rename">Rename</option>
              </select>
            </div>
            <div class="form-group">
              <label class="form-label" for="f-fallback">Fail Fallback</label>
              <select class="form-input" id="f-fallback" bind:value={formFailFallback}>
                <option value="suggest_only">Suggest Only</option>
                <option value="auto_retry_best">Auto Retry Best</option>
              </select>
            </div>
            <div class="form-group">
              <label class="form-label" for="f-dest">Download Dest Dir</label>
              <input class="form-input" id="f-dest" bind:value={formDestDir} placeholder="./downloads/complete" />
            </div>
            <div class="form-group">
              <label class="form-label" for="f-tmp">Download Temp Dir</label>
              <input class="form-input" id="f-tmp" bind:value={formTempDir} placeholder="./downloads/tmp" />
            </div>
            <div class="form-group">
              <label class="form-label" for="f-loglevel">Log Level</label>
              <select class="form-input" id="f-loglevel" bind:value={formLogLevel}>
                <option value="debug">Debug</option>
                <option value="info">Info</option>
                <option value="warn">Warn</option>
                <option value="error">Error</option>
              </select>
            </div>
          </div>
        {:else}
          <!-- Display mode: use reactive config store values directly, fall back to form state if available -->
          <div class="form-grid">
            <div class="form-group"><span class="form-label">IRC Nickname</span><span class="text-sm">{$config?.irc?.nickname || formIrcNick || '—'}</span></div>
            <div class="form-group"><span class="form-label">HTTP Port</span><span class="text-sm">{$config?.http?.port ?? formHttpPort ?? '—'}</span></div>
            <div class="form-group"><span class="form-label">Bind Address</span><span class="text-sm">{$config?.http?.bind_address || formBindAddr || '—'}</span></div>
            <div class="form-group"><span class="form-label">Max Parallel</span><span class="text-sm">{$config?.download?.max_parallel_total ?? formMaxParallel ?? '—'}</span></div>
            <div class="form-group"><span class="form-label">Max Rate (B/s)</span><span class="text-sm">{$config?.download?.max_rate_bps ?? formMaxRateBPS ?? '—'}</span></div>
            <div class="form-group"><span class="form-label">Conflict Policy</span><span class="text-sm">{$config?.download?.conflict_policy || formConflictPolicy || '—'}</span></div>
            <div class="form-group"><span class="form-label">Fail Fallback</span><span class="text-sm">{$config?.download?.fail_fallback || formFailFallback || '—'}</span></div>
            <div class="form-group"><span class="form-label">Dest Dir</span><span class="text-sm">{$config?.download?.dest_dir || formDestDir || '—'}</span></div>
            <div class="form-group"><span class="form-label">Temp Dir</span><span class="text-sm">{$config?.download?.temp_dir || formTempDir || '—'}</span></div>
            <div class="form-group"><span class="form-label">Log Level</span><span class="text-sm">{$config?.logging?.level || formLogLevel || '—'}</span></div>
          </div>
        {/if}
      </div>
    {:else}
      <!-- ── Tab: Advanced (YAML) ── -->
      <div class="tab-content">
        <p class="text-xs text-muted mb-1">
          ⚠️ Edit the raw <code>config.yaml</code> directly. A <code>.bak</code> backup is created automatically before each save.
          Syntax errors are caught before writing to disk.
        </p>
        <textarea
          class="form-input yaml-editor"
          bind:value={yamlContent}
          oninput={onYAMLInput}
          spellcheck="false"
          placeholder="Loading raw config..."
        ></textarea>
        <div class="flex" style="justify-content:flex-end;margin-top:0.5rem">
          <button class="btn btn-sm btn-ghost" onclick={loadRawYAML}>↻ Reload</button>
          <button class="btn btn-sm btn-primary" onclick={saveYAML} disabled={!yamlDirty}>
            💾 Save to disk
          </button>
        </div>
      </div>
    {/if}
  </div>

  <!-- ── Data Management ── -->
  <div class="card mb-2">
    <div class="card-header"><span class="card-title">📋 Data Management</span></div>
    <div class="btn-group">
      <button class="btn btn-sm btn-primary" onclick={doExport}>📤 Export</button>
      <button class="btn btn-sm btn-warning" onclick={doImport}>📥 Import</button>
    </div>
  </div>

  <Modal title="Export Data" visible={showExportModal} on:close={() => showExportModal = false}>
    <pre style="max-height:300px;overflow:auto;background:var(--bg-input);padding:0.75rem;border-radius:var(--radius);font-size:0.8rem">{exportData}</pre>
    <div class="modal-actions">
      <button class="btn btn-sm btn-primary" onclick={() => navigator.clipboard.writeText(exportData)}>Copy to Clipboard</button>
    </div>
  </Modal>

  <!-- ── Danger Zone ── -->
  <div class="card">
    <div class="card-header"><span class="card-title">⚠️ Danger Zone</span></div>
    <p class="text-sm text-muted mb-1">Reset the application to initial setup state. This will clear servers and configurations.</p>
    <button class="btn btn-sm btn-danger" onclick={resetSetup}>🔄 Reset Setup</button>
  </div>
{/if}

<style>
  .tab-bar {
    display: flex;
    border-bottom: 1px solid var(--border-color);
    margin-bottom: 0.75rem;
  }
  .tab-btn {
    background: none;
    border: none;
    padding: 0.5rem 1rem;
    cursor: pointer;
    font-size: 0.85rem;
    color: var(--text-secondary);
    border-bottom: 2px solid transparent;
    transition: all 0.15s;
  }
  .tab-btn:hover { color: var(--text-primary); }
  .tab-btn.active {
    color: var(--color-primary);
    border-bottom-color: var(--color-primary);
  }
  .tab-content { padding: 0; }
  .form-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.75rem;
  }
  @media (max-width: 600px) { .form-grid { grid-template-columns: 1fr; } }
  .form-group { display: flex; flex-direction: column; gap: 0.25rem; }
  .form-label { font-size: 0.75rem; font-weight: 600; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.05em; }
  .yaml-editor {
    min-height: 400px;
    font-family: 'SF Mono', 'Fira Code', 'Cascadia Code', monospace;
    font-size: 0.8rem;
    line-height: 1.5;
    tab-size: 2;
    resize: vertical;
  }
</style>
