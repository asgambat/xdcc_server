<script>
  import { sseStatus } from '../lib/stores.js';
</script>

{#if $sseStatus !== 'connected'}
  <div class="connection-status">
    <span class="connection-dot" class:connected={$sseStatus === 'connected'} class:disconnected={$sseStatus === 'disconnected'} class:reconnecting={$sseStatus === 'reconnecting'}></span>
    <span>
      {#if $sseStatus === 'connected'}Connected
      {:else if $sseStatus === 'reconnecting'}Reconnecting...
      {:else}Disconnected{/if}
    </span>
  </div>
{/if}

<style>
  .connection-status {
    position: fixed;
    top: 0.5rem;
    right: 0.5rem;
    z-index: 150;
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.3rem 0.6rem;
    border-radius: var(--radius);
    font-size: 0.75rem;
    background: var(--bg-secondary);
    border: 1px solid var(--border-color);
  }
  .connection-dot {
    width: 8px; height: 8px; border-radius: 50%;
  }
  .connection-dot.connected { background: var(--success); }
  .connection-dot.disconnected { background: var(--danger); }
  .connection-dot.reconnecting { background: var(--warning); animation: pulse 1.5s infinite; }
  @keyframes pulse {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.4; }
  }
</style>
