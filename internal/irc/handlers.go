package irc

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"xdcc_server/internal/entities"

	"github.com/lrstanley/girc"
)

func (c *Client) registerHandlers() {
	// Clear previous CUIDs in case registerHandlers is called multiple times
	// on the same client (e.g. after reconnect with a new girc.Client).
	c.conn.handlerCUIDs = nil

	cuid := c.conn.irc.Handlers.Add(girc.CONNECTED, func(client *girc.Client, e girc.Event) {
		c.conn.connectTime = time.Now()
		c.infof("✓ Connected to IRC server successfully")
		// Safe close: handle already-closed channel (e.g. when using existing connection)
		select {
		case <-c.conn.connectedCh:
		default:
			close(c.conn.connectedCh)
		}
	})
	c.conn.handlerCUIDs = append(c.conn.handlerCUIDs, cuid)

	// End of WHOIS: decide whether to send XDCC now or wait for JOIN.
	cuid = c.conn.irc.Handlers.Add(girc.RPL_ENDOFWHOIS, func(client *girc.Client, e girc.Event) {
		// Capture the current pack once at handler entry (fix 2.19): packIdxVal may
		// advance to the next pack before this handler finishes, so all references
		// within the handler must use this snapshot to avoid mis-attributing events.
		pack := c.currentPack()
		ps := c.ps.Load()
		flags := ps.snapshotDecisionFlags()
		c.infof("WHOIS response completed for %s", pack.Bot)
		if flags.messageSent {
			return
		}
		if flags.needsJoin {
			// We sent a JOIN; wait for the JOIN event to trigger XDCC.
			// But add a safety timeout: if the server doesn't re-emit JOIN
			// (e.g. because we were already in the channel via an auto-join),
			// send XDCC anyway after a few seconds.
			//
			// We use a reusable timer instead of time.After to avoid allocating
			// a new timer (and its internal runtime goroutine) on every WHOIS.
			// In burst scenarios this prevents transient goroutine accumulation.
			c.infof("Waiting for JOIN confirmation before sending XDCC request (fallback in 5s)")

			// Reset the reusable timer to 5s under timerMu (fix 2.20): the
			// timer is shared between this handler goroutine and the download
			// goroutine (resetForPack), so all accesses must be serialized.
			// Capture the channel inside the lock so the goroutine below uses
			// a stable reference even if the timer is later reset/replaced.
			c.conn.timerMu.Lock()
			if c.conn.whoisFallbackTimer == nil {
				c.conn.whoisFallbackTimer = time.NewTimer(5 * time.Second)
			} else {
				c.stopWhoisFallbackTimerUnlocked()
				c.conn.whoisFallbackTimer.Reset(5 * time.Second)
			}
			timerC := c.conn.whoisFallbackTimer.C
			c.conn.timerMu.Unlock()

			// Capture downloadDone and ps locally: resetForPack replaces c.ps,
			// so reading c.ps.downloadDone inside the goroutine would race with
			// the assignment and could reference the wrong pack's channel.
			downloadDone := ps.downloadDone
			go func() {
				select {
				case <-downloadDone:
					return
				case <-c.ctx.Done():
					return
				case <-timerC:
					if !ps.isMessageSent() {
						c.infof("JOIN confirmation not received, sending XDCC request anyway")
						c.sendXDCCRequest(client, ps, pack)
					}
				}
			}()
			return
		}
		if flags.whoisFoundChannels {
			// All channels were already joined — send XDCC directly.
			c.infof("All channels already joined, sending XDCC request")
			c.sendXDCCRequest(client, ps, pack)
			return
		}
		// No channels found in WHOIS at all.
		if c.opts.FallbackChannel != "" {
			ch := c.opts.FallbackChannel
			if !strings.HasPrefix(ch, "#") {
				ch = "#" + ch
			}
			ch = strings.ToLower(ch)
			if c.opts.IsChannelBlacklisted != nil && c.opts.IsChannelBlacklisted(ch) {
				c.infof("Fallback channel %s is blacklisted, sending XDCC request directly", ch)
				c.sendXDCCRequest(client, ps, pack)
			} else {
				c.infof("No channels from WHOIS, joining fallback channel: %s", ch)
				ps.setNeedsJoin(true)
				// Record fallback channel on the pack for speed tracking.
				if pack.GetChannel() == "" {
					pack.SetChannel(ch)
				}
				client.Cmd.Join(ch)
			}
		} else {
			c.infof("No channels from WHOIS and no fallback, sending XDCC request directly")
			c.sendXDCCRequest(client, ps, pack)
		}
	})
	c.conn.handlerCUIDs = append(c.conn.handlerCUIDs, cuid)

	// WHOIS channels: join only channels we have not yet joined.
	// If the bot is in exactly one channel, that will be automatically joined.
	cuid = c.conn.irc.Handlers.Add(girc.RPL_WHOISCHANNELS, func(client *girc.Client, e girc.Event) {
		if len(e.Params) < 2 {
			return
		}
		// Capture current pack once at handler entry (fix 2.19).
		pack := c.currentPack()
		ps := c.ps.Load()
		rawChannels := e.Params[len(e.Params)-1]
		c.infof("WHOIS response: bot is in channels: %s", rawChannels)

		for _, part := range strings.Fields(rawChannels) {
			part = strings.TrimLeft(part, "@+%&~")
			if !strings.HasPrefix(part, "#") {
				continue
			}
			ch := strings.ToLower(part)
			// Filter blacklisted channels immediately: they must never be
			// joined, counted as "found" via WHOIS, or recorded on the
			// pack for speed tracking — not even if already joined from
			// before the blacklist entry was added.
			if c.opts.IsChannelBlacklisted != nil && c.opts.IsChannelBlacklisted(ch) {
				c.infof("Skipping blacklisted channel %s", part)
				continue
			}
			ps.setWhoisFoundChannels(true)
			c.conn.joinedChannelsMu.RLock()
			alreadyIn := c.conn.joinedChannels[ch]
			c.conn.joinedChannelsMu.RUnlock()
			if alreadyIn {
				c.infof("Already in channel %s, skipping JOIN", part)
				// Still record the channel on the pack for speed tracking.
				if pack.GetChannel() == "" {
					pack.SetChannel(ch)
				}
			} else {
				c.infof("Joining channel: %s", part)
				ps.setNeedsJoin(true)
				// Record the discovered channel on the pack so it can be
				// used for per-channel statistics after download completes.
				if pack.GetChannel() == "" {
					pack.SetChannel(ch)
				}
				time.Sleep(time.Duration(1+randN(2)) * time.Second)
				client.Cmd.Join(part)
			}
		}
	})
	c.conn.handlerCUIDs = append(c.conn.handlerCUIDs, cuid)

	// JOIN: record membership, send XDCC if pending.
	// If this is a newly-joined channel (discovered via WHOIS), wait 5s before
	// sending XDCC so the bot has time to register our presence.
	// If the channel was already joined, send XDCC immediately.
	cuid = c.conn.irc.Handlers.Add(girc.JOIN, func(client *girc.Client, e girc.Event) {
		if e.Source == nil || !strings.EqualFold(e.Source.Name, client.GetNick()) {
			return
		}
		pack := c.currentPack()
		ps := c.ps.Load()
		flags := ps.snapshotDecisionFlags()
		ch := strings.ToLower(e.Params[0])
		c.conn.joinedChannelsMu.Lock()
		c.conn.joinedChannels[ch] = true
		c.conn.joinedChannelsMu.Unlock()
		c.infof("✓ Joined channel: %s", e.Params[0])
		if !flags.messageSent {
			if flags.needsJoin {
				// New channel: reset the fallback timer to 5s from now
				// so XDCC is sent 5s after JOIN confirmation.
				// Acquire timerMu (fix 2.20) to synchronize with resetForPack
				// and the RPL_ENDOFWHOIS handler which also touch the timer.
				c.infof("New channel joined, waiting 5s before XDCC request")
				c.conn.timerMu.Lock()
				if c.conn.whoisFallbackTimer != nil {
					c.stopWhoisFallbackTimerUnlocked()
					c.conn.whoisFallbackTimer.Reset(5 * time.Second)
				}
				c.conn.timerMu.Unlock()
			} else {
				// Channel already joined: send XDCC immediately
				c.sendXDCCRequest(client, ps, pack)
			}
		}
	})
	c.conn.handlerCUIDs = append(c.conn.handlerCUIDs, cuid)

	// CTCP DCC handler (DCC SEND / DCC ACCEPT for resume).
	// Note: CTCP.Set() does not return a CUID and simply overwrites any
	// previous handler for the same command, so it does not accumulate.
	c.conn.irc.CTCP.Set("DCC", func(client *girc.Client, ctcp girc.CTCPEvent) {
		sourceHost := ""
		if ctcp.Source != nil {
			sourceHost = ctcp.Source.Host
		}
		// Pass client to handleDCC instead of reading c.conn.irc,
		// which may be concurrently reassigned by reconnect().
		c.handleDCC(client, ctcp.Text, sourceHost)
	})

	// NOTICE from bot.
	// The handler distinguishes three classes of messages:
	//   1. Server ident/hostname checks — downgraded to logf (verbose only) because
	//      they are emitted by the server itself, not the bot, and clutter normal output.
	//   2. "Already requested" messages — trigger a 60 s wait + retry.
	//   3. "Denied / slot busy" messages — abort with ErrBotDenied.
	// Message patterns include both English and Italian strings because several
	// Rizon bots (particularly Italian ones) reply in Italian.
	cuid = c.conn.irc.Handlers.Add(girc.NOTICE, func(client *girc.Client, e girc.Event) {
		// Capture current pack once at handler entry (fix 2.19).
		pack := c.currentPack()
		ps := c.ps.Load() // capture ps to avoid race with resetForPack()
		notice := e.Last()
		msg := strings.ToLower(notice)

		// Try to extract pack filename and size from the notice.
		// This is important for manual downloads where filename/size aren't known upfront.
		if filename, size := parsePackInfoFromNotice(notice); filename != "" || size > 0 {
			ps.mu.Lock()
			if filename != "" && ps.packFilename == "" {
				ps.packFilename = filename
				pack.SetFilename(filename, true)
				c.infof("Bot notice: discovered filename=%s", filename)
			}
			if size > 0 && ps.packSize == 0 {
				ps.packSize = size
				pack.SetSize(size)
				c.infof("Bot notice: discovered size=%s", entities.HumanReadableBytes(size))
			}
			ps.mu.Unlock()
		}

		// Check for failure-class notices first. These get categorized logs
		// that include the notice text — no need for a separate raw log.
		alreadyReqMsgs := []string{"you already requested", "richiesto questo pack!"}
		blockedMsgs := []string{"xdcc send negato", "numero pack errato", "invalid pack number",
			"gli slots sono occupati", "denied"}

		for _, s := range alreadyReqMsgs {
			if strings.Contains(msg, s) {
				c.noticef("Bot %s says pack already requested (pack=%d): %s", pack.Bot, pack.PackNumber, notice)
				c.finishWithNotice(ps, ErrPackAlreadyReq, notice)
				return
			}
		}
		for _, s := range blockedMsgs {
			if strings.Contains(msg, s) {
				c.noticef("Bot %s denied XDCC request (pack=%d): %s", pack.Bot, pack.PackNumber, notice)
				c.finishWithNotice(ps, ErrBotDenied, notice)
				return
			}
		}

		// Raw notice log for non-failure messages (informational notices,
		// transfer progress, etc.). Quiet-filtered server checks use logf
		// (verbose only), everything else uses noticef.
		quietFiltered := []string{
			"looking up your hostname",
			"checking ident",
			"couldn't resolve your hostname",
			"no ident response",
		}
		isQuietFiltered := false
		for _, f := range quietFiltered {
			if strings.Contains(msg, f) {
				isQuietFiltered = true
				break
			}
		}
		if isQuietFiltered {
			c.logf("Bot notice: %s", notice)
		} else {
			c.noticef("Bot notice: %s", notice)
		}
	})
	c.conn.handlerCUIDs = append(c.conn.handlerCUIDs, cuid)

	// IRC format: :server 401 ourNick targetNick :No such nick/channel
	// girc includes the recipient (ourNick) as Params[0]; the target is Params[1].
	cuid = c.conn.irc.Handlers.Add(girc.ERR_NOSUCHNICK, func(client *girc.Client, e girc.Event) {
		if len(e.Params) < 2 {
			return
		}
		// Capture current pack once at handler entry (fix 2.19).
		pack := c.currentPack()
		if !strings.EqualFold(e.Params[1], pack.Bot) {
			return // 401 for a different nick, not our bot
		}
		ps := c.ps.Load() // capture ps to avoid race with resetForPack()
		c.noticef("Bot '%s' not found on server", pack.Bot)
		c.finishWithErrorPS(ps, ErrBotNotFound)
	})
	c.conn.handlerCUIDs = append(c.conn.handlerCUIDs, cuid)

	cuid = c.conn.irc.Handlers.Add(girc.ERROR, func(client *girc.Client, e girc.Event) {
		ps := c.ps.Load() // capture ps to avoid race with resetForPack()
		c.noticef("IRC error: %s", e.Last())
		c.finishWithErrorPS(ps, ErrUnrecoverable)
	})
	c.conn.handlerCUIDs = append(c.conn.handlerCUIDs, cuid)
}

// removeHandlers removes all handlers previously registered by this client
// from the girc.Client. Uses handlersRegisteredOn (the client where handlers
// were actually registered) rather than c.conn.irc, which may have already
// been reassigned to a new client by SetExistingClient or reconnect().
func (c *Client) removeHandlers() {
	if c.conn.handlersRegisteredOn == nil {
		c.conn.handlerCUIDs = nil
		return
	}
	for _, cuid := range c.conn.handlerCUIDs {
		c.conn.handlersRegisteredOn.Handlers.Remove(cuid)
	}
	c.conn.handlerCUIDs = nil
}

// sendXDCCRequest sends the XDCC request message to the bot. Both ps and pack
// must be snapshots captured at handler entry so the request state and target
// stay consistent even if c.ps/c.packIdxVal advance concurrently.
func (c *Client) sendXDCCRequest(client *girc.Client, ps *packState, pack *entities.XDCCPack) {
	if ps.markMessageSent() {
		return
	}
	if c.opts.WaitTime > 0 {
		c.logf("Waiting %ds before sending XDCC request", c.opts.WaitTime)
		time.Sleep(time.Duration(c.opts.WaitTime) * time.Second)
	}
	msg := pack.GetRequestMessage(false)
	c.infof("→ Sending XDCC request to bot %s: %s", pack.Bot, msg)
	client.Cmd.Message(pack.Bot, msg)
}

// Pre-compiled regexes for pack info extraction — compiled once at init time
// to avoid recompilation on every bot NOTICE.
var (
	packFileRe = regexp.MustCompile(`pack #\d+\s*\(\s*"([^"]+)"\s*\)`)
	sizeRes    = []*regexp.Regexp{
		regexp.MustCompile(`(?i)grandezza\s*([\d.]+)\s*(gb|mb|kb)`),
		regexp.MustCompile(`(?i)size\s*:?\s*([\d.]+)\s*(gb|mb|kb)`),
		regexp.MustCompile(`(?i)\(([\d.]+)\s*(gb|mb|kb)\)`),
	}
)

// parsePackInfoFromNotice extracts the pack filename and size from a bot notice.
// Returns the filename and size in bytes, or empty string and 0 if not found.
// Examples:
//
//	"Ti sto inviando il pack #128 (\"Don.Matteo.S15E01.ITA.DLMux.x264-WRM.mkv\"), che ha grandezza 1.6GB."
//	"Sending pack #42 (file.mkv), size: 500MB"
//	"Pack #99 - "another.file.avi" - 1.2GB"
func parsePackInfoFromNotice(notice string) (filename string, sizeBytes int64) {
	// Try to extract filename from quoted filename after "pack #N"
	// Pattern: pack #<number> ("filename")
	if m := packFileRe.FindStringSubmatch(notice); len(m) >= 2 {
		filename = m[1]
	}

	// Try to extract size from Italian "grandezza X.YGB/MB/KB" or English "size: X.YGB/MB/KB"
	// Handle both formats, case-insensitive.
	for _, re := range sizeRes {
		if m := re.FindStringSubmatch(notice); len(m) >= 3 {
			var size float64
			if _, err := fmt.Sscanf(m[1], "%f", &size); err == nil {
				multiplier := int64(1)
				switch strings.ToLower(m[2]) {
				case "gb":
					multiplier = 1024 * 1024 * 1024
				case "mb":
					multiplier = 1024 * 1024
				case "kb":
					multiplier = 1024
				}
				sizeBytes = int64(size * float64(multiplier))
				break
			}
		}
	}

	return filename, sizeBytes
}
