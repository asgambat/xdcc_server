<script>
  import { currentView, downloadsBadge, hasAdminToken, SYSTEM_VIEWS } from '../lib/stores.js';
  import { createEventDispatcher } from 'svelte';

  export let sidebarOpen;
  export let toggleSidebar;

  const dispatch = createEventDispatcher();


  const navItems = [
    { view: 'dashboard',  icon: '📊', label: 'Dashboard', section: 'Overview' },
    { view: 'servers',    icon: '🖥️', label: 'Servers',   section: 'Overview' },
    { view: 'downloads',  icon: '⬇️', label: 'Downloads', section: 'Overview', badge: true },
    { view: 'search',     icon: '🔍', label: 'Search',    section: 'Search' },
    { view: 'presets',    icon: '📋', label: 'Presets',   section: 'Search' },
    { view: 'watchlists', icon: '👁️', label: 'Watchlists',section: 'Search' },
    { view: 'logs',       icon: '📜', label: 'Logs',      section: 'System' },
    { view: 'providers',  icon: '🌐', label: 'Providers', section: 'System' },
    { view: 'settings',   icon: '⚙️', label: 'Settings',  section: 'System' },
  ];

  function navigate(view) {
    // SYSTEM section views require admin token — prompt if missing
    if (SYSTEM_VIEWS.has(view) && !$hasAdminToken) {
      dispatch('requestToken', view);
      return;
    }
    dispatch('navigate', view);
  }
</script>

<aside class="sidebar" class:open={sidebarOpen}>
  <div class="sidebar-header">
    <div class="logo-group">
      <div class="sidebar-logo">⚡</div>
      <div class="sidebar-title">XDCC Manager</div>
    </div>
    <button class="hamburger" onclick={toggleSidebar} aria-label="Close sidebar">✕</button>
  </div>
  <nav class="sidebar-nav">
    {#each navItems as item}
      {#if item.section !== navItems[navItems.indexOf(item) - 1]?.section}
        <div class="nav-section">{item.section}</div>
      {/if}
      <div
        class="nav-item"
        class:active={$currentView === item.view}
        onclick={() => navigate(item.view)}
        role="button"
        tabindex="0"
        onkeydown={(e) => e.key === 'Enter' && navigate(item.view)}
      >
        <span class="nav-icon">{item.icon}</span>
        {item.label}
        {#if item.badge && $downloadsBadge > 0}
          <span class="nav-badge">{$downloadsBadge}</span>
        {/if}
      </div>
    {/each}
  </nav>
  <!-- Keyboard shortcut hints -->
  <div class="sidebar-footer">
    <button class="shortcut-hint" onclick={() => dispatch('requestToken', '')} title="Open admin token prompt">
      <kbd>Ctrl+L</kbd>
      <span class="shortcut-label">Token</span>
    </button>
    <span class="shortcut-hint">
      <kbd>Ctrl+K</kbd>
      <span class="shortcut-label">Search</span>
    </span>
  </div>
</aside>
<div class="sidebar-overlay" class:open={sidebarOpen} onclick={toggleSidebar} role="presentation"></div>

<style>
  .sidebar {
    width: var(--sidebar-width);
    background: var(--bg-secondary);
    border-right: 1px solid var(--border-color);
    display: flex;
    flex-direction: column;
    position: fixed;
    top: 0;
    left: 0;
    height: 100vh;
    z-index: 100;
    transition: transform var(--transition);
  }
  .logo-group {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    flex: 1;
  }
  .sidebar-header {
    padding: 1rem 1.25rem;
    border-bottom: 1px solid var(--border-color);
    display: flex;
    align-items: center;
    min-height: var(--header-height);
  }
  @media (max-width: 768px) {
    .logo-group { display: none; }
    .sidebar-header { justify-content: flex-end; }
  }
  .sidebar-logo {
    width: 32px; height: 32px;
    background: linear-gradient(135deg, var(--accent), #a78bfa);
    border-radius: 8px;
    display: flex; align-items: center; justify-content: center;
    font-size: 1.1rem; flex-shrink: 0;
  }
  .sidebar-title { font-size: 1rem; font-weight: 600; white-space: nowrap; }
  .sidebar-nav { flex: 1; overflow-y: auto; padding: 0.75rem 0; }
  .nav-section {
    padding: 0.5rem 1.25rem 0.25rem;
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--text-muted);
    font-weight: 600;
  }
  .nav-item {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.6rem 1.25rem;
    color: var(--text-secondary);
    cursor: pointer;
    transition: all var(--transition);
    border-left: 3px solid transparent;
    font-size: 0.9rem;
  }
  .nav-item:hover { background: var(--bg-hover); color: var(--text-primary); }
  .nav-item.active { background: var(--accent-glow); color: var(--accent-light); border-left-color: var(--accent); }
  .nav-icon { font-size: 1.1rem; width: 1.5rem; text-align: center; flex-shrink: 0; }
  .nav-badge {
    margin-left: auto;
    background: var(--accent);
    color: white;
    font-size: 0.7rem;
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-weight: 600;
  }
  .hamburger { display: none; background: none; border: none; color: var(--text-primary); font-size: 1.5rem; cursor: pointer; padding: 0.25rem; }

  /* Sidebar footer — keyboard shortcuts */
  .sidebar-footer {
    padding: 0.6rem 1rem;
    border-top: 1px solid var(--border-color);
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
    flex-shrink: 0;
  }
  .shortcut-hint {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.75rem;
    color: var(--text-muted);
    background: none;
    border: none;
    cursor: default;
    padding: 0.15rem 0;
    text-align: left;
    width: 100%;
    transition: color 0.15s;
  }
  button.shortcut-hint {
    cursor: pointer;
  }
  button.shortcut-hint:hover {
    color: var(--text-primary);
  }
  kbd {
    font-family: inherit;
    font-size: 0.65rem;
    padding: 0.1rem 0.4rem;
    border-radius: 4px;
    background: var(--bg-hover);
    border: 1px solid var(--border-color);
    color: var(--text-secondary);
    line-height: 1.4;
    flex-shrink: 0;
  }
  .shortcut-label {
    font-size: 0.7rem;
  }

  @media (max-width: 768px) {
    .sidebar { transform: translateX(-100%); width: 100%; z-index: 10000; }
    .sidebar.open { transform: translateX(0); }
    .hamburger { display: block; }
    .sidebar-footer { display: none; }
  }
  .sidebar-overlay {
    display: none;
    position: fixed;
    top: 0; left: 0; width: 100%; height: 100%;
    background: rgba(0,0,0,0.5);
    z-index: 9999;
  }
  @media (max-width: 768px) {
    .sidebar-overlay.open { display: block; }
  }
</style>
