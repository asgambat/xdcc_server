// Package irc implements the XDCC IRC client using the girc library.
// A single Client can download multiple packs sequentially on the same IRC
// connection, rejoining channels only when needed.
package irc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xdcc_server/internal/entities"

	"github.com/lrstanley/girc"
)

// Internal constants
const (
	ackQueueBufSize = 256             // capacity of the ACK send queue
	packDelay       = 3 * time.Second // delay between pack downloads

	// connectGracePeriod is extra time added to ConnectTimeout to account for
	// IRC handshake, WHOIS response, and channel JOIN after the TCP connection
	// is established. Without this buffer, fast connections would time out
	// while waiting for post-CONNECT server responses.
	connectGracePeriod = 30 * time.Second
)

// ---------------------------------------------------------------------------
// packState — per-pack mutable state (reset via resetForPack between packs)
// ---------------------------------------------------------------------------

// packState holds all state that is reset between consecutive pack downloads.
// Fields are grouped by their lifecycle: channels/sync primitives, DCC I/O
// state, bot communication state, and download lifecycle flags.
type packState struct {
	// --- Synchronization and channels (recreated per pack) ---
	mu                  sync.Mutex
	ackQueue            chan []byte
	downloadDone        chan struct{} // closed when pack finishes (success or error)
	downloadStarted     chan struct{} // closed when DCC TCP connection is established
	downloadDoneOnce    *sync.Once
	downloadStartedOnce *sync.Once

	// --- DCC transfer state ---
	peerAddr      string // stored on DCC SEND, reused on DCC ACCEPT
	dccConn       net.Conn
	dccFile       *os.File
	progress      int64     // use atomic access (StoreInt64/LoadInt64)
	filesize      int64     // expected total bytes (protected by mu)
	dccTimestamp  time.Time // last progress snapshot time (throttle)
	downloading   bool      // true while receiveData is running (protected by mu)
	downloadError error     // first error captured (protected by mu)
	downStartTime time.Time // when the DCC TCP transfer began

	// --- Bot communication ---
	lastBotNotice string // last NOTICE from the bot for this pack
	packFilename  string // discovered from bot notice before DCC SEND
	packSize      int64  // discovered from bot notice before DCC SEND

	// --- Lifecycle atomics (per-pack) ---
	messageSent        atomic.Bool // true after XDCC request has been sent
	whoisFoundChannels atomic.Bool // WHOIS found at least one channel
	needsJoin          atomic.Bool // we sent a JOIN and must wait for confirmation

	// --- Stall detection ---
	lastActivity atomic.Int64 // unix nanoseconds of last received byte
}

// ---------------------------------------------------------------------------
// Connection — IRC connection state (persists across packs)
// ---------------------------------------------------------------------------

// Connection holds the state of a single IRC connection that can be reused
// across multiple pack downloads. Fields in Connection are NOT reset between
// packs; they represent the persistent connection lifecycle.
type Connection struct {
	irc                  *girc.Client
	ircErrCh             chan error      // receives error from irc.Connect() goroutine
	connectedCh          chan struct{}   // closed on CONNECTED event
	joinedChannels       map[string]bool // channels joined in this connection (cleared on reconnect)
	handlersRegisteredOn *girc.Client    // which girc.Client handlers are currently bound to
	connectTime          time.Time

	// When true, the client uses an existing connection managed externally
	// (e.g. by ircmanager). connect() is skipped; the caller must call
	// SetExistingClient() to provide the girc.Client before DownloadAll().
	usingExistingConn bool

	// Handler CUIDs registered on the girc.Client. Tracked so handlers can be
	// removed when the download completes, preventing accumulation of duplicate
	// handlers on persistent connections shared across multiple downloads.
	handlerCUIDs []string

	// whoisFallbackTimer is a reusable timer for the WHOIS→JOIN fallback.
	// Instead of spawning a new timer (via time.After) on each WHOIS response
	// — which can accumulate goroutines in burst scenarios — we reuse a single
	// timer per Connection. The timer is stopped and drained in resetForPack().
	whoisFallbackTimer *time.Timer
}

// ---------------------------------------------------------------------------
// Client — orchestrates multiple pack downloads on a shared IRC connection
// ---------------------------------------------------------------------------

// Client manages the download of one or more XDCC packs on a single IRC
// connection. Packs on the same server are downloaded without disconnecting;
// channels already joined are not rejoined.
//
// The Client holds a Connection (persistent IRC state) and replaces its
// packState for each pack. Fields not in Connection or packState are
// configuration / index state that persists for the Client lifetime.
type Client struct {
	ctx       context.Context
	packs     []*entities.XDCCPack
	opts      DownloadOptions
	verbosity int // 0=normal, 1=verbose, 2=debug, -1=quiet
	logger    Logger

	conn *Connection

	// Current pack index (set before each pack download)
	packIdxVal atomic.Int32

	// Per-pack state (replaced via resetForPack between packs)
	ps *packState
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewClient creates a new XDCC Client that will download all packs in order.
// packs must all belong to the same IRC server.
// verbosity: -1=quiet, 0=normal, 1=verbose (-v), 2=debug (-vv).
func NewClient(ctx context.Context, packs []*entities.XDCCPack, opts DownloadOptions, verbosity int) *Client {
	if opts.ChannelJoinDelay < 0 {
		opts.ChannelJoinDelay = randN(6) + 5
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 120
	}
	if opts.StallTimeout < 0 {
		opts.StallTimeout = 0
	}
	if opts.DNSServer == "" {
		opts.DNSServer = "8.8.8.8:53"
	}
	logger := opts.Logger
	if logger == nil {
		logger = defaultLogger()
	}
	return &Client{
		ctx:       ctx,
		packs:     packs,
		opts:      opts,
		verbosity: verbosity,
		logger:    logger,
		conn:      &Connection{},
		ps:        &packState{}, // non-nil so methods like SetAlreadyJoinedChannels are safe before resetForPack
	}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// SetExistingClient configures the client to use an already-established IRC
// connection instead of creating its own. The caller is responsible for
// managing the connection lifecycle (e.g. the ircmanager keeps the connection
// alive after the download completes).
//
// Must be called before DownloadAll().
func (c *Client) SetExistingClient(irc *girc.Client) {
	c.conn.usingExistingConn = true
	c.conn.irc = irc
	c.conn.connectedCh = make(chan struct{})
	c.conn.joinedChannels = make(map[string]bool)
	c.conn.ircErrCh = make(chan error, 1)

	// Always remove previously registered handlers before registering new ones,
	// even when reusing the same girc.Client. This prevents accumulation of
	// duplicate handlers across multiple downloads on persistent connections.
	if c.conn.handlersRegisteredOn != nil {
		c.removeHandlers()
	}

	c.registerHandlers()
	c.conn.handlersRegisteredOn = irc
	close(c.conn.connectedCh) // Already connected — signal immediately
}

// SetAlreadyJoinedChannels informs the client about channels that are already
// joined on the persistent connection (e.g. auto-join channels). This prevents
// the client from sending duplicate JOIN commands that the server would ignore,
// which would cause the XDCC request to never be sent.
//
// Must be called after SetExistingClient and before DownloadAll().
func (c *Client) SetAlreadyJoinedChannels(channels []string) {
	// No lock needed: called before DownloadAll() starts any goroutines.
	for _, ch := range channels {
		c.conn.joinedChannels[strings.ToLower(ch)] = true
	}
}

// DownloadAll downloads all packs sequentially, reusing the IRC connection
// for packs on the same server. Returns one PackResult per pack.
func (c *Client) DownloadAll() []PackResult {
	c.logf("=== Starting XDCC download session ===")
	c.logf("Server: %s:%d", c.packs[0].Server.Address, c.packs[0].Server.Port)
	c.logf("Total packs to download: %d", len(c.packs))

	results := make([]PackResult, len(c.packs))

	if !c.conn.usingExistingConn {
		if err := c.connect(); err != nil {
			c.logf("ERROR: Failed to connect to IRC server: %v", err)
			for i := range results {
				results[i].Error = err
			}
			return results
		}
	} else {
		c.logf("Using existing persistent IRC connection")
	}

	if !c.conn.usingExistingConn {
		defer func() {
			c.logf("=== Closing IRC connection ===")
			c.conn.irc.Close()
			select {
			case <-c.conn.ircErrCh:
			case <-time.After(5 * time.Second):
			}
		}()
	}

	closeConn := func() {
		if !c.conn.usingExistingConn {
			c.conn.irc.Close()
			select {
			case <-c.conn.ircErrCh:
			case <-time.After(5 * time.Second):
			}
		}
	}

	for i := range c.packs {
		select {
		case <-c.ctx.Done():
			for j := i; j < len(results); j++ {
				results[j].Error = ErrCancelled
			}
			closeConn()
			return results
		default:
		}
		if i > 0 {
			c.debugf("Waiting 3s before next pack")
			select {
			case <-c.ctx.Done():
				for j := i; j < len(results); j++ {
					results[j].Error = ErrCancelled
				}
				closeConn()
				return results
			case <-time.After(packDelay):
			}
		}
		results[i] = c.downloadPackAtIndex(i, 0)
		// Fatal errors: propagate to all remaining packs
		if results[i].Error != nil {
			if errors.Is(results[i].Error, ErrServerUnreachable) ||
				errors.Is(results[i].Error, ErrUnrecoverable) ||
				errors.Is(results[i].Error, ErrCancelled) {
				for j := i + 1; j < len(results); j++ {
					results[j].Error = results[i].Error
				}
				break
			}
		}
	}

	if !c.conn.usingExistingConn {
		c.conn.irc.Close()
		// Drain ircErrCh so the goroutine can exit
		select {
		case <-c.conn.ircErrCh:
		case <-time.After(5 * time.Second):
		}
	}
	return results
}

// LastBotNotice returns the last NOTICE received from the bot for the
// current pack. Safe to call after DownloadAll returns.
func (c *Client) LastBotNotice() string {
	c.ps.mu.Lock()
	defer c.ps.mu.Unlock()
	return c.ps.lastBotNotice
}

// Cleanup removes all handlers registered by this Client from the girc.Client.
// Must be called after DownloadAll() when using a persistent connection
// (SetExistingClient), to prevent handler accumulation across multiple
// downloads on the same girc.Client. Each call to registerHandlers() adds
// new handlers without removing old ones; Cleanup removes them so the next
// download starts with a clean handler slate.
func (c *Client) Cleanup() {
	c.removeHandlers()
}

// ---------------------------------------------------------------------------
// Connection management
// ---------------------------------------------------------------------------

func (c *Client) connect() error {
	// When using an existing connection managed externally, skip connection entirely.
	if c.conn.usingExistingConn {
		return nil
	}

	server := c.packs[0].Server

	// Resolve the hostname to all valid IPs so we can try each one in order.
	resolvedIPs, err := c.resolveAllHosts(server.Address)
	if err != nil {
		return err
	}

	nick := c.opts.Username
	if nick == "" {
		nick = randomUsername()
	} else {
		nick += randomSuffix(3)
	}

	var lastErr error
	for i, ip := range resolvedIPs {
		if len(resolvedIPs) > 1 {
			c.infof("Connecting to %s:%d as '%s' (IP %d/%d: %s)",
				server.Address, server.Port, nick, i+1, len(resolvedIPs), ip)
		} else {
			c.infof("Connecting to %s:%d as '%s'", server.Address, server.Port, nick)
		}

		c.conn.connectedCh = make(chan struct{})
		c.conn.joinedChannels = make(map[string]bool)
		c.conn.ircErrCh = make(chan error, 1)

		c.conn.irc = girc.New(girc.Config{
			Server:      ip, // use resolved IP to avoid repeating a blocked DNS lookup
			Port:        server.Port,
			Nick:        nick,
			User:        nick,
			Name:        nick,
			PingDelay:   30 * time.Second,
			PingTimeout: 60 * time.Second,
		})
		c.registerHandlers()
		c.conn.handlersRegisteredOn = c.conn.irc
		go func() { c.conn.ircErrCh <- c.conn.irc.Connect() }()

		timeout := time.Duration(c.opts.ConnectTimeout)*time.Second + connectGracePeriod
		select {
		case <-c.conn.connectedCh:
			return nil
		case connErr := <-c.conn.ircErrCh:
			if connErr != nil {
				if isConnectError(connErr) {
					lastErr = connErr
					if i < len(resolvedIPs)-1 {
						c.noticef("IP %s failed (%v), trying next IP...", ip, connErr)
						continue
					}
					return fmt.Errorf("%w: all %d IPs for %s failed (last: %v)",
						ErrServerUnreachable, len(resolvedIPs), server.Address, lastErr)
				}
				return connErr
			}
			return fmt.Errorf("IRC connection closed before CONNECTED event")
		case <-c.ctx.Done():
			c.conn.irc.Close()
			return ErrCancelled
		case <-time.After(timeout):
			c.conn.irc.Close()
			lastErr = fmt.Errorf("connection to %s timed out", ip)
			if i < len(resolvedIPs)-1 {
				c.noticef("IP %s timed out, trying next IP...", ip)
				continue
			}
			return fmt.Errorf("%w: all %d IPs for %s timed out",
				ErrServerUnreachable, len(resolvedIPs), server.Address)
		}
	}

	// Should not be reached, but handle defensively.
	return fmt.Errorf("%w: %v", ErrServerUnreachable, lastErr)
}

func (c *Client) reconnect() error {
	// When using an existing persistent connection, get a fresh girc.Client
	// from the external manager. The manager reconnects automatically with
	// exponential backoff; we just wait for the new client to be ready.
	if c.conn.usingExistingConn {
		if c.opts.ReconnectCallback == nil {
			return fmt.Errorf("cannot reconnect on persistent connection: no callback provided")
		}
		c.infof("Waiting for persistent connection to be re-established...")
		for i := 0; i < 30; i++ {
			newClient := c.opts.ReconnectCallback()
			if newClient != nil {
				// Avoid duplicate handler registrations on the same girc.Client.
				// When the persistent connection hasn't actually dropped, the
				// callback returns the same client; re-registering would pile up
				// duplicate handlers each retry.
				if newClient == c.conn.handlersRegisteredOn {
					c.infof("Persistent connection still alive, reusing existing handlers")
					c.conn.irc = newClient
					c.conn.joinedChannels = make(map[string]bool)
					return nil
				}
				c.infof("Persistent connection re-established, re-binding handlers")
				c.conn.irc = newClient
				c.conn.joinedChannels = make(map[string]bool)
				c.registerHandlers()
				c.conn.handlersRegisteredOn = newClient
				return nil
			}
			select {
			case <-c.ctx.Done():
				return ErrCancelled
			case <-time.After(1 * time.Second):
			}
		}
		return fmt.Errorf("persistent connection not re-established after 30s")
	}

	c.infof("Reconnecting to IRC...")
	c.conn.irc.Close()
	// Drain ircErrCh (may have been consumed already; best-effort)
	select {
	case <-c.conn.ircErrCh:
	case <-time.After(3 * time.Second):
	}
	return c.connect()
}

// ---------------------------------------------------------------------------
// Per-pack download
// ---------------------------------------------------------------------------

func (c *Client) currentPack() *entities.XDCCPack {
	return c.packs[c.packIdxVal.Load()]
}

// stopWhoisFallbackTimer safely stops the reusable WHOIS fallback timer and
// drains its channel so the next reset does not immediately read a stale value.
// This is safe to call even if the timer has never been created or has already
// fired. It is called from resetForPack() between packs.
func (c *Client) stopWhoisFallbackTimer() {
	if c.conn.whoisFallbackTimer == nil {
		return
	}
	if !c.conn.whoisFallbackTimer.Stop() {
		select {
		case <-c.conn.whoisFallbackTimer.C:
		default:
		}
	}
}

func (c *Client) resetForPack() {
	// Close the previous pack's state before creating a new one.
	// This unblocks any goroutines still reading from the old channels
	// (e.g. ackSender, progressPrinter, stallWatcher).
	if c.ps != nil {
		c.ps.mu.Lock()
		if c.ps.dccConn != nil {
			c.ps.dccConn.Close()
			c.ps.dccConn = nil
		}
		if c.ps.dccFile != nil {
			c.ps.dccFile.Close()
			c.ps.dccFile = nil
		}
		c.ps.mu.Unlock()

		if c.ps.downloadDone != nil && c.ps.downloadDoneOnce != nil {
			c.ps.downloadDoneOnce.Do(func() {
				close(c.ps.downloadDone)
			})
		}
	}

	// Stop the WHOIS fallback timer from the previous pack and drain its
	// channel so it can be safely reused in the next pack.
	c.stopWhoisFallbackTimer()

	// Allocate a fresh packState per pack so that sync.Once instances are
	// independent and no stale values leak between packs.
	c.ps = &packState{
		downloadDone:        make(chan struct{}),
		downloadDoneOnce:    &sync.Once{},
		downloadStarted:     make(chan struct{}),
		downloadStartedOnce: &sync.Once{},
		ackQueue:            make(chan []byte, ackQueueBufSize),
	}
}

func (c *Client) downloadPackAtIndex(idx, retryCount int) PackResult {
	if retryCount > 3 {
		return PackResult{Error: fmt.Errorf("giving up on pack %d after 3 retries",
			c.packs[idx].PackNumber)}
	}

	if idx < 0 {
		idx = 0
	}
	c.packIdxVal.Store(int32(idx)) //nolint:gosec // idx is always a small pack index (0..len(packs))

	c.resetForPack()
	pack := c.currentPack()

	c.infof("--- Starting pack download: %s (pack #%d) from bot %s ---", pack.GetFilename(), pack.PackNumber, pack.Bot)

	// Channel-join delay only on first connection (not between packs).
	// 0 = no delay, -1 = random 5-10s, >0 = that many seconds.
	if idx == 0 && c.opts.ChannelJoinDelay != 0 {
		c.infof("Waiting %ds before WHOIS (channel join delay)", c.opts.ChannelJoinDelay)
		select {
		case <-c.ctx.Done():
			return PackResult{Error: ErrCancelled}
		case <-time.After(time.Duration(c.opts.ChannelJoinDelay) * time.Second):
		}
	}

	c.infof("→ Sending WHOIS query for bot: %s", pack.Bot)
	c.conn.irc.Cmd.Whois(pack.Bot)

	err := c.waitForCurrentPack()
	if err == nil {
		// Return discovered filename and size (may be empty/0 if not yet known).
		// Read under lock to avoid data race with the NOTICE handler.
		c.ps.mu.Lock()
		filename := c.ps.packFilename
		filesize := c.ps.packSize
		c.ps.mu.Unlock()
		return PackResult{
			FilePath: pack.GetFilepath(),
			Filename: filename,
			FileSize: filesize,
		}
	}

	switch {
	case errors.Is(err, ErrPackAlreadyReq):
		c.noticef("Bot %s says pack already requested, waiting 60s before retry (attempt %d/3)", pack.Bot, retryCount+1)
		fmt.Println("Pack already requested. Waiting 60 seconds before retrying...")
		select {
		case <-c.ctx.Done():
			return PackResult{Error: ErrCancelled}
		case <-time.After(60 * time.Second):
		}
		return c.downloadPackAtIndex(idx, retryCount+1)

	case errors.Is(err, ErrTimeout), errors.Is(err, ErrDownloadFailed):
		c.noticef("Download for bot %s failed (%v), retrying (attempt %d/3)", pack.Bot, err, retryCount+1)
		fmt.Printf("Retrying pack #%d (attempt %d/3)...\n", pack.PackNumber, retryCount+1)
		if err2 := c.reconnect(); err2 != nil {
			c.noticef("Reconnect failed for bot %s: %v", pack.Bot, err2)
			return PackResult{Error: err2}
		}
		return c.downloadPackAtIndex(idx, retryCount+1)
	}

	c.noticef("Giving up on pack #%d (bot %s) after error: %v", pack.PackNumber, pack.Bot, err)
	c.ps.mu.Lock()
	notice := c.ps.lastBotNotice
	c.ps.mu.Unlock()
	return PackResult{Error: err, LastBotNotice: notice}
}

func (c *Client) waitForCurrentPack() error {
	// Phase 1: wait for DCC transfer to start.
	// Covers: WHOIS response + channel join + bot response + WaitTime.
	pack := c.currentPack()
	connectTimeout := time.Duration(c.opts.ConnectTimeout+c.opts.WaitTime)*time.Second + connectGracePeriod
	c.infof("Waiting up to %v for DCC transfer to start (bot=%s, pack=%d)", connectTimeout, pack.Bot, pack.PackNumber)

	select {
	case <-c.ps.downloadStarted:
		c.infof("DCC transfer started for bot %s", pack.Bot)
	case <-c.ps.downloadDone:
		c.ps.mu.Lock()
		downloadErr := c.ps.downloadError
		c.ps.mu.Unlock()
		c.infof("Download finished with error for bot %s: %v", pack.Bot, downloadErr)
		return downloadErr
	case <-c.ctx.Done():
		c.infof("Download cancelled for bot %s", pack.Bot)
		c.finishWithError(ErrCancelled)
		return ErrCancelled
	case err := <-c.conn.ircErrCh:
		// IRC connection died before transfer started; treat as timeout so
		// downloadPackAtIndex will reconnect and retry.
		c.infof("IRC connection lost while waiting for DCC from bot %s: %v", pack.Bot, err)
		c.ps.mu.Lock()
		downloadErr := c.ps.downloadError
		c.ps.mu.Unlock()
		if err != nil && downloadErr == nil {
			if isConnectError(err) {
				return fmt.Errorf("%w: %v", ErrServerUnreachable, err)
			}
			return ErrTimeout
		}
		return downloadErr
	case <-time.After(connectTimeout):
		c.infof("TIMEOUT after %v waiting for DCC transfer from bot %s (pack=%d) — no DCC SEND received, WHOIS/join/XDDC may have failed silently",
			connectTimeout, pack.Bot, pack.PackNumber)
		c.finishWithError(ErrTimeout)
		return ErrTimeout
	}

	// Phase 2: DCC transfer is a direct TCP connection — it can survive
	// IRC disconnect. Just wait for completion.
	if c.opts.StallTimeout > 0 {
		go c.stallWatcher()
	}
	select {
	case <-c.ps.downloadDone:
	case <-c.ctx.Done():
		c.ps.mu.Lock()
		if c.ps.dccConn != nil {
			c.ps.dccConn.Close()
		}
		c.ps.mu.Unlock()
		c.finishWithError(ErrCancelled)
	}
	c.ps.mu.Lock()
	downloadErr := c.ps.downloadError
	c.ps.mu.Unlock()
	return downloadErr
}

// ---------------------------------------------------------------------------
// Finish helpers
// ---------------------------------------------------------------------------

// finishSuccess records a successful download. Does NOT close the IRC
// connection so subsequent packs can reuse it.
func (c *Client) finishSuccess() {
	elapsed := time.Since(c.ps.downStartTime)
	speedStr := formatSpeed(float64(c.ps.filesize) / elapsed.Seconds())
	fmt.Printf("\nFile %s downloaded successfully in %s at %s\n",
		c.currentPack().GetFilename(),
		formatDuration(elapsed),
		speedStr)
	c.ps.downloadDoneOnce.Do(func() {
		close(c.ps.downloadDone)
	})
}

// finishWithNotice stores a bot notice and then calls finishWithError.
func (c *Client) finishWithNotice(err error, notice string) {
	c.ps.mu.Lock()
	c.ps.lastBotNotice = notice
	c.ps.mu.Unlock()
	c.finishWithError(err)
}

// finishWithError records a download error. Does NOT close the IRC
// connection so the session can retry or continue with the next pack.
// The first error wins: subsequent calls are ignored (sync.Once guards the channel close).
func (c *Client) finishWithError(err error) {
	c.ps.mu.Lock()
	if c.ps.downloadError == nil {
		c.ps.downloadError = err
	}
	c.ps.mu.Unlock()
	c.ps.downloadDoneOnce.Do(func() {
		close(c.ps.downloadDone)
	})
}

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

// infof prints at verbosity >= 0 (default and above). Use for connection status
// and download progress that is suppressed only in quiet mode (-q / -qq).
func (c *Client) infof(format string, args ...interface{}) {
	if c.verbosity >= 0 {
		c.logger.Printf(format, args...)
	}
}

// noticef prints at verbosity >= -1 (quiet and above).
// Use for errors, bot messages, and status that matter even in quiet mode.
func (c *Client) noticef(format string, args ...interface{}) {
	if c.verbosity >= -1 {
		c.logger.Printf(format, args...)
	}
}

// logf prints at verbosity >= 1 (-v). Use for channel joins, WHOIS results,
// DCC negotiation messages.
func (c *Client) logf(format string, args ...interface{}) {
	if c.verbosity >= 1 {
		c.logger.Printf(format, args...)
	}
}

// debugf prints at verbosity >= 2 (-vv). Use for low-level details: DNS,
// DCC internals, raw IRC event flow.
func (c *Client) debugf(format string, args ...interface{}) {
	if c.verbosity >= 2 {
		c.logger.Printf(format, args...)
	}
}
