export function formatBytes(bytes) {
  if (!bytes || bytes === 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  const val = bytes / Math.pow(1024, i);
  return `${val.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

export function formatSpeed(bytesPerSec) {
  if (!bytesPerSec || bytesPerSec <= 0) return '—';
  return `${formatBytes(bytesPerSec)}/s`;
}

export function formatETA(remainingBytes, speedBPS) {
  if (!remainingBytes || !speedBPS || speedBPS <= 0) return '—';
  const seconds = remainingBytes / speedBPS;
  if (seconds < 60) return `${Math.round(seconds)}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`;
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return `${h}h ${m}m`;
}

export function formatUptime(seconds) {
  if (!seconds || seconds <= 0) return '—';
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

export function timeAgo(dateStr) {
  if (!dateStr) return '—';
  const now = new Date();
  const d = new Date(dateStr);
  const diff = Math.floor((now.getTime() - d.getTime()) / 1000);
  if (diff < 60) return 'just now';
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

/**
 * Escape HTML special characters in a string to prevent XSS.
 * Use this when inserting untrusted data into HTML strings programmatically.
 * Svelte templates automatically escape `{variables}`, but this helper
 * provides defense-in-depth for string construction and non-Svelte contexts.
 */
export function escapeHtml(str) {
  if (str == null) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
}

export function statusBadge(status) {
  const map = {
    'connected':    'badge-ok',
    'disconnected': 'badge-danger',
    'reconnecting': 'badge-warning',
    'connecting':   'badge-warning',
    'queued':       'badge-warning',
    'downloading':  'badge-info',
    'completed':    'badge-success',
    'failed':       'badge-danger',
    'paused':       'badge-paused',
    'skipped_existing': 'badge-skipped',
    'ok':           'badge-ok',
    'timeout':      'badge-warning',
    'skipped':      'badge-skipped',
  };
  const cls = map[status] || 'badge-info';
  return { cls, status };
}

/**
 * Normalize a size filter string: if it contains only digits, append "MB".
 * This prevents filter failures when the user enters a plain number without a unit.
 */
export function normalizeSize(s) {
  const trimmed = (s || '').trim();
  if (/^\d+$/.test(trimmed)) return trimmed + 'MB';
  return trimmed;
}

// Debounce function to limit API call frequency
export function debounce(fn, delay = 300) {
  let timeoutId;
  return function(...args) {
    clearTimeout(timeoutId);
    timeoutId = setTimeout(() => fn.apply(this, args), delay);
  };
}

/* ---------------------------------------------------------------------------
 * disabledReason — central helper for disabled-state tooltips and a11y labels
 * ---------------------------------------------------------------------------
 * Each button/input in the UI repeats the same pattern:
 *   - `disabled={!condition}`
 *   - `title={condition ? enabledLabel : disabledReasonText}`
 *   - `aria-label={...}` reflecting both states for screen readers
 *
 * This helper returns a single object with `{ disabled, title, ariaLabel }`
 * derived from the state, so every button stays consistent and any wording
 * change only has to be made in one place. Pair it with the global CSS
 * tooltip helper in app.css (`.btn[disabled][title]:hover::after`) for a
 * uniform hover experience across the app.
 *
 * Usage:
 *   const r = disabledReason(srv.status === 'connected', {
 *     enabled:  'Join channel',
 *     disabled: 'Connect to the server first to join channels',
 *   });
 *   <button disabled={r.disabled} title={r.title} aria-label={r.ariaLabel}>…</button>
 *
 * Convenience wrappers for the two most common cases are also exported
 * (`serverConnectionReason`, `channelJoinedReason`).
 * ------------------------------------------------------------------------- */
export function disabledReason(isDisabled, labels) {
  const { enabled, disabled } = labels || {};
  if (isDisabled) {
    return {
      disabled: true,
      title: disabled ?? '',
      ariaLabel: `${enabled ?? ''} (disabled${disabled ? ': ' + disabled : ''})`,
    };
  }
  return {
    disabled: false,
    title: enabled ?? '',
    ariaLabel: enabled ?? '',
  };
}

/**
 * Convenience wrapper for the common "server must be connected" pattern.
 * Returns null when the server is connected (no reason to disable), or a
 * reason object describing why the control is disabled otherwise.
 *
 *   {#if !serverConnectionReason(srv.status)}
 *     …render enabled control…
 *   {:else}
 *     <button disabled={true} title={reason.title} …>…</button>
 *   {/if}
 */
export function serverConnectionReason(status, actionLabel = 'this action') {
  if (status === 'connected') return null;
  return disabledReason(true, {
    enabled: actionLabel,
    disabled: `Connect to the server first to ${actionLabel.toLowerCase()}`,
  });
}

/**
 * Convenience wrapper for the "must be joined to channel" pattern.
 * Returns null when the channel is joined, or a reason object otherwise.
 */
export function channelJoinedReason(joined, channelName, actionLabel = 'perform this action') {
  if (joined) return null;
  const ch = channelName ? ` ${channelName}` : ' the channel';
  return disabledReason(true, {
    enabled: actionLabel,
    disabled: `Join${ch} first to ${actionLabel.toLowerCase()}`,
  });
}
