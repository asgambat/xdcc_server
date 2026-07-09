package ircmanager

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"xdcc_server/internal/channellog"
	"xdcc_server/internal/config"
	"xdcc_server/internal/entities"
	xdccirc "xdcc_server/internal/irc"
	"xdcc_server/internal/logging"
	"xdcc_server/internal/pubsub"
	"xdcc_server/internal/store"

	"github.com/lrstanley/girc"
)

// Internal constants
const (
	defaultConnectionTimeout  = 30 * time.Second       // timeout for connections to establish
	waitConnectedPollInterval = 200 * time.Millisecond // polling interval when waiting for connection
	maxReconnectRetries       = 10                     // max reconnection attempts before giving up
)

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// Manager manages persistent IRC connections to multiple servers.
// Each server gets a dedicated connection that stays alive until explicitly
// disconnected. Auto-connect servers from the configuration are connected
// on Start(). Events are emitted via Subscribe() for SSE propagation.
type Manager struct {
	mu     sync.RWMutex
	store  store.ServerStore
	cfg    *config.Config
	logger *logging.Logger

	// serverOps serializes connect/disconnect operations per server ID,
	// preventing interleaving races between ConnectServer and DisconnectServer.
	opMu      sync.Mutex
	serverOps map[int64]*sync.Mutex

	conns      map[int64]*managedConnection
	subscriber *pubsub.Hub[Event]

	// chlog writes per-channel PRIVMSG/NOTICE traffic to disk when the
	// operator has enabled the hidden irc.channel_log feature in YAML.
	// May be nil if initialization failed (logged at New()); in that case
	// channel logging is silently skipped — better than blocking the
	// whole manager on a filesystem hiccup.
	chlog *channellog.Logger

	// chlogReconcileDone is closed by Stop() to signal the config revision
	// polling goroutine to exit. chlogReconcileExited is closed by the
	// goroutine on exit, so Stop() can wait for it before closing chlog.
	chlogReconcileDone   chan struct{}
	chlogReconcileExited chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
}

// New creates a new IRC connection manager.
func New(st store.ServerStore, cfg *config.Config, logger *logging.Logger) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		store:      st,
		cfg:        cfg,
		logger:     logger,
		serverOps:  make(map[int64]*sync.Mutex),
		conns:      make(map[int64]*managedConnection),
		subscriber: pubsub.New[Event](256),
		ctx:        ctx,
		cancel:     cancel,
	}

	// Initialize the channel logger if the hidden irc.channel_log list is
	// non-empty, or if log_private_messages is enabled. The log directory is
	// taken from logging.file_path's directory when available, otherwise it
	// defaults to ./logs/ to keep the per-channel files alongside the main
	// server log.
	if len(cfg.IRC.ChannelLog) > 0 || cfg.IRC.LogPrivateMessages {
		logDir := "./logs"
		if cfg.Logging.FilePath != "" {
			logDir = filepath.Dir(cfg.Logging.FilePath)
		}
		// Resolve to absolute path for a clear, unambiguous startup message.
		if abs, err := filepath.Abs(logDir); err == nil {
			logDir = abs
		}
		cl, err := channellog.New(logDir)
		if err != nil {
			logger.Warnf("channellog: init failed (dir=%s): %v — channel logging disabled", logDir, err)
		} else {
			m.chlog = cl
			if len(cfg.IRC.ChannelLog) > 0 {
				logger.Infof("channellog: enabled for %d channel(s), writing to %s", len(cfg.IRC.ChannelLog), logDir)
			}
			if cfg.IRC.LogPrivateMessages {
				logger.Infof("channellog: private message logging enabled, writing to %s/private.log", logDir)
			}
		}
	}

	return m
}

// ---------------------------------------------------------------------------
// Lifecycle (Fase 3.2, 3.3)
// ---------------------------------------------------------------------------

// Start connects to all auto-connect servers from the configuration and joins
// their auto-join channels. It also connects to any servers marked auto_connect
// in the database that are not yet managed.
func (m *Manager) Start() error {
	// Start config revision polling to reconcile stale channel log files
	// when the operator adds/removes channels from irc.channel_log at runtime.
	if m.chlog != nil {
		m.chlogReconcileDone = make(chan struct{})
		m.chlogReconcileExited = make(chan struct{})
		go m.reconcileChannelLogLoop()
	}

	// Connect default servers from config — take a snapshot to avoid
	// racing with a concurrent config Replace from the API.
	cfgSnap := m.cfg.Clone()
	for _, sc := range cfgSnap.IRC.DefaultServers {
		if !sc.AutoConnect {
			continue
		}

		// Check if already stored in DB
		servers, err := m.store.ListServers(m.ctx)
		if err != nil {
			m.logger.Warnf("listing servers failed: %v", err)
			continue
		}

		var existingID int64
		var found bool
		for _, s := range servers {
			if s.Address == sc.Address && s.Port == sc.Port {
				existingID = s.ID
				found = true
				break
			}
		}

		if !found {
			// Add to DB
			id, err := m.store.AddServer(m.ctx, store.ServerRecord{
				Address:     sc.Address,
				Port:        sc.Port,
				AutoConnect: true,
				Status:      "disconnected",
			})
			if err != nil {
				m.logger.Warnf("adding server %s to DB failed: %v", sc.Address, err)
				continue
			}
			existingID = id

			// Add channels to DB
			for _, cc := range sc.Channels {
				_, err := m.store.AddChannel(m.ctx, store.ChannelRecord{
					ServerID: existingID,
					Name:     cc.Name,
					AutoJoin: cc.AutoJoin,
					Joined:   false,
				})
				if err != nil {
					m.logger.Warnf("adding channel %s to DB failed: %v", cc.Name, err)
				}
			}
		}

		// Connect this server
		if err := m.ConnectServerByID(existingID); err != nil {
			m.logger.Warnf("connecting to %s failed: %v", sc.Address, err)
		}
	}

	// Also connect any DB servers marked auto_connect that aren't in config
	servers, err := m.store.ListServers(m.ctx)
	if err != nil {
		return fmt.Errorf("listing servers: %w", err)
	}

	// Collect server IDs to connect under RLock, then connect outside
	// the lock. This avoids the fragile unlock/re-lock pattern in the
	// loop that could race with a concurrent RemoveServer modifying m.conns
	// or the servers slice between iterations.
	m.mu.RLock()
	var toConnect []int64
	for _, s := range servers {
		if s.AutoConnect && s.Status == "disconnected" {
			if _, exists := m.conns[s.ID]; !exists {
				toConnect = append(toConnect, s.ID)
			}
		}
	}
	m.mu.RUnlock()

	for _, id := range toConnect {
		if err := m.ConnectServerByID(id); err != nil {
			m.logger.Warnf("connecting to server %d failed: %v", id, err)
		}
	}

	return nil
}

// Stop gracefully disconnects all managed connections.
func (m *Manager) Stop() {
	m.mu.RLock()
	ids := make([]int64, 0, len(m.conns))
	for id := range m.conns {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(sid int64) {
			defer wg.Done()
			_ = m.DisconnectServer(sid)
		}(id)
	}
	wg.Wait()

	// Stop the config revision polling goroutine and wait for it to fully
	// exit before closing the channel logger. This prevents a race between
	// ReconcileChannels (still running in the goroutine) and Close().
	if m.chlogReconcileDone != nil {
		close(m.chlogReconcileDone)
		<-m.chlogReconcileExited
	}

	// Flush per-channel log files to disk before tearing down the manager.
	if m.chlog != nil {
		if err := m.chlog.Close(); err != nil {
			m.logger.Warnf("channellog: close error: %v", err)
		}
	}

	m.cancel()
}

// ---------------------------------------------------------------------------
// Config revision polling for channel log reconciliation
// ---------------------------------------------------------------------------

// reconcileChannelLogLoop periodically polls the config revision number.
// When it detects a change, it reconciles the channellog file handles:
// channels removed from irc.channel_log get their log files closed.
// Runs every 30 seconds (matching channellog's own sync interval).
func (m *Manager) reconcileChannelLogLoop() {
	defer close(m.chlogReconcileExited)

	// NOTE: reading lastRev at goroutine start creates a tiny window where
	// a config update that lands between the read and the first tick is
	// missed. This is acceptable because the ticker fires every 30s, so
	// the change is picked up on the next tick at worst. If this becomes
	// an issue, initialize lastRev = 0 to force a reconciliation on the
	// first tick instead.
	lastRev := m.cfg.GetConfigRevision()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.chlogReconcileDone:
			return
		case <-ticker.C:
			rev := m.cfg.GetConfigRevision()
			if rev == lastRev {
				continue
			}
			lastRev = rev
			m.logger.Debugf("channellog: config revision changed (%d), reconciling channel log files", rev)
			m.chlog.ReconcileChannels(func(ch string) bool {
				// "private" is not a real channel — it's the key used by
				// LogPrivate for the private.log file. Always keep it.
				if ch == "private" {
					return true
				}
				return m.cfg.IsChannelLogged(ch)
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Subscribe to events (Fase 3.6)
// ---------------------------------------------------------------------------

// Subscribe returns a channel that receives state change events.
// The caller MUST consume from the channel, or the manager will block on
// event emission when the subscriber queue fills up.
func (m *Manager) Subscribe() chan Event {
	return m.subscriber.Subscribe()
}

// Unsubscribe removes a previously subscribed channel.
func (m *Manager) Unsubscribe(ch chan Event) {
	m.subscriber.Unsubscribe(ch)
}

// emitEvent sends an event to all subscribers (non-blocking).
func (m *Manager) emitEvent(evt Event) {
	evt.Timestamp = time.Now()
	m.subscriber.Publish(evt)
}

// lockServerOp acquires a per-server mutex so ConnectServer and
// DisconnectServer cannot run concurrently for the same server ID.
func (m *Manager) lockServerOp(serverID int64) func() {
	m.opMu.Lock()
	lock, ok := m.serverOps[serverID]
	if !ok {
		lock = &sync.Mutex{}
		m.serverOps[serverID] = lock
	}
	m.opMu.Unlock()

	lock.Lock()
	return lock.Unlock
}

// ---------------------------------------------------------------------------
// Public API (Fase 3.5)
// ---------------------------------------------------------------------------

// ConnectServerByID connects to an IRC server using its database ID.
// It loads server details from the store, including auto-join channels.
func (m *Manager) ConnectServerByID(serverID int64) error {
	srv, err := m.store.GetServer(m.ctx, serverID)
	if err != nil {
		return fmt.Errorf("fetching server %d: %w", serverID, err)
	}
	if srv == nil {
		return fmt.Errorf("server %d not found", serverID)
	}
	return m.ConnectServer(srv)
}

// ConnectServer connects to an IRC server with the given details.
// If the server is already connected, it returns nil.
func (m *Manager) ConnectServer(srv *store.ServerRecord) error {
	unlockServer := m.lockServerOp(srv.ID)
	defer unlockServer()

	m.mu.Lock()

	// If already connected, return immediately.
	if existing, ok := m.conns[srv.ID]; ok {
		status := existing.Status()
		if status == "connected" || status == "connecting" || status == "reconnecting" {
			m.mu.Unlock()
			return nil
		}
	}

	// Grab the old connection (if any) for cleanup outside the lock.
	// The per-server lock (lockServerOp) already prevents concurrent
	// ConnectServer/DisconnectServer for the same server ID.
	var oldConn *managedConnection
	if existing, ok := m.conns[srv.ID]; ok {
		oldConn = existing
	}

	m.mu.Unlock()

	// Cancel and wait for the old connection's run() goroutine to
	// finish. This replaces the fragile time.Sleep(10ms) with
	// proper synchronization via WaitGroup.
	if oldConn != nil {
		oldConn.cancel()
		oldConn.wg.Wait()
	}

	// Load auto-join channels before publishing the new connection in m.conns.
	// If setup fails, we abort without exposing a half-initialized connection.
	channels, err := m.store.GetChannelsByServer(m.ctx, srv.ID)
	if err != nil {
		return fmt.Errorf("loading channels for server %d: %w", srv.ID, err)
	}

	autoJoinChs := make([]string, 0, len(channels))
	for _, ch := range channels {
		if ch.AutoJoin {
			autoJoinChs = append(autoJoinChs, ch.Name)
		}
	}

	// Create new managed connection
	conn := &managedConnection{
		id:          srv.ID,
		address:     srv.Address,
		port:        srv.Port,
		nickname:    m.cfg.GetNickname(),
		autoJoinChs: autoJoinChs,
		manager:     m,
		joinedChs:   make(map[string]string),
		status:      "connecting",
		startedCh:   make(chan struct{}),
	}

	// Initialize context BEFORE inserting in m.conns so that no other
	// goroutine can observe a connection with nil ctx/cancel.
	conn.ctx, conn.cancel = context.WithCancel(m.ctx)

	// Insert the new connection — fully initialized and ready to run.
	m.mu.Lock()
	m.conns[srv.ID] = conn
	m.mu.Unlock()

	// Update DB status to 'connecting' (not 'connected' yet)
	if err := m.store.SetServerStatus(m.ctx, srv.ID, "connecting"); err != nil {
		m.logger.Warnf("updating server status in DB failed: %v", err)
	}

	// Start connection in background, then wait until run() has called
	// wg.Add(1). This guarantees that any subsequent wg.Wait() (e.g. in
	// DisconnectServer) observes the counter already incremented and does
	// not return prematurely.
	go conn.run()
	<-conn.startedCh

	return nil
}

// DisconnectServer disconnects from an IRC server by its ID.
// This method is idempotent: if the server is not currently managed (e.g.,
// already disconnected or removed by a concurrent call), it returns nil
// because the desired state is already achieved.
func (m *Manager) DisconnectServer(serverID int64) error {
	unlockServer := m.lockServerOp(serverID)
	defer unlockServer()

	m.mu.Lock()
	conn, ok := m.conns[serverID]
	if ok {
		delete(m.conns, serverID)
	}
	m.mu.Unlock()

	if !ok {
		// Server is not currently managed — it may have been disconnected
		// by a concurrent request, by Stop(), or never connected at all.
		// The desired state (disconnected) is already achieved, so ensure
		// the DB reflects reality and return nil.
		if err := m.store.SetServerStatus(m.ctx, serverID, "disconnected"); err != nil {
			m.logger.Warnf("updating server %d status to 'disconnected' in DB failed: %v", serverID, err)
		}
		return nil
	}

	// Signal disconnect and wait for run() to complete
	conn.disconnect()

	// Wait for run() goroutine to finish gracefully using WaitGroup
	// This eliminates race conditions and guarantees cleanup completion
	done := make(chan struct{})
	go func() {
		conn.wg.Wait()
		close(done)
	}()

	gracefulShutdown := false

	// Use select with timeout for visibility
	select {
	case <-done:
		m.logger.Infof("server %d disconnected cleanly", serverID)
		gracefulShutdown = true
	case <-time.After(5 * time.Second):
		m.logger.Warnf("server %d shutdown taking longer than expected, still waiting...", serverID)
	}

	if !gracefulShutdown {
		// Second phase: wait up to 10 more seconds (15s total)
		select {
		case <-done:
			m.logger.Infof("server %d shutdown completed after delay", serverID)
		case <-time.After(10 * time.Second):
			// Avoid false-positive timeout errors if run() already stopped right
			// after the timeout fired.
			if conn.IsRunning() {
				m.logger.Errorf("server %d shutdown exceeded 15s and run() is still active", serverID)
				return fmt.Errorf("server %d shutdown timeout after 15s (run goroutine still active)", serverID)
			}
			m.logger.Warnf("server %d shutdown exceeded 15s wait budget, but run() already stopped", serverID)
		}
	}

	if err := m.store.SetServerStatus(m.ctx, serverID, "disconnected"); err != nil {
		m.logger.Warnf("updating server status in DB failed: %v", err)
	}
	return nil
}

// GetClient returns the underlying girc.Client for a managed connection.
// Returns nil if the server is not connected.
func (m *Manager) GetClient(serverID int64) *girc.Client {
	m.mu.RLock()
	conn, ok := m.conns[serverID]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	conn.mu.RLock()
	client := conn.irc
	conn.mu.RUnlock()
	return client
}

// JoinChannel joins a channel on a specific server.
func (m *Manager) JoinChannel(serverID int64, channel string) error {
	if m.cfg.IsChannelBlacklisted(channel) {
		return fmt.Errorf("channel %q is blacklisted", channel)
	}
	m.mu.RLock()
	conn, ok := m.conns[serverID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("server %d is not connected", serverID)
	}
	return conn.joinChannel(channel)
}

// LeaveChannel leaves a channel on a specific server.
func (m *Manager) LeaveChannel(serverID int64, channel string) error {
	m.mu.RLock()
	conn, ok := m.conns[serverID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("server %d is not connected", serverID)
	}
	return conn.leaveChannel(channel)
}

// GetServers returns the list of all known IRC servers with their status.
func (m *Manager) GetServers() []store.ServerRecord {
	servers, err := m.store.ListServers(m.ctx)
	if err != nil {
		m.logger.Warnf("listing servers failed: %v", err)
		return nil
	}

	// Overlay live status from managed connections (hold lock only for the overlay)
	m.mu.RLock()
	for i, s := range servers {
		if conn, ok := m.conns[s.ID]; ok {
			servers[i].Status = conn.Status()
		}
	}
	m.mu.RUnlock()
	return servers
}

// GetChannels returns the list of channels for a specific server.
func (m *Manager) GetChannels(serverID int64) []store.ChannelRecord {
	channels, err := m.store.GetChannelsByServer(m.ctx, serverID)
	if err != nil {
		m.logger.Warnf("listing channels for server %d failed: %v", serverID, err)
		return nil
	}

	// Overlay join status and topic from live connection
	m.mu.RLock()
	conn, ok := m.conns[serverID]
	m.mu.RUnlock()

	if ok {
		conn.mu.RLock()
		for i, ch := range channels {
			if topic, joined := conn.joinedChs[ch.Name]; joined {
				channels[i].Joined = true
				channels[i].Topic = topic
			} else {
				channels[i].Joined = false
			}
		}
		conn.mu.RUnlock()
	} else {
		// Server is not currently managed (disconnected) — reflect reality
		for i := range channels {
			channels[i].Joined = false
		}
	}

	return channels
}

// GetChannelTopic returns the topic for a specific channel.
func (m *Manager) GetChannelTopic(serverID int64, channel string) (string, error) {
	m.mu.RLock()
	conn, ok := m.conns[serverID]
	m.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("server %d is not connected", serverID)
	}

	conn.mu.RLock()
	topic, joined := conn.joinedChs[channel]
	conn.mu.RUnlock()
	if !joined {
		return "", fmt.Errorf("not joined to channel %s", channel)
	}
	return topic, nil
}

// SendChannelMessage sends a message to an IRC channel on the given server.
// The message is sent via PRIVMSG to the channel. The server must be
// connected and the client must currently be joined to the channel, otherwise
// an error is returned. Multi-line messages (containing \n or \r) are split
// into separate PRIVMSG commands so each line is delivered atomically.
func (m *Manager) SendChannelMessage(serverID int64, channel, message string) error {
	if message == "" {
		return fmt.Errorf("message must not be empty")
	}
	channel = xdccirc.NormalizeChannel(channel)

	m.mu.RLock()
	conn, ok := m.conns[serverID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("server %d is not connected", serverID)
	}

	conn.mu.RLock()
	ircClient := conn.irc
	_, joined := conn.joinedChs[channel]
	conn.mu.RUnlock()
	if ircClient == nil {
		return fmt.Errorf("server %d is not connected", serverID)
	}
	if !joined {
		return fmt.Errorf("not joined to channel %s", channel)
	}

	// Split on newlines (CR/LF) so each line is its own PRIVMSG. This avoids
	// embedding line breaks into a single command, which most IRC servers
	// would reject or truncate. Empty messages are already rejected by the
	// `if message == ""` check above, so we just skip intermediate blank lines.
	lines := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		m.logger.Infof("sending message to %s on %s: %q", channel, conn.address, line)
		conn.sendChannelMsg(ircClient, channel, line)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Download support
// ---------------------------------------------------------------------------

// DownloadPack performs an XDCC download using persistent IRC connections.
// This method:
// 1. Ensures connection to the target server
// 2. Uses the persistent connection's girc.Client for WHOIS/join/XDCC
// 3. Performs WHOIS to discover the bot's channel(s) if channel is empty
// 4. Joins the channel if necessary (tracks channels in joinedChs)
// 5. Sends XDCC request to the bot
// 6. Handles DCC transfer with progress callback
// 7. Maintains the connection after download (channels remain joined)
func (m *Manager) DownloadPack(ctx context.Context, pack *entities.XDCCPack, channel string, progressFn func(bytesReceived, totalBytes int64, speedBPS float64)) (string, error) {
	m.logger.Infof("DownloadPack: starting download for %s from bot %s on %s", pack.GetFilename(), pack.Bot, pack.Server.Address)

	// Find or create persistent connection for this server
	_, conn, err := m.ensureConnection(pack.Server.Address, pack.Server.Port)
	if err != nil {
		return "", fmt.Errorf("ensuring connection to %s: %w", pack.Server.Address, err)
	}

	// Get the underlying girc.Client from the persistent connection
	conn.mu.RLock()
	gircClient := conn.irc
	var joinedChs []string
	for ch := range conn.joinedChs {
		joinedChs = append(joinedChs, ch)
	}
	conn.mu.RUnlock()
	if gircClient == nil {
		return "", fmt.Errorf("persistent connection to %s has no IRC client", pack.Server.Address)
	}

	m.logger.Infof("Using persistent IRC connection for download on %s", pack.Server.Address)

	// Configure download options
	opts := xdccirc.DownloadOptions{
		ConnectTimeout:   120,
		StallTimeout:     60,
		FallbackChannel:  channel,
		ThrottleBytes:    0, // Use unlimited for now, can make configurable
		WaitTime:         1,
		ChannelJoinDelay: m.cfg.GetChannelJoinDelay(),                                      // from config: -1=random, 0=no delay, >0=fixed
		Username:         m.cfg.GetNickname(), Logger: xdccirc.LoggerFunc(m.logger.Printf), // *logging.Logger has Printf, so this works
		ProgressCallback: progressFn,
		// When the persistent connection drops and reconnects, the xdccirc.Client
		// calls this to get the new girc.Client and re-bind its handlers.
		IsChannelBlacklisted: m.cfg.IsChannelBlacklisted,
		ReconnectCallback: func() *girc.Client {
			conn.mu.RLock()
			irc := conn.irc
			conn.mu.RUnlock()
			return irc
		},
	}

	// Create xdccirc.Client and attach it to the existing persistent connection
	packSlice := []*entities.XDCCPack{pack}
	client, err := xdccirc.NewClient(ctx, packSlice, opts, 1) // verbosity=1 so WHOIS/JOIN logs appear
	if err != nil {
		return "", err
	}
	client.SetExistingClient(gircClient)
	// Remove download handlers when done to prevent accumulation on the
	// shared girc.Client across multiple downloads on the same connection.
	defer client.Cleanup()

	// Tell the client about channels the managed connection is already in
	// so it doesn't try to re-join them (which the server would silently
	// ignore, causing XDCC to never be sent).
	if len(joinedChs) > 0 {
		client.SetAlreadyJoinedChannels(joinedChs)
	}

	m.logger.Infof("DownloadPack: WHOIS → JOIN → XDCC for bot %s on %s (channel=%q, pack=%d)",
		pack.Bot, pack.Server.Address, channel, pack.PackNumber)
	results := client.DownloadAll()

	if len(results) == 0 {
		m.logger.Errorf("DownloadPack: no result from download client for bot %s on %s",
			pack.Bot, pack.Server.Address)
		return "", fmt.Errorf("no result from download client")
	}

	r := results[0]
	if r.Error != nil {
		m.logger.Errorf("DownloadPack: FAILED for bot %s on %s — %v",
			pack.Bot, pack.Server.Address, r.Error)
		return "", r.Error
	}

	// Return the downloaded file path
	filePath := pack.GetFilepath()
	if r.FilePath != "" {
		filePath = r.FilePath
	}

	m.logger.Infof("DownloadPack: SUCCESS for bot %s on %s — %s",
		pack.Bot, pack.Server.Address, filePath)
	return filePath, nil
}

// ensureConnection ensures a connection exists to the given server,
// creating one if necessary. Returns the serverID and connection.
func (m *Manager) ensureConnection(address string, port int) (int64, *managedConnection, error) {
	// Check if we already have a connection to this server
	m.mu.RLock()
	for id, conn := range m.conns {
		if conn.address != address || conn.port != port {
			continue
		}
		m.mu.RUnlock()
		// Connection exists
		if conn.Status() == "connected" {
			m.logger.Infof("Reusing existing connection to %s:%d", address, port)
			return id, conn, nil
		}
		// Connection exists but not connected yet — wait efficiently
		m.logger.Infof("Waiting for connection to %s:%d to establish...", address, port)
		if !conn.waitConnected(defaultConnectionTimeout) {
			return 0, nil, fmt.Errorf("connection to %s:%d did not establish in time", address, port)
		}
		return id, conn, nil
	}
	m.mu.RUnlock()

	// No connection exists - create one
	m.logger.Infof("Creating new persistent connection to %s:%d", address, port)

	// Check if server exists in database
	servers, err := m.store.ListServers(m.ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("listing servers: %w", err)
	}

	var serverID int64
	var found bool
	for _, s := range servers {
		if s.Address == address && s.Port == port {
			serverID = s.ID
			found = true
			break
		}
	}

	if !found {
		// Add server to database
		serverID, err = m.store.AddServer(m.ctx, store.ServerRecord{
			Address:     address,
			Port:        port,
			AutoConnect: false, // Don't auto-connect on restart
			Status:      "disconnected",
		})
		if err != nil {
			return 0, nil, fmt.Errorf("adding server to database: %w", err)
		}
		m.logger.Infof("Added new server %s:%d to database (ID: %d)", address, port, serverID)
	}

	// Connect to the server
	if err := m.ConnectServerByID(serverID); err != nil {
		return 0, nil, fmt.Errorf("connecting to server: %w", err)
	}

	// Wait for connection to establish using notification channel.
	// Re-acquire lock to fetch the connection safely: ConnectServerByID
	// inserted it into m.conns, but a concurrent DisconnectServer could
	// have removed it before we acquire the lock.
	m.logger.Infof("Waiting for connection to %s:%d to establish...", address, port)
	m.mu.RLock()
	conn, ok := m.conns[serverID]
	m.mu.RUnlock()

	if !ok || conn == nil {
		return 0, nil, fmt.Errorf("connection to %s:%d was removed before it established", address, port)
	}

	if !conn.waitConnected(defaultConnectionTimeout) {
		return 0, nil, fmt.Errorf("connection to %s:%d did not establish in time", address, port)
	}

	m.logger.Infof("Connection to %s:%d established successfully", address, port)
	return serverID, conn, nil
}

// ---------------------------------------------------------------------------
// managedConnection
// ---------------------------------------------------------------------------

// managedConnection manages a single persistent IRC connection.
// It handles reconnection with exponential backoff and tracks channel state.
type managedConnection struct {
	id          int64
	address     string
	port        int
	nickname    string
	autoJoinChs []string // channels to auto-join on (re)connect

	mu         sync.RWMutex
	status     string            // disconnected, connecting, connected, reconnecting
	joinedChs  map[string]string // channel name -> topic
	retryCount int
	backoffIdx int

	irc *girc.Client

	// connectedCh is closed when the IRC connection is established.
	// Used by ensureConnection to wait efficiently instead of polling.
	connectedCh chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	// Lifecycle management with WaitGroup pattern (prevents race conditions)
	wg        sync.WaitGroup // Tracks active run() goroutine
	runningMu sync.Mutex     // Protects isRunning field
	isRunning bool           // Prevents duplicate run() calls

	// startedCh is closed by run() once wg.Add(1) has been called, so
	// that ConnectServer (which waits on it) can be sure that any
	// subsequent wg.Wait() will observe the counter already incremented.
	startedCh   chan struct{}
	startedOnce sync.Once

	manager *Manager
}

func (mc *managedConnection) Status() string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return mc.status
}

// signalStarted closes startedCh exactly once, unblocking any caller
// waiting for run() to register itself in the WaitGroup.
// Safe to call from both the normal start path and the early-return guard.
func (mc *managedConnection) signalStarted() {
	mc.startedOnce.Do(func() {
		if mc.startedCh != nil {
			close(mc.startedCh)
		}
	})
}

// IsRunning returns whether the run() goroutine is currently active.
// Useful for testing and debugging lifecycle management.
func (mc *managedConnection) IsRunning() bool {
	mc.runningMu.Lock()
	defer mc.runningMu.Unlock()
	return mc.isRunning
}

func (mc *managedConnection) setStatus(s string) {
	mc.mu.Lock()
	mc.status = s
	mc.mu.Unlock()
}

// closeTrackedClient clears mc.irc under lock and closes the tracked client.
// If no tracked client exists, it closes fallback (if provided).
func (mc *managedConnection) closeTrackedClient(fallback *girc.Client, reason string) {
	mc.mu.Lock()
	tracked := mc.irc
	mc.irc = nil
	mc.mu.Unlock()

	if tracked != nil {
		if mc.manager != nil && mc.manager.logger != nil && reason != "" {
			mc.manager.logger.Infof("closing IRC client for server %d (%s)", mc.id, reason)
		}
		tracked.Close()
		return
	}

	if fallback != nil {
		if mc.manager != nil && mc.manager.logger != nil && reason != "" {
			mc.manager.logger.Infof("closing IRC client for server %d (%s)", mc.id, reason)
		}
		fallback.Close()
	}
}

// activeGreetingClient returns the current IRC client only if it still matches
// the expected pointer and the connection is live.
func (mc *managedConnection) activeGreetingClient(expected *girc.Client) *girc.Client {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if expected == nil || mc.status != "connected" || mc.irc == nil || mc.irc != expected {
		return nil
	}
	return mc.irc
}

// connectResult represents the outcome of a connection attempt.
type connectResult int

const (
	connectResultExplicitCancel connectResult = iota // User requested disconnect
	connectResultInitialFailure                      // Failed on first attempt
	connectResultDropped                             // Connection dropped after being established
)

// run is the main loop for a managed connection. It connects, waits for
// disconnection, then reconnects with exponential backoff unless explicitly cancelled.
// This method is goroutine-safe and prevents duplicate invocations.
func (mc *managedConnection) run() {
	// Prevent duplicate run() calls - critical for lifecycle safety
	mc.runningMu.Lock()
	if mc.isRunning {
		mc.manager.logger.Warnf("run() already active for server %d, skipping duplicate call", mc.id)
		mc.runningMu.Unlock()
		// Signal startedCh defensively: unblocks any caller waiting on it
		// even though this is a duplicate invocation (no-op if already done).
		mc.signalStarted()
		return
	}
	mc.isRunning = true
	// wg.Add(1) is called here — not by the caller — so that Add and Done
	// are always paired inside run() itself. signalStarted() is invoked
	// immediately after to unblock ConnectServer (which waits on startedCh).
	mc.wg.Add(1)
	mc.runningMu.Unlock()

	mc.signalStarted()
	defer mc.wg.Done()

	// Ensure cleanup on exit
	defer func() {
		mc.runningMu.Lock()
		mc.isRunning = false
		mc.runningMu.Unlock()

		// Panic recovery to prevent goroutine crash
		if r := recover(); r != nil {
			mc.manager.logger.Errorf("PANIC in run() for server %d: %v\n%s", mc.id, r, debug.Stack())
			mc.setStatus("error")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := mc.manager.store.SetServerStatus(shutdownCtx, mc.id, "error"); err != nil {
				mc.manager.logger.Warnf("set server %d status=error in DB failed: %v", mc.id, err)
			}
			cancel()

			// Clean up IRC client resources to prevent goroutine/channel leaks.
			mc.closeTrackedClient(nil, "panic recovery")
		}
	}()

	// Helper: final disconnect cleanup. Uses a timeout context because
	// mc.ctx is already cancelled when this runs (conn.disconnect() cancels
	// it), and a bare context.Background() would let store operations
	// block indefinitely during shutdown.
	disconnectCleanup := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mc.manager.store.SetServerStatus(shutdownCtx, mc.id, "disconnected"); err != nil {
			mc.manager.logger.Warnf("set server %d status=disconnected in DB failed: %v", mc.id, err)
		}
		mc.manager.emitEvent(Event{
			Type:       EventServerDisconnected,
			ServerID:   mc.id,
			ServerAddr: mc.address,
		})
	}

	for {
		result := mc.connect()
		if mc.ctx.Err() != nil {
			disconnectCleanup()
			return
		}

		// Handle result based on disconnect reason
		switch result {
		case connectResultExplicitCancel:
			disconnectCleanup()
			return
		case connectResultInitialFailure, connectResultDropped:
			// Automatic reconnect for failures and drops
			mc.manager.logger.Infof("IRC connection to %s lost, reconnecting...", mc.address)
			mc.manager.emitEvent(Event{
				Type:       EventServerDisconnected,
				ServerID:   mc.id,
				ServerAddr: mc.address,
			})
			if !mc.reconnectBackoff() {
				// If context was cancelled (user disconnected), clean up as disconnected.
				// If context is still alive, max retries were exceeded — status is
				// already "failed" in DB, just return without overwriting it.
				if mc.ctx.Err() != nil {
					disconnectCleanup()
				}
				return
			}
		}
	}
}

// connect establishes a single IRC connection and returns the disconnect reason.
func (mc *managedConnection) connect() connectResult {
	// Clear stale channel state before new connection attempt
	mc.mu.Lock()
	mc.joinedChs = make(map[string]string)
	mc.irc = nil
	mc.mu.Unlock()

	nick := mc.nickname + randomSuffix(3)
	mc.setStatus("connecting")

	mc.manager.logger.Infof("connecting to %s:%d as '%s'", mc.address, mc.port, nick)

	client := girc.New(girc.Config{
		Server:      mc.address,
		Port:        mc.port,
		Nick:        nick,
		User:        nick,
		Name:        nick,
		PingDelay:   30 * time.Second,
		PingTimeout: 60 * time.Second,
	})

	connected := make(chan struct{})
	disconnected := make(chan error, 1)

	// Ensure the connected channel is closed exactly once when connect()
	// exits, whether by successful CONNECTED event (handler closes it)
	// or by failure/cancellation (defer closes it). This prevents
	// waitConnected() from blocking forever on a channel that will
	// never receive a close signal.
	var closeConnectedOnce sync.Once
	closeConnected := func() { close(connected) }

	// Expose the connected channel so ensureConnection can wait on it
	// without busy-polling Status().
	mc.mu.Lock()
	mc.connectedCh = connected
	mc.mu.Unlock()
	defer func() {
		mc.mu.Lock()
		mc.connectedCh = nil
		mc.mu.Unlock()
		closeConnectedOnce.Do(closeConnected)
	}()

	// Register handlers
	client.Handlers.Add(girc.CONNECTED, func(cl *girc.Client, e girc.Event) {
		mc.mu.Lock()
		mc.status = "connected"
		mc.retryCount = 0
		mc.backoffIdx = 0
		mc.irc = cl
		mc.mu.Unlock()

		mc.manager.logger.Infof("connected to %s:%d", mc.address, mc.port)

		// Update DB
		if err := mc.manager.store.SetServerConnected(mc.ctx, mc.id); err != nil {
			mc.manager.logger.Warnf("updating server %d status failed: %v", mc.id, err)
		}

		// Emit event
		mc.manager.emitEvent(Event{
			Type:       EventServerConnected,
			ServerID:   mc.id,
			ServerAddr: mc.address,
		})

		// Auto-join channels (skip blacklisted).
		// Take a snapshot under RLock to avoid racing with concurrent
		// joinChannel/leaveChannel (HTTP handler goroutines) that write
		// to autoJoinChs under mc.mu.Lock().
		mc.mu.RLock()
		chsSnapshot := make([]string, len(mc.autoJoinChs))
		copy(chsSnapshot, mc.autoJoinChs)
		mc.mu.RUnlock()
		for _, ch := range chsSnapshot {
			if mc.manager.cfg.IsChannelBlacklisted(ch) {
				mc.manager.logger.Infof("skipping auto-join of blacklisted channel %q on %s", ch, mc.address)
				continue
			}
			cl.Cmd.Join(ch)
		}

		closeConnectedOnce.Do(closeConnected)
	})

	client.Handlers.Add(girc.JOIN, func(cl *girc.Client, e girc.Event) {
		if e.Source == nil || !isOwnNick(e.Source.Name, cl.GetNick()) {
			return
		}
		ch := xdccirc.NormalizeChannel(e.Params[0])
		mc.manager.logger.Infof("joined channel %s on %s", ch, mc.address)
		mc.mu.Lock()
		mc.joinedChs[ch] = "" // topic will be updated by TOPIC event
		mc.mu.Unlock()

		// Update DB: mark existing channel as joined, or create it if it was
		// joined automatically (e.g. via WHOIS during a download).
		channels, err := mc.manager.store.GetChannelsByServerAndName(mc.ctx, mc.id, ch)
		if err == nil && channels != nil {
			if err := mc.manager.store.SetChannelJoined(mc.ctx, channels.ID, true); err != nil {
				mc.manager.logger.Warnf("mark channel %s joined in DB failed: %v", ch, err)
			}
		} else if err == nil {
			_, err = mc.manager.store.AddChannel(mc.ctx, store.ChannelRecord{
				ServerID: mc.id,
				Name:     ch,
				AutoJoin: false,
				Joined:   true,
			})
			if err != nil {
				mc.manager.logger.Warnf("failed to add joined channel %s to DB: %v", ch, err)
			}
		} else {
			mc.manager.logger.Warnf("looking up channel %s in DB failed: %v", ch, err)
		}

		mc.manager.emitEvent(Event{
			Type:       EventChannelJoined,
			ServerID:   mc.id,
			ServerAddr: mc.address,
			Channel:    ch,
		})

		// Send a greeting to the channel after a small random delay (2-4s).
		// This makes the bot look more human and avoids triggering IRC
		// anti-flood protections when many channels are joined in a burst.
		// Runs in its own goroutine so the JOIN handler returns immediately
		// and the IRC event loop is not blocked.
		mc.sendChannelGreeting(cl, ch)
	})

	client.Handlers.Add(girc.KICK, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 2 {
			return
		}
		if !isOwnNick(e.Params[1], cl.GetNick()) {
			return
		}
		ch := xdccirc.NormalizeChannel(e.Params[0])
		mc.manager.logger.Infof("kicked from channel %s on %s", ch, mc.address)
		mc.mu.Lock()
		delete(mc.joinedChs, ch)
		mc.mu.Unlock()

		channels, err := mc.manager.store.GetChannelsByServerAndName(mc.ctx, mc.id, ch)
		if err == nil && channels != nil {
			if err := mc.manager.store.SetChannelJoined(mc.ctx, channels.ID, false); err != nil {
				mc.manager.logger.Warnf("mark channel %s not joined in DB failed: %v", ch, err)
			}
		}

		mc.manager.emitEvent(Event{
			Type:       EventChannelLeft,
			ServerID:   mc.id,
			ServerAddr: mc.address,
			Channel:    ch,
		})
	})

	client.Handlers.Add(girc.PART, func(cl *girc.Client, e girc.Event) {
		if e.Source == nil || !isOwnNick(e.Source.Name, cl.GetNick()) {
			return
		}
		ch := xdccirc.NormalizeChannel(e.Params[0])
		mc.manager.logger.Infof("left channel %s on %s", ch, mc.address)
		mc.mu.Lock()
		delete(mc.joinedChs, ch)
		mc.mu.Unlock()

		channels, err := mc.manager.store.GetChannelsByServerAndName(mc.ctx, mc.id, ch)
		if err == nil && channels != nil {
			if err := mc.manager.store.SetChannelJoined(mc.ctx, channels.ID, false); err != nil {
				mc.manager.logger.Warnf("mark channel %s not joined in DB failed: %v", ch, err)
			}
		}

		mc.manager.emitEvent(Event{
			Type:       EventChannelLeft,
			ServerID:   mc.id,
			ServerAddr: mc.address,
			Channel:    ch,
		})
	})

	client.Handlers.Add(girc.TOPIC, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 1 {
			return
		}
		ch := xdccirc.NormalizeChannel(e.Params[0])
		topic := stripIRCFormatting(e.Last())
		mc.mu.Lock()
		mc.joinedChs[ch] = topic
		mc.mu.Unlock()

		channels, err := mc.manager.store.GetChannelsByServerAndName(mc.ctx, mc.id, ch)
		if err == nil && channels != nil {
			if err := mc.manager.store.UpdateChannelTopic(mc.ctx, channels.ID, topic); err != nil {
				mc.manager.logger.Warnf("update channel topic in DB failed: %v", err)
			}
		}

		mc.manager.emitEvent(Event{
			Type:       EventChannelTopicUpdated,
			ServerID:   mc.id,
			ServerAddr: mc.address,
			Channel:    ch,
			Topic:      topic,
		})
	})

	client.Handlers.Add(girc.RPL_TOPIC, func(cl *girc.Client, e girc.Event) {
		if len(e.Params) < 3 {
			return
		}
		ch := xdccirc.NormalizeChannel(e.Params[1])
		topic := stripIRCFormatting(e.Params[len(e.Params)-1])
		mc.mu.Lock()
		mc.joinedChs[ch] = topic
		mc.mu.Unlock()
	})

	client.Handlers.Add(girc.ERROR, func(cl *girc.Client, e girc.Event) {
		mc.manager.logger.Warnf("IRC error on %s: %s", mc.address, e.Last())
	})

	// Per-channel PRIVMSG / NOTICE logger (hidden, opt-in via irc.channel_log).
	// Only records traffic addressed to a channel we are joined to and that
	// appears in the configured log list. Private messages (target = our own
	// nick) are intentionally skipped to avoid leaking direct queries.
	mc.registerChannelLogHandlers(client)

	// Start connection in a goroutine; when irc.Connect() returns,
	// the connection has been lost (either due to error or explicit Close).
	// Tracked by mc.wg so DisconnectServer can detect leaked goroutines.
	mc.wg.Add(1)
	go func() {
		defer mc.wg.Done()
		err := client.Connect()
		disconnected <- err
		close(disconnected)
	}()

	// Phase 1: Wait for CONNECTED event or immediate failure
	select {
	case <-mc.ctx.Done():
		mc.closeTrackedClient(client, "context cancelled during connect")
		// Drain with timeout to prevent indefinite blocking
		select {
		case <-disconnected:
		case <-time.After(5 * time.Second):
			mc.manager.logger.Warnf("client.Close() for %s did not complete within 5s", mc.address)
		}
		return connectResultExplicitCancel
	case <-connected:
		// Connected successfully — proceed to Phase 2
	case err := <-disconnected:
		// Connection failed on first attempt.
		mc.manager.logger.Errorf("connection to %s failed: %v", mc.address, err)
		// Use a timeout context because mc.ctx might already be cancelled
		// if a concurrent DisconnectServer() called disconnect() while the
		// connection was still being established.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := mc.manager.store.IncrementServerRetry(shutdownCtx, mc.id); err != nil {
			mc.manager.logger.Warnf("increment retry count for server %d in DB failed: %v", mc.id, err)
		}
		cancel()
		return connectResultInitialFailure
	}

	// Phase 2: Connection is established. Wait for disconnection or cancellation.
	select {
	case <-mc.ctx.Done():
		// Explicit disconnect requested — send QUIT, close, then drain with timeout
		mc.closeTrackedClient(client, "explicit disconnect")
		// Drain with timeout to prevent indefinite blocking
		select {
		case <-disconnected:
		case <-time.After(5 * time.Second):
			mc.manager.logger.Warnf("client.Close() for %s did not complete within 5s (phase 2)", mc.address)
		}
		return connectResultExplicitCancel
	case err := <-disconnected:
		// Connection dropped — will trigger reconnect in run()
		if err != nil {
			mc.manager.logger.Infof("connection to %s lost: %v", mc.address, err)
		}
		return connectResultDropped
	}
}

// reconnectBackoff implements exponential backoff (Fase 3.4).
// Returns false if the context was cancelled or max retries exceeded.
func (mc *managedConnection) reconnectBackoff() bool {
	mc.mu.Lock()
	mc.status = "reconnecting"
	mc.retryCount++
	idx := mc.backoffIdx
	if idx < 5 {
		mc.backoffIdx++
	}
	retries := mc.retryCount
	mc.mu.Unlock()

	// Check max retries before scheduling
	if retries > maxReconnectRetries {
		mc.manager.logger.Errorf("max reconnection attempts (%d) exceeded for %s — giving up", maxReconnectRetries, mc.address)
		mc.setStatus("failed")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := mc.manager.store.SetServerStatus(shutdownCtx, mc.id, "failed"); err != nil {
			mc.manager.logger.Warnf("set server %d status=failed in DB failed: %v", mc.id, err)
		}
		cancel()
		return false
	}

	// Notify the manager to update DB
	if err := mc.manager.store.SetServerStatus(mc.ctx, mc.id, "reconnecting"); err != nil {
		mc.manager.logger.Warnf("set server %d status=reconnecting in DB failed: %v", mc.id, err)
	}

	mc.manager.emitEvent(Event{
		Type:       EventServerReconnecting,
		ServerID:   mc.id,
		ServerAddr: mc.address,
	})

	// Calculate backoff delay
	delays := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second}
	var delay time.Duration
	if idx < len(delays) {
		delay = delays[idx]
	} else {
		delay = 1 * time.Hour // after 5 failures, retry every hour
	}

	mc.manager.logger.Infof("reconnecting to %s in %v (attempt %d)", mc.address, delay, retries)

	select {
	case <-mc.ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

// disconnect tears down the connection gracefully.
func (mc *managedConnection) disconnect() {
	mc.setStatus("disconnected")
	if mc.cancel != nil {
		mc.cancel()
	}
}

// waitConnected waits for the managed connection to reach "connected"
// status. Uses the connectedCh notification channel when available,
// with a short polling fallback. Returns true if connected within the
// timeout, false otherwise.
func (mc *managedConnection) waitConnected(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for {
		mc.mu.RLock()
		ch := mc.connectedCh
		ready := mc.status == "connected" && mc.irc != nil
		mc.mu.RUnlock()

		if ready {
			return true
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}

		if ch == nil {
			sleepFor := waitConnectedPollInterval
			if remaining < sleepFor {
				sleepFor = remaining
			}
			time.Sleep(sleepFor)
			continue
		}

		timer := time.NewTimer(remaining)
		select {
		case <-ch:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}

			mc.mu.RLock()
			readyAfterSignal := mc.status == "connected" && mc.irc != nil
			sameSignal := mc.connectedCh == ch
			mc.mu.RUnlock()
			if readyAfterSignal {
				return true
			}
			if sameSignal {
				sleepFor := waitConnectedPollInterval
				if remaining < sleepFor {
					sleepFor = remaining
				}
				time.Sleep(sleepFor)
			}
		case <-timer.C:
			return false
		}
	}
}

// sendChannelMsg sends a PRIVMSG to the given channel via the girc client
// and also logs it to the channel log file (if channel logging is enabled).
// girc's PRIVMSG handler only fires for incoming messages, so outgoing ones
// would otherwise not appear in the channel log.
func (mc *managedConnection) sendChannelMsg(cl *girc.Client, channel, message string) {
	cl.Cmd.Message(channel, message)

	if mc.manager.chlog != nil && mc.manager.cfg != nil &&
		mc.manager.cfg.IsChannelLogged(channel) {
		nick := cl.GetNick()
		mc.manager.chlog.Log(channel, nick, channellog.KindMessage, message)
	}
}

// sendChannelGreeting sends a random greeting message to the given channel
// after a random delay between 2 and 4 seconds. The greeting is picked from
// the configured greetings list (config.IRC.Greetings); when the list is empty
// the built-in default "hello everybody" is used.
//
// Runs in its own goroutine so the caller (JOIN handler) returns immediately
// and the IRC event loop is never blocked. The goroutine is also cancelled
// when the connection is torn down, so it cannot outlive the IRC client it
// would otherwise send a message through.
func (mc *managedConnection) sendChannelGreeting(cl *girc.Client, channel string) {
	if cl == nil {
		return
	}
	go func() {
		// Random delay between 2 and 4 seconds, inclusive.
		// crypto/rand is used (instead of math/rand) to be safe under
		// concurrent calls from multiple goroutines.
		delaySec, err := randIntRange(2, 4)
		if err != nil {
			// Fall back to a fixed 3s delay on the (extremely rare) rand
			// failure so we never skip the greeting entirely.
			delaySec = 3
		}
		timer := time.NewTimer(time.Duration(delaySec) * time.Second)
		defer timer.Stop()

		select {
		case <-mc.ctx.Done():
			return
		case <-timer.C:
		}

		// Pick the greeting. We do this after the delay so changes to the
		// configured list take effect for the next channel joined.
		greeting := mc.manager.cfg.PickGreeting()

		// Re-check context cancellation right before sending, since the
		// connection could have been torn down while the timer was ticking.
		select {
		case <-mc.ctx.Done():
			return
		default:
		}

		sendClient := mc.activeGreetingClient(cl)
		if sendClient == nil {
			// The connection changed or dropped while waiting.
			return
		}

		mc.manager.logger.Infof("sending greeting to %s on %s: %q", channel, mc.address, greeting)
		mc.sendChannelMsg(sendClient, channel, greeting)
	}()
}

// registerChannelLogHandlers installs PRIVMSG, NOTICE, and private message
// handlers that write message traffic to per-channel or private log files.
//
// All handlers are registered when chlog is non-nil (i.e. channel logging was
// possible at startup). Each handler reads the *current* config on every event
// invocation, so runtime config changes via /api/config (e.g. adding channels
// to channel_log, toggling log_private_messages) take effect immediately
// without requiring reconnection.
func (mc *managedConnection) registerChannelLogHandlers(cl *girc.Client) {
	if mc.manager.chlog == nil || mc.manager.cfg == nil {
		return
	}

	// Channel PRIVMSG handler — logs messages to per-channel files.
	// Reads cfg.IRC.ChannelLog dynamically on each event.
	privHandler := func(_ *girc.Client, e girc.Event) {
		if len(e.Params) == 0 {
			return
		}
		target := e.Params[0]
		if target == "" || (target[0] != '#' && target[0] != '&') {
			return
		}
		target = xdccirc.NormalizeChannel(target)
		if !mc.manager.cfg.IsChannelLogged(target) {
			return
		}
		sender := ""
		if e.Source != nil {
			sender = e.Source.Name
		}
		mc.manager.chlog.Log(target, sender, channellog.KindMessage, e.Last())
	}

	// Channel NOTICE handler — logs notices to per-channel files.
	noticeHandler := func(_ *girc.Client, e girc.Event) {
		if len(e.Params) == 0 {
			return
		}
		target := e.Params[0]
		if target == "" || (target[0] != '#' && target[0] != '&') {
			return
		}
		target = xdccirc.NormalizeChannel(target)
		if !mc.manager.cfg.IsChannelLogged(target) {
			return
		}
		sender := ""
		if e.Source != nil {
			sender = e.Source.Name
		}
		mc.manager.chlog.Log(target, sender, channellog.KindNotice, e.Last())
	}

	// Private message handler — logs DMs to private.log.
	// Reads cfg.IRC.LogPrivateMessages dynamically on each event.
	privMsgHandler := func(_ *girc.Client, e girc.Event) {
		if !mc.manager.cfg.GetLogPrivateMessages() {
			return
		}
		if len(e.Params) == 0 {
			return
		}
		target := e.Params[0]
		if target == "" {
			return
		}
		// Skip channel messages — those are handled by privHandler above.
		if target[0] == '#' || target[0] == '&' {
			return
		}
		sender := ""
		if e.Source != nil {
			sender = e.Source.Name
		}
		mc.manager.chlog.LogPrivate(mc.address, sender, e.Last())
	}

	privCUID := cl.Handlers.Add(girc.PRIVMSG, privHandler)
	mc.manager.logger.Debugf("channellog: registered PRIVMSG handler (CUID=%s) for %s", privCUID, mc.address)
	noticeCUID := cl.Handlers.Add(girc.NOTICE, noticeHandler)
	mc.manager.logger.Debugf("channellog: registered NOTICE handler (CUID=%s) for %s", noticeCUID, mc.address)
	privMsgCUID := cl.Handlers.Add(girc.PRIVMSG, privMsgHandler)
	mc.manager.logger.Debugf("channellog: registered private message handler (CUID=%s) for %s", privMsgCUID, mc.address)
}

// randIntRange returns a pseudo-random integer in the inclusive range [lo, hi].
// Uses crypto/rand so it is safe under concurrent invocation.
func randIntRange(lo, hi int) (int, error) {
	if hi < lo {
		lo, hi = hi, lo
	}
	span := int64(hi - lo + 1)
	n, err := rand.Int(rand.Reader, big.NewInt(span))
	if err != nil {
		return 0, err
	}
	return int(n.Int64()) + lo, nil
}

// joinChannel sends a JOIN command for the given channel.
func (mc *managedConnection) joinChannel(channel string) error {
	if mc.manager.cfg.IsChannelBlacklisted(channel) {
		return fmt.Errorf("channel %q is blacklisted", channel)
	}

	mc.mu.RLock()
	client := mc.irc
	mc.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("not connected")
	}

	// Normalize channel name
	channel = xdccirc.NormalizeChannel(channel)

	// Persist to DB: create or update channel record
	existingCh, err := mc.manager.store.GetChannelsByServerAndName(mc.ctx, mc.id, channel)
	if err != nil || existingCh == nil {
		// Channel doesn't exist — create it
		_, err = mc.manager.store.AddChannel(mc.ctx, store.ChannelRecord{
			ServerID: mc.id,
			Name:     channel,
			AutoJoin: true,
			Joined:   false, // Will be set to true by JOIN handler
		})
		if err != nil {
			mc.manager.logger.Warnf("failed to add channel %s to DB: %v", channel, err)
		}
	} else {
		// Channel exists — update auto_join to true
		existingCh.AutoJoin = true
		if err := mc.manager.store.UpdateChannel(mc.ctx, *existingCh); err != nil {
			mc.manager.logger.Warnf("failed to update channel %s in DB: %v", channel, err)
		}
	}

	// Send JOIN command to IRC
	mc.manager.logger.Infof("joining channel %s on %s (server_id=%d)", channel, mc.address, mc.id)
	client.Cmd.Join(channel)

	// Add to auto-join for reconnection (in-memory)
	mc.mu.Lock()
	found := false
	for _, ch := range mc.autoJoinChs {
		if ch == channel {
			found = true
			break
		}
	}
	if !found {
		mc.autoJoinChs = append(mc.autoJoinChs, channel)
	}
	mc.mu.Unlock()

	return nil
}

// leaveChannel sends a PART command for the given channel.
func (mc *managedConnection) leaveChannel(channel string) error {
	mc.mu.RLock()
	client := mc.irc
	mc.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("not connected")
	}

	// Normalize channel name
	channel = xdccirc.NormalizeChannel(channel)

	// Remove from in-memory joined state immediately so GetChannels()
	// reflects the change even before the server responds.
	mc.mu.Lock()
	delete(mc.joinedChs, channel)
	mc.mu.Unlock()

	// Update DB: set auto_join=false and joined=false
	existingCh, err := mc.manager.store.GetChannelsByServerAndName(mc.ctx, mc.id, channel)
	if err == nil && existingCh != nil {
		existingCh.AutoJoin = false
		existingCh.Joined = false
		if err := mc.manager.store.UpdateChannel(mc.ctx, *existingCh); err != nil {
			mc.manager.logger.Warnf("failed to update channel %s in DB: %v", channel, err)
		}
	}

	// Send PART command to IRC
	mc.manager.logger.Infof("leaving channel %s on %s (server_id=%d)", channel, mc.address, mc.id)
	client.Cmd.Part(channel)

	// Remove from auto-join list (in-memory)
	mc.mu.Lock()
	for i, ch := range mc.autoJoinChs {
		if ch == channel {
			mc.autoJoinChs = append(mc.autoJoinChs[:i], mc.autoJoinChs[i+1:]...)
			break
		}
	}
	mc.mu.Unlock()

	return nil
}
