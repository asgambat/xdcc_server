package irc

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lrstanley/girc"
	"xdcc-go/internal/entities"
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
		c.infof("WHOIS response completed for %s", c.currentPack().Bot)
		if c.ps.messageSent.Load() {
			return
		}
		if c.ps.needsJoin.Load() {
			// We sent a JOIN; wait for the JOIN event to trigger XDCC.
			// But add a safety timeout: if the server doesn't re-emit JOIN
			// (e.g. because we were already in the channel via an auto-join),
			// send XDCC anyway after a few seconds.
			//
			// We use a reusable timer instead of time.After to avoid allocating
			// a new timer (and its internal runtime goroutine) on every WHOIS.
			// In burst scenarios this prevents transient goroutine accumulation.
			c.infof("Waiting for JOIN confirmation before sending XDCC request (fallback in 5s)")

			// Reset the reusable timer to 5s. Stop and drain first in case
			// a stale value remains from a previous pack or a second WHOIS
			// fires for the same pack (rare but safe to handle).
			if c.conn.whoisFallbackTimer == nil {
				c.conn.whoisFallbackTimer = time.NewTimer(5 * time.Second)
			} else {
				c.stopWhoisFallbackTimer()
				c.conn.whoisFallbackTimer.Reset(5 * time.Second)
			}

			go func() {
				select {
				case <-c.ps.downloadDone:
					return
				case <-c.ctx.Done():
					return
				case <-c.conn.whoisFallbackTimer.C:
					if !c.ps.messageSent.Load() {
						c.infof("JOIN confirmation not received, sending XDCC request anyway")
						c.sendXDCCRequest(client)
					}
				}
			}()
			return
		}
		if c.ps.whoisFoundChannels.Load() {
			// All channels were already joined — send XDCC directly.
			c.infof("All channels already joined, sending XDCC request")
			c.sendXDCCRequest(client)
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
				c.sendXDCCRequest(client)
			} else {
				c.infof("No channels from WHOIS, joining fallback channel: %s", ch)
				c.ps.needsJoin.Store(true)
				// Record fallback channel on the pack for speed tracking.
				if c.currentPack().Channel == "" {
					c.currentPack().Channel = ch
				}
				client.Cmd.Join(ch)
			}
		} else {
			c.infof("No channels from WHOIS and no fallback, sending XDCC request directly")
			c.sendXDCCRequest(client)
		}
	})
	c.conn.handlerCUIDs = append(c.conn.handlerCUIDs, cuid)

	// WHOIS channels: join only channels we have not yet joined.
	// If the bot is in exactly one channel, that will be automatically joined.
	cuid = c.conn.irc.Handlers.Add(girc.RPL_WHOISCHANNELS, func(client *girc.Client, e girc.Event) {
		if len(e.Params) < 2 {
			return
		}
		rawChannels := e.Params[len(e.Params)-1]
		c.infof("WHOIS response: bot is in channels: %s", rawChannels)

		for _, part := range strings.Fields(rawChannels) {
			part = strings.TrimLeft(part, "@+%&~")
			if !strings.HasPrefix(part, "#") {
				continue
			}
			ch := strings.ToLower(part)
			c.ps.whoisFoundChannels.Store(true)
			alreadyIn := c.conn.joinedChannels[ch]
			if alreadyIn {
				c.infof("Already in channel %s, skipping JOIN", part)
				// Still record the channel on the pack for speed tracking.
				if c.currentPack().Channel == "" {
					c.currentPack().Channel = ch
				}
			} else if c.opts.IsChannelBlacklisted != nil && c.opts.IsChannelBlacklisted(ch) {
				c.infof("Skipping blacklisted channel %s", part)
			} else {
				c.infof("Joining channel: %s", part)
				c.ps.needsJoin.Store(true)
				// Record the discovered channel on the pack so it can be
				// used for per-channel statistics after download completes.
				if c.currentPack().Channel == "" {
					c.currentPack().Channel = ch
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
		ch := strings.ToLower(e.Params[0])
		c.conn.joinedChannels[ch] = true
		c.infof("✓ Joined channel: %s", e.Params[0])
		if !c.ps.messageSent.Load() {
			if c.ps.needsJoin.Load() {
				// New channel: reset the fallback timer to 5s from now
				// so XDCC is sent 5s after JOIN confirmation.
				c.infof("New channel joined, waiting 5s before XDCC request")
				if c.conn.whoisFallbackTimer != nil {
					c.stopWhoisFallbackTimer()
					c.conn.whoisFallbackTimer.Reset(5 * time.Second)
				}
			} else {
				// Channel already joined: send XDCC immediately
				c.sendXDCCRequest(client)
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
		c.handleDCC(ctcp.Text, sourceHost)
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
		notice := e.Last()
		msg := strings.ToLower(notice)
		// These are standard IRC server ident/hostname check messages — suppress in quiet mode.
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

		// Try to extract pack filename and size from the notice.
		// This is important for manual downloads where filename/size aren't known upfront.
		if filename, size := parsePackInfoFromNotice(notice); filename != "" || size > 0 {
			c.ps.mu.Lock()
			if filename != "" && c.ps.packFilename == "" {
				c.ps.packFilename = filename
				c.currentPack().SetFilename(filename, true)
				c.infof("Bot notice: discovered filename=%s", filename)
			}
			if size > 0 && c.ps.packSize == 0 {
				c.ps.packSize = size
				c.currentPack().SetSize(size)
				c.infof("Bot notice: discovered size=%s", entities.HumanReadableBytes(size))
			}
			c.ps.mu.Unlock()
		}

		alreadyReqMsgs := []string{"you already requested", "richiesto questo pack!"}
		blockedMsgs := []string{"xdcc send negato", "numero pack errato", "invalid pack number",
			"gli slots sono occupati", "denied"}

		for _, s := range alreadyReqMsgs {
			if strings.Contains(msg, s) {
				c.finishWithNotice(ErrPackAlreadyReq, notice)
				return
			}
		}
		for _, s := range blockedMsgs {
			if strings.Contains(msg, s) {
				c.finishWithNotice(ErrBotDenied, notice)
				return
			}
		}
	})
	c.conn.handlerCUIDs = append(c.conn.handlerCUIDs, cuid)

	cuid = c.conn.irc.Handlers.Add(girc.ERR_NOSUCHNICK, func(client *girc.Client, e girc.Event) {
		c.noticef("Bot '%s' not found on server", c.currentPack().Bot)
		c.finishWithError(ErrBotNotFound)
	})
	c.conn.handlerCUIDs = append(c.conn.handlerCUIDs, cuid)

	cuid = c.conn.irc.Handlers.Add(girc.ERROR, func(client *girc.Client, e girc.Event) {
		c.noticef("IRC error: %s", e.Last())
		c.finishWithError(ErrUnrecoverable)
	})
	c.conn.handlerCUIDs = append(c.conn.handlerCUIDs, cuid)
}

// removeHandlers removes all handlers previously registered by this client
// from the girc.Client. This prevents accumulation of duplicate handlers when
// multiple downloads share the same persistent IRC connection.
func (c *Client) removeHandlers() {
	for _, cuid := range c.conn.handlerCUIDs {
		c.conn.irc.Handlers.Remove(cuid)
	}
	c.conn.handlerCUIDs = nil
}

func (c *Client) sendXDCCRequest(client *girc.Client) {
	if c.ps.messageSent.Swap(true) {
		return
	}
	if c.opts.WaitTime > 0 {
		c.logf("Waiting %ds before sending XDCC request", c.opts.WaitTime)
		time.Sleep(time.Duration(c.opts.WaitTime) * time.Second)
	}
	pack := c.currentPack()
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
