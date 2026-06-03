<script>
  import { toasts } from '../lib/stores.js';
</script>

<div class="toast-container">
  {#each $toasts as t (t.id)}
    <div class="toast toast-{t.type}">
      <span>
        {#if t.type === 'success'}✅
        {:else if t.type === 'error'}❌
        {:else if t.type === 'warning'}⚠️
        {:else}ℹ️{/if}
      </span>
      <span>{t.message}</span>
    </div>
  {/each}
</div>
