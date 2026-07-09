<script>
  import { onMount, onDestroy } from 'svelte';
  import { servers, channels } from '../lib/stores.js';
  import { ServersAPI, sseClient } from '../lib/api.js';
  import { statusBadge, serverConnectionReason, channelJoinedReason } from '../lib/utils.js';
  import { addToast } from '../lib/stores.js';
  import Modal from './Modal.svelte';

  let newAddress = $state('irc.rizon.net');
  let newPort = $state(6667);
  let loading = $state(true);
  let messagesEnabled = $state(false);

  // Track which servers are currently being connected to prevent double-clicks
  let connectingServers = $state(new Set());
  // Timeout IDs for connect fallback (if SSE doesn't respond within 15s)
  let connectTimeouts = new Map();
  let showTopicModal = $state(false);
  let topicModalTitle = $state('');
  let topicModalContent = $state('');
  let unsubServerConnected, unsubServerDisconnected;
  let unsubChannelJoined, unsubChannelLeft, unsubChannelTopicUpdated;

  // Sort servers: connected first, then disconnected, then by address.
  // Server records may use either `address` or `server_address`.
  let sorted = $derived($servers.length ? [...$servers].sort((a, b) => {
    if (a.status === 'connected' && b.status !== 'connected') return -1;
    if (a.status !== 'connected' && b.status === 'connected') return 1;
    const addrA = (a.address || a.server_address || '');
    const addrB = (b.address || b.server_address || '');
    return addrA.localeCompare(addrB);
  }) : []);

  onMount(async () => {
    // Register SSE handlers BEFORE awaiting loadServers(), so events
    // that arrive during the initial data load are not silently dropped.

    // Helper: clear a connect timeout and remove from connecting set
    function onConnectResolved(serverId) {
      if (connectTimeouts.has(serverId)) {
        clearTimeout(connectTimeouts.get(serverId));
        connectTimeouts.delete(serverId);
      }
      if (connectingServers.has(serverId)) {
        connectingServers = new Set([...connectingServers].filter(x => x !== serverId));
      }
    }

    // Server status store updates are handled in App.svelte (always mounted).
    // We only manage toast notifications, channels, and connecting state here.
    unsubServerConnected = sseClient.on('server_connected', (data) => {
      const serverId = data.server_id;
      if (serverId) {
        loadChannels(serverId);
        if (connectingServers.has(serverId)) {
          onConnectResolved(serverId);
          const addr = data.server_addr || '';
          addToast(addr ? `Connected to ${addr}` : 'Server connected', 'success');
        }
      }
    });

    unsubServerDisconnected = sseClient.on('server_disconnected', (data) => {
      const serverId = data.server_id;
      if (serverId && connectingServers.has(serverId)) {
        onConnectResolved(serverId);
        const addr = data.server_addr || '';
        addToast(addr ? `Connection to ${addr} failed` : 'Connection failed', 'error');
      }
    });

    unsubChannelJoined = sseClient.on('channel_joined', (data) => {
      const serverId = data.server_id;
      if (serverId) loadChannels(serverId);
    });

    unsubChannelLeft = sseClient.on('channel_left', (data) => {
      const serverId = data.server_id;
      if (serverId) loadChannels(serverId);
    });

    unsubChannelTopicUpdated = sseClient.on('channel_topic_updated', (data) => {
      const serverId = data.server_id;
      if (serverId) loadChannels(serverId);
    });

    // Fetch messages_enabled flag from public /api/status endpoint.
    // This controls whether the send-message button is visible/enabled.
    try {
      const res = await fetch('/api/status');
      if (res.ok) {
        const statusData = await res.json();
        if (statusData.messages_enabled !== undefined) {
          messagesEnabled = statusData.messages_enabled;
        }
      }
    } catch { /* default to disabled */ }

    // Now load initial data — SSE handlers are ready to receive events
    await loadServers();
    loading = false;
  });

  onDestroy(() => {
    // Clear all pending connect timeouts to prevent memory leaks
    for (const tid of connectTimeouts.values()) clearTimeout(tid);
    connectTimeouts.clear();
    if (unsubServerConnected) unsubServerConnected();
    if (unsubServerDisconnected) unsubServerDisconnected();
    if (unsubChannelJoined) unsubChannelJoined();
    if (unsubChannelLeft) unsubChannelLeft();
    if (unsubChannelTopicUpdated) unsubChannelTopicUpdated();
  });

  async function loadServers() {
    try {
      const list = await ServersAPI.list();
      servers.set(list);
      // Pre-load channels for all servers to avoid {#await} infinite loop
      await Promise.allSettled(list.map(srv => loadChannels(srv.id)));
    } catch (e) {
      addToast(e.message, 'error');
    }
  }

  let connectingNew = $state(false);

  function scheduleConnectTimeout(serverId) {
    // Clear any existing timeout for this server
    if (connectTimeouts.has(serverId)) {
      clearTimeout(connectTimeouts.get(serverId));
    }
    // Set fallback: if SSE doesn't respond within 15s, refresh state manually
    const tid = setTimeout(async () => {
      connectTimeouts.delete(serverId);
      if (connectingServers.has(serverId)) {
        connectingServers = new Set([...connectingServers].filter(x => x !== serverId));
        await loadServers();
      }
    }, 15000);
    connectTimeouts.set(serverId, tid);
  }

  async function connectNewServer() {
    if (!newAddress.trim()) return addToast('Enter a server address', 'warning');
    connectingNew = true;
    try {
      const result = await ServersAPI.connect({ address: newAddress.trim(), port: newPort });
      const serverId = result?.id;
      if (serverId) {
        connectingServers = new Set([...connectingServers, serverId]);
        scheduleConnectTimeout(serverId);
      }
      await loadServers();
    } catch (e) {
      addToast(e.message, 'error');
    } finally {
      connectingNew = false;
    }
  }

  async function connectServer(id) {
    connectingServers = new Set([...connectingServers, id]);
    scheduleConnectTimeout(id);
    try {
      await ServersAPI.connect(id);
      // Wait for SSE server_connected (success) or server_disconnected (failure)
      // before showing any toast or removing from the connecting set.
      // The scheduleConnectTimeout provides a 15s fallback if SSE doesn't respond.
    } catch (e) {
      if (connectTimeouts.has(id)) {
        clearTimeout(connectTimeouts.get(id));
        connectTimeouts.delete(id);
      }
      connectingServers = new Set([...connectingServers].filter(x => x !== id));
      addToast(e.message, 'error');
    }
  }

  async function disconnectServer(id) {
    // Clean up any pending connect timeout and remove from connecting set
    if (connectTimeouts.has(id)) {
      clearTimeout(connectTimeouts.get(id));
      connectTimeouts.delete(id);
    }
    connectingServers = new Set([...connectingServers].filter(x => x !== id));
    try {
      await ServersAPI.disconnect(id);
      addToast('Server disconnected', 'info');
      await loadServers();
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function removeServer(id, address) {
    if (!window.confirm(`Remove server ${address}? This will disconnect (if connected) and delete it permanently.`)) return;
    try {
      await ServersAPI.remove(id);
      addToast(`Server ${address} removed`, 'info');
      await loadServers();
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function loadChannels(serverId) {
    try {
      const chs = await ServersAPI.listChannels(serverId);
      channels.update(c => ({ ...c, [serverId]: chs || [] }));
    } catch (e) {
      addToast(e.message, 'error');
      // Ensure the key exists even on failure so the UI doesn't show "Loading..." forever
      channels.update(c => ({ ...c, [serverId]: [] }));
    }
  }

  async function joinChannel(serverId) {
    const input = /** @type {HTMLInputElement | null} */ (document.getElementById(`channel-input-${serverId}`));
    let channelName = input?.value.trim();
    if (!channelName) return addToast('Enter a channel name', 'warning');

    // Normalize: channels are case-insensitive per RFC 1459, always lowercase
    channelName = channelName.toLowerCase();
    if (!channelName.startsWith('#')) {
      channelName = '#' + channelName;
    }

    try {
      await ServersAPI.joinChannel(serverId, channelName);
      addToast(`Joined ${channelName}`, 'success');
      input.value = '';
      await loadChannels(serverId);
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function leaveChannel(serverId, channel) {
    try {
      await ServersAPI.leaveChannel(serverId, channel);
      addToast(`Left ${channel}`, 'info');
      await loadChannels(serverId);
    } catch (e) { addToast(e.message, 'error'); }
  }

  async function toggleAutoJoin(serverId, channel, autoJoin) {
    try {
      await ServersAPI.setChannelAutoJoin(serverId, channel, autoJoin);
      addToast(`${channel}: ${autoJoin ? 'Added to' : 'Removed from'} auto-join`, autoJoin ? 'success' : 'info');
      await loadChannels(serverId);
    } catch (e) { addToast(e.message, 'error'); }
  }

  function openTopicModal(topic, channelName) {
    topicModalTitle = `Topic: ${channelName}`;
    topicModalContent = topic || 'No topic set';
    showTopicModal = true;
  }

  // Send-message modal state
  let showMessageModal = $state(false);
  let messageModalChannel = $state('');
  let messageModalServerId = $state(null);
  let messageText = $state('');
  let messageSending = $state(false);

  function openMessageModal(serverId, channelName) {
    if (!channelName) return;
    messageModalServerId = serverId;
    messageModalChannel = channelName;
    messageText = '';
    showMessageModal = true;
  }

  function closeMessageModal() {
    showMessageModal = false;
    messageText = '';
    messageModalServerId = null;
    messageModalChannel = '';
  }

  async function sendChannelMessage() {
    if (!messageModalServerId || !messageModalChannel) return;
    const text = messageText.trim();
    if (!text) return addToast('Enter a message to send', 'warning');
    if (text.length > 4000) return addToast('Message is too long (max 4000 characters)', 'warning');

    messageSending = true;
    try {
      await ServersAPI.sendMessage(messageModalServerId, messageModalChannel, text);
      addToast(`Message sent to ${messageModalChannel}`, 'success');
      closeMessageModal();
    } catch (e) {
      addToast(e.message, 'error');
    } finally {
      messageSending = false;
    }
  }

  function onMessageKeydown(e) {
    // Submit on Enter (without Shift) so users can still type multi-line
    // messages using Shift+Enter.
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      sendChannelMessage();
    }
  }
</script>

<!-- Connect to Server — always visible at the top -->
<div class="card mb-3">
  <div class="card-header"><span class="card-title">🔌 Connect to Server</span></div>
  <div class="form-row">
    <div class="form-group" style="flex:2">
      <label class="form-label" for="new-address">Server Address</label>
      <input class="form-input" id="new-address" bind:value={newAddress} placeholder="irc.rizon.net" />
    </div>
    <div class="form-group" style="flex:1">
      <label class="form-label" for="new-port">Port</label>
      <input class="form-input" id="new-port" bind:value={newPort} placeholder="6667" type="number" />
    </div>
    <div class="form-group" style="display:flex;align-items:end">
      <button class="btn btn-primary" onclick={connectNewServer} disabled={connectingNew}>
        {connectingNew ? 'Connecting...' : 'Connect'}
      </button>
    </div>
  </div>
</div>

<!-- Server list section -->
<h3 class="section-title">🖥️ Server List</h3>

<Modal title={topicModalTitle} visible={showTopicModal} on:close={() => showTopicModal = false}>
  <div style="white-space:pre-wrap;word-wrap:break-word;max-height:400px;overflow-y:auto">{topicModalContent}</div>
</Modal>

<Modal title={`Send message to ${messageModalChannel}`} visible={showMessageModal} on:close={closeMessageModal}>
  <div class="form-group">
    <label class="form-label" for="channel-message-input">Message</label>
    <textarea
      id="channel-message-input"
      class="form-input"
      rows="4"
      maxlength="4000"
      placeholder="Type your message here… (Enter to send, Shift+Enter for newline)"
      bind:value={messageText}
      onkeydown={onMessageKeydown}
      style="resize:vertical;font-family:inherit;min-height:90px"
    ></textarea>
    <div class="text-sm text-muted mt-1" style="display:flex;justify-content:space-between">
      <span>Channel: <strong>{messageModalChannel}</strong></span>
      <span>{messageText.length} / 4000</span>
    </div>
  </div>
  <div class="modal-actions">
    <button class="btn btn-ghost" onclick={closeMessageModal} disabled={messageSending}>Cancel</button>
    <button class="btn btn-primary" onclick={sendChannelMessage} disabled={messageSending || !messageText.trim()}>
      {messageSending ? 'Sending…' : 'SEND'}
    </button>
  </div>
</Modal>

{#if loading}
  <div class="spinner"></div>
{:else if sorted.length === 0}
  <div class="empty-state">
    <div class="empty-state-icon">🖥️</div>
    <div class="empty-state-text">No servers configured</div>
    <div class="empty-state-sub">Use the form above to connect to an IRC server</div>
  </div>
{:else}
  {#each sorted as srv}
    <div class="card mb-2">
      <div class="card-header">
        <div>
          <span class="card-title">{srv.address}:{srv.port || 6667}</span>
          <div class="text-sm text-muted mt-1">
            <span class="badge badge-{statusBadge(srv.status).cls}"><span class="badge-dot"></span>{srv.status}</span>
          </div>
        </div>
        <div class="btn-group">
          {#if connectingServers.has(srv.id)}
            <button class="btn btn-sm btn-success" disabled>Connecting...</button>
          {:else if srv.status === 'reconnecting'}
            <button class="btn btn-sm btn-danger" onclick={() => disconnectServer(srv.id)}>Cancel reconnect</button>
          {:else if srv.status !== 'connected'}
            <button class="btn btn-sm btn-success" onclick={() => connectServer(srv.id)}>Connect</button>
          {:else}
            <button class="btn btn-sm btn-danger" onclick={() => disconnectServer(srv.id)}>Disconnect</button>
          {/if}
          <button class="btn btn-sm btn-ghost" onclick={() => removeServer(srv.id, srv.address)} title="Remove server">🗑️</button>
        </div>
      </div>

      {#if $channels[srv.id] !== undefined}
        {#if $channels[srv.id]?.length}
          <div class="table-container">
            <table>
              <thead><tr><th>Channel</th><th>Speed</th><th>Topic</th><th>Joined</th><th>Auto-join</th><th>Actions</th></tr></thead>
              <tbody>
                {#each $channels[srv.id] as ch}
                  <tr>
                    <td><strong>{ch.name}</strong></td>
                    <td class="text-sm text-muted">
                      {#if ch.avg_speed_bps > 0}
                        {(ch.avg_speed_bps / 1024 / 1024).toFixed(2)} MB/s
                      {:else}
                        —
                      {/if}
                    </td>
                    <td class="text-muted truncate" style="max-width:300px;cursor:pointer" onclick={() => openTopicModal(ch.topic, ch.name)} title="Click to view full topic">{ch.topic || '—'}</td>
                    <td>
                      <span class="badge" class:badge-ok={ch.joined} class:badge-info={!ch.joined}>
                        {ch.joined ? 'Yes' : 'No'}
                      </span>
                    </td>
                    <td>
                      <span class="badge" class:badge-ok={ch.auto_join} class:badge-info={!ch.auto_join}>
                        {ch.auto_join ? 'Yes' : 'No'}
                      </span>
                    </td>
                    <td>
                      <div class="btn-group">
                        {#if ch.auto_join}
                          <button class="btn btn-sm btn-ghost" onclick={() => toggleAutoJoin(srv.id, ch.name, false)} title="Remove from auto-join">−</button>
                        {:else}
                          <button class="btn btn-sm btn-ghost" onclick={() => toggleAutoJoin(srv.id, ch.name, true)} title="Add to auto-join">+</button>
                        {/if}
                        <!-- svelte-ignore a11y_aria_attributes -->
                        <button
                          class="btn btn-sm btn-ghost"
                          onclick={() => openMessageModal(srv.id, ch.name)}
                          disabled={messagesEnabled ? (channelJoinedReason(ch.joined, ch.name, `Send a message to ${ch.name}`)?.disabled ?? false) : true}
                          title={messagesEnabled ? (channelJoinedReason(ch.joined, ch.name, `Send a message to ${ch.name}`)?.title ?? 'Send a message') : 'Message sending is disabled in server configuration'}
                          aria-label={messagesEnabled ? (channelJoinedReason(ch.joined, ch.name, `Send a message to ${ch.name}`)?.ariaLabel ?? `Send a message to ${ch.name}`) : `Send a message to ${ch.name} (disabled: message sending disabled in config)`}
                        >✏️</button>
                        <button class="btn btn-sm btn-ghost" onclick={() => leaveChannel(srv.id, ch.name)} title="Leave">✕</button>
                      </div>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {:else}
          <div class="text-sm text-muted" style="padding:0.5rem 0">No channels configured</div>
        {/if}
      {:else}
        <div class="text-sm text-muted" style="padding:0.5rem 0">Loading channels...</div>
      {/if}

      <div class="flex gap-1 mt-1" style="align-items:center">
        <input
          class="form-input"
          id="channel-input-{srv.id}"
          placeholder="#channel"
          style="width:200px"
          disabled={serverConnectionReason(srv.status, 'Type a channel name to join')?.disabled ?? false}
          title={serverConnectionReason(srv.status, 'Type a channel name to join')?.title ?? 'Type a channel name to join'}
          aria-label={serverConnectionReason(srv.status, 'Type a channel name to join')?.ariaLabel ?? 'Type a channel name to join'}
          onkeydown={(e) => e.key === 'Enter' && !serverConnectionReason(srv.status, 'Type a channel name to join')?.disabled && joinChannel(srv.id)}
        />
        <button
          class="btn btn-sm btn-primary"
          disabled={serverConnectionReason(srv.status, 'Join channel')?.disabled ?? false}
          title={serverConnectionReason(srv.status, 'Join channel')?.title ?? 'Join channel'}
          aria-label={serverConnectionReason(srv.status, 'Join channel')?.ariaLabel ?? 'Join channel'}
          onclick={() => joinChannel(srv.id)}
        >Join</button>
      </div>
    </div>
  {/each}
{/if}
