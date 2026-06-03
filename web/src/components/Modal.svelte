<script>
  import { createEventDispatcher } from 'svelte';

  export let title = '';
  export let visible = false;

  const dispatch = createEventDispatcher();

  function close() {
    dispatch('close');
  }

  function outsideClick(e) {
    if (e.target === e.currentTarget) close();
  }
</script>

{#if visible}    <div class="modal-overlay" onclick={outsideClick} onkeydown={(e) => e.key === 'Escape' && close()} role="dialog" aria-modal="true" tabindex="-1">
    <div class="modal">
      <div class="modal-title">{title}</div>
      <div class="modal-body">
        <slot />
      </div>
    </div>
  </div>
{/if}

<style>
  .modal-overlay {
    position: fixed; top: 0; left: 0; right: 0; bottom: 0;
    background: rgba(0,0,0,0.6);
    display: flex; align-items: center; justify-content: center;
    z-index: 200;
    backdrop-filter: blur(4px);
    animation: fadeIn 0.2s ease;
  }
  .modal {
    background: var(--bg-secondary);
    border: 1px solid var(--border-color);
    border-radius: var(--radius-xl);
    padding: 1.5rem;
    width: 90%;
    max-width: 500px;
    max-height: 80vh;
    overflow-y: auto;
    box-shadow: var(--shadow-lg);
    animation: slideUp 0.2s ease;
  }
  .modal-title { font-size: 1.1rem; font-weight: 600; margin-bottom: 1rem; }
  :global(.modal-actions) { display: flex; gap: 0.5rem; justify-content: flex-end; margin-top: 1.25rem; }
  @keyframes fadeIn { from { opacity: 0; } to { opacity: 1; } }
  @keyframes slideUp { from { opacity: 0; transform: translateY(10px); } to { opacity: 1; transform: translateY(0); } }
</style>
