package irc

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xdcc_server/internal/entities"
)

// bufPool reduces GC pressure for the 4 KB read buffer allocated on each
// receiveData() call. Multiple concurrent downloads share the pool.
// A pointer wrapper is used so Put() avoids allocation (SA6002).
type dccBuffer struct {
	data []byte
}

var bufPool = sync.Pool{
	New: func() any { return &dccBuffer{data: make([]byte, 4096)} },
}

func (c *Client) handleDCC(text, sourceHost string) {
	parts := splitDCC(text)
	if len(parts) == 0 {
		return
	}
	switch strings.ToUpper(parts[0]) {
	case "SEND":
		c.handleDCCSend(parts, sourceHost)
	case "ACCEPT":
		c.handleDCCAccept(parts)
	default:
		c.logf("Unknown DCC command: %s", parts[0])
	}
}

func (c *Client) handleDCCSend(parts []string, sourceHost string) {
	// Capture pack once at handler entry (fix 2.19) so all references within
	// this function use the same snapshot even if packIdxVal advances.
	pack := c.currentPack()
	ps := c.ps // capture ps to avoid race with resetForPack()

	if len(parts) < 5 {
		c.noticef("Malformed DCC SEND (bot=%s pack=%d): %v", pack.Bot, pack.PackNumber, parts)
		return
	}
	filename := parts[1]
	ipNum := parts[2]
	port := parts[3]
	sizeStr := parts[4]

	// Passive DCC: the bot reports IP 0.0.0.0 (NAT/firewall scenario).
	// Fall back to the source hostname from the IRC CTCP event, or to the
	// server address as a last resort. This is non-standard but widely used
	// by bots behind NAT.
	peerIP := ipNumToQuad(ipNum)
	if peerIP == "0.0.0.0" {
		if sourceHost != "" {
			c.logf("Passive DCC: using source host %s instead of 0.0.0.0", sourceHost)
			peerIP = sourceHost
		} else {
			peerIP = pack.Server.Address
			c.logf("Passive DCC with unknown source host, falling back to %s", peerIP)
		}
	}
	peerAddr := peerIP + ":" + port
	filesize := parseI64(sizeStr)

	pack.SetFilename(filename, false)

	ps.mu.Lock()
	ps.filesize = filesize
	ps.peerAddr = peerAddr
	ps.mu.Unlock()

	c.debugf("DCC SEND: file=%s addr=%s size=%s", filename, peerAddr, entities.HumanReadableBytes(filesize))

	existingPath := pack.GetFilepath()
	c.debugf("Checking for existing file at: %s", existingPath)
	if fi, err := os.Stat(existingPath); err == nil {
		pos := fi.Size()
		c.logf("Existing file: %s, remote: %s",
			entities.HumanReadableBytes(pos), entities.HumanReadableBytes(filesize))
		if pos >= filesize {
			c.noticef("File already fully downloaded (local: %s >= remote: %s), skipping",
				entities.HumanReadableBytes(pos), entities.HumanReadableBytes(filesize))
			c.finishWithErrorPS(ps, ErrAlreadyDownloaded)
			return
		}
		atomic.StoreInt64(&ps.progress, pos)
		resumeParam := fmt.Sprintf("%q %s %d", filename, port, pos)
		c.debugf("Resuming download from %s / %s",
			entities.HumanReadableBytes(pos), entities.HumanReadableBytes(filesize))
		c.logf("Sending DCC RESUME: %s", resumeParam)
		c.conn.irc.Cmd.SendCTCP(pack.Bot, "DCC", "RESUME "+resumeParam)
		return
	}

	c.startDownload(ps, peerAddr, false)
}

func (c *Client) handleDCCAccept(parts []string) {
	if len(parts) < 4 {
		return
	}
	ps := c.ps // capture ps to avoid race with resetForPack()
	c.debugf("DCC ACCEPT: resuming download")
	c.startDownloadAppend(ps)
}

func (c *Client) startDownload(ps *packState, addr string, appendMode bool) {
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendMode {
		flag = os.O_APPEND | os.O_WRONLY
	}

	pack := c.currentPack()
	path := pack.GetFilepath()
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		c.noticef("Cannot open download file %s (bot=%s pack=%d): %v", path, pack.Bot, pack.PackNumber, err)
		c.finishWithErrorPS(ps, fmt.Errorf("cannot open file: %w", err))
		return
	}

	conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		f.Close()
		c.noticef("DCC connection failed to %s (bot=%s pack=%d): %v", addr, pack.Bot, pack.PackNumber, err)
		c.finishWithErrorPS(ps, fmt.Errorf("DCC connection failed: %w", err))
		return
	}

	ps.mu.Lock()
	ps.dccFile = f
	ps.dccConn = conn
	ps.downStartTime = time.Now()
	ps.dccTimestamp = time.Now()
	ps.downloading = true
	size := ps.filesize
	ps.mu.Unlock()

	c.debugf("Starting download (append=%v) to %s", appendMode, path)
	c.infof("Downloading %s → %s", entities.HumanReadableBytes(size), path)

	ps.downloadStartedOnce.Do(func() {
		close(ps.downloadStarted)
	})
	ps.lastActivity.Store(time.Now().UnixNano())

	go c.ackSender()
	go c.progressPrinter()
	go c.receiveData()
}

func (c *Client) startDownloadAppend(ps *packState) {
	ps.mu.Lock()
	peerAddr := ps.peerAddr
	ps.mu.Unlock()
	if peerAddr == "" {
		pack := c.currentPack()
		c.noticef("DCC resume failed: no peer address (bot=%s pack=%d)", pack.Bot, pack.PackNumber)
		c.finishWithErrorPS(ps, ErrDownloadFailed)
		return
	}
	c.startDownload(ps, peerAddr, true)
}

// receiveData reads incoming bytes from the DCC TCP connection and writes them
// to the destination file. It sends an ACK after every chunk (IRC DCC protocol
// requires the receiver to acknowledge each received byte count).
// When the connection closes (EOF) the defer block decides success/failure by
// comparing progress to the expected file size.
func (c *Client) receiveData() {
	// Capture packState locally so the entire function uses a consistent
	// snapshot. This prevents "sync: unlock of unlocked mutex" panics when
	// resetForPack() replaces c.ps in another goroutine (e.g. stallWatcher
	// triggers finishWithError → waitForCurrentPack returns → retry →
	// resetForPack) while this goroutine's defer is still running.
	//
	// Same safety argument as ackSender and progressPrinter: resetForPack()
	// closes the old downloadDone channel, which unblocks any pending
	// select on it. The captured packState remains valid for reading fields
	// even after c.ps is replaced — channels are closed, not freed.
	ps := c.ps
	downloadDone := ps.downloadDone

	// Pre-create throttle timer to avoid per-chunk allocations on long downloads.
	var throttleTimer *time.Timer
	if c.opts.ThrottleBytes > 0 {
		throttleTimer = time.NewTimer(0)
		if !throttleTimer.Stop() {
			<-throttleTimer.C
		}
	}

	defer func() {
		if throttleTimer != nil {
			throttleTimer.Stop()
		}
		ps.mu.Lock()
		ps.downloading = false
		if ps.dccFile != nil {
			ps.dccFile.Close()
		}
		if ps.dccConn != nil {
			ps.dccConn.Close()
			ps.dccConn = nil
		}
		size := ps.filesize
		ps.mu.Unlock()

		prog := atomic.LoadInt64(&ps.progress)
		if prog >= size && size > 0 {
			// Clear any previously recorded error (e.g. ErrTimeout from
			// stallWatcher) when all bytes have been received. The stall
			// watcher may have fired while the bot was sending the last
			// chunk but before the TCP connection was closed, setting
			// downloadError to ErrTimeout. Since we have the full file,
			// this is a clean success.
			//
			// NOTE: this is safe because prog >= size guarantees we
			// received every byte the bot advertised. If size were
			// misinterpreted (e.g. bot reports wrong size), the file
			// could be truncated yet still pass here — but there is no
			// way to detect that without an external checksum.
			ps.mu.Lock()
			if ps.downloadError != nil {
				c.noticef("Download complete (%d/%d bytes) — overriding previous error: %v", prog, size, ps.downloadError)
				ps.downloadError = nil
			}
			ps.mu.Unlock()
			c.logf("Download complete")
			elapsed := time.Since(ps.downStartTime)
			speedStr := formatSpeed(float64(size) / elapsed.Seconds())
			c.noticef("File %s downloaded successfully in %s at %s",
				c.currentPack().GetFilename(),
				formatDuration(elapsed),
				speedStr)
			// Complete on the captured packState (ps), not c.ps, to
			// avoid operating on a packState that resetForPack() may
			// have already replaced (e.g. stall → retry race).
			ps.downloadDoneOnce.Do(func() {
				close(ps.downloadDone)
			})
		} else if prog >= size {
			// Both are zero — no data was transferred. This is unlikely
			// for XDCC (bots rarely send zero-byte packs) but is a
			// valid edge case that should not be treated as an error.
			c.logf("Download complete (zero-byte file)")
			ps.downloadDoneOnce.Do(func() {
				close(ps.downloadDone)
			})
		} else {
			c.noticef("Download incomplete: got %d of %d bytes (bot=%s pack=%d)", prog, size, c.currentPack().Bot, c.currentPack().PackNumber)
			// Record the error on the captured packState so it's
			// visible to the caller (downloadPackAtIndex) even if
			// resetForPack() has already replaced c.ps.
			ps.mu.Lock()
			if ps.downloadError == nil {
				ps.downloadError = ErrDownloadFailed
			}
			ps.mu.Unlock()
			ps.downloadDoneOnce.Do(func() {
				close(ps.downloadDone)
			})
		}
	}()

	// Take a local reference to dccConn under lock to avoid a data race:
	// stallWatcher may concurrently set c.ps.dccConn = nil under c.ps.mu.
	ps.mu.Lock()
	conn := ps.dccConn
	ps.mu.Unlock()
	if conn == nil {
		return
	}

	bufPtr := bufPool.Get().(*dccBuffer) //nolint:errcheck // pool.New always returns *dccBuffer

	buf := bufPtr.data
	defer bufPool.Put(bufPtr)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			ps.mu.Lock()
			_, werr := ps.dccFile.Write(buf[:n])
			ps.mu.Unlock()
			if werr != nil {
				c.noticef("Write error (bot=%s pack=%d): %v", c.currentPack().Bot, c.currentPack().PackNumber, werr)
				return
			}
			atomic.AddInt64(&ps.progress, int64(n))
			ps.lastActivity.Store(time.Now().UnixNano())

			if c.opts.ThrottleBytes > 0 {
				ps.mu.Lock()
				delta := time.Since(ps.dccTimestamp).Seconds()
				chunkTime := float64(n) / float64(c.opts.ThrottleBytes)
				sleepTime := chunkTime - delta
				ps.dccTimestamp = time.Now()
				ps.mu.Unlock()
				if sleepTime > 0 {
					throttleTimer.Reset(time.Duration(sleepTime * float64(time.Second)))
					select {
					case <-throttleTimer.C:
					case <-downloadDone:
						return
					}
				}
			}
			c.enqueueACK(ps)
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) ackSender() {
	// Capture packState and its channels locally so the goroutine does not
	// dynamically read c.ps, which resetForPack() replaces between packs.
	// Without this, the goroutine could block forever on the new packState's
	// channels after c.ps is replaced (goroutine leak).
	//
	// This is safe because resetForPack() closes the old downloadDone
	// channel, which unblocks the select below. The captured packState
	// fields (ackQueue, downloadDone) remain valid for reading even after
	// c.ps is replaced — the channels are closed, not garbage-collected.
	ps := c.ps
	ackQueue := ps.ackQueue
	downloadDone := ps.downloadDone
	for {
		select {
		case ack := <-ackQueue:
			ps.mu.Lock()
			conn := ps.dccConn
			ps.mu.Unlock()
			if conn == nil {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(ack); err != nil {
				c.debugf("ACK write failed: %v", err)
				return
			}
		case <-downloadDone:
			return
		}
	}
}

// enqueueACK builds a big-endian ACK packet containing the current progress
// counter and queues it for the ackSender goroutine. The packet is 4 bytes for
// transfers ≤ 4 GiB, and 8 bytes for larger files (extended DCC ACK, RFC 2571).
// If the queue is full the ACK is dropped — the next chunk will enqueue a fresh one.
//
// The caller must pass its captured packState to avoid a data race on c.ps,
// which resetForPack() can replace concurrently.
func (c *Client) enqueueACK(ps *packState) {
	prog := atomic.LoadInt64(&ps.progress)
	var ack []byte
	if prog >= 0 && prog <= 0xFFFFFFFF {
		ack = make([]byte, 4)
		binary.BigEndian.PutUint32(ack, uint32(prog)) //nolint:gosec // prog is always >=0 when ≤0xFFFFFFFF
	} else {
		ack = make([]byte, 8)
		binary.BigEndian.PutUint64(ack, uint64(prog))
	}
	select {
	case ps.ackQueue <- ack:
	default:
	}
}

func (c *Client) progressPrinter() {
	// Capture packState and its channels locally so the goroutine does not
	// dynamically read c.ps, which resetForPack() replaces between packs.
	//
	// Same safety argument as ackSender: resetForPack() closes the old
	// downloadDone channel, which unblocks any pending select.
	ps := c.ps
	downloadDone := ps.downloadDone
	downloadStarted := ps.downloadStarted

	// Wait for the DCC transfer to start instead of busy-polling
	// c.ps.downloading with lock/unlock every 50ms.
	select {
	case <-downloadStarted:
	case <-downloadDone:
		return
	}

	// Guard against future misuse: verify ps.downloading is actually true
	// before entering the progress loop.
	ps.mu.Lock()
	dl := ps.downloading
	ps.mu.Unlock()
	if !dl {
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var lastProgress int64
	lastTime := time.Now()

	for {
		select {
		case <-ticker.C:
			prog := atomic.LoadInt64(&ps.progress)
			ps.mu.Lock()
			total := ps.filesize
			ps.mu.Unlock()
			elapsed := time.Since(lastTime).Seconds()
			speed := float64(prog-lastProgress) / elapsed
			lastProgress = prog
			lastTime = time.Now()

			// Call external progress callback if set
			if cb := c.opts.ProgressCallback; cb != nil && total > 0 {
				cb(prog, total, speed)
			}

			if c.opts.ProgressCallback == nil && c.verbosity >= 0 {
				// Only print to stdout when not using a callback
				pct := 0.0
				if total > 0 {
					pct = float64(prog) / float64(total) * 100
				}

				eta := ""
				if speed > 0 && total > prog {
					remaining := time.Duration(float64(total-prog)/speed) * time.Second
					if remaining < 90*time.Second {
						eta = fmt.Sprintf(" remaining: %ds", int(remaining.Seconds()))
					} else {
						eta = fmt.Sprintf(" remaining: %dm %ds",
							int(remaining.Minutes()), int(remaining.Seconds())%60)
					}
				}

				speedStr := formatSpeed(speed)

				fmt.Printf("\r  %.1f%% [%s / %s] %s%s    ",
					pct,
					entities.HumanReadableBytes(prog),
					entities.HumanReadableBytes(total),
					speedStr,
					eta)
			}

			ps.mu.Lock()
			dl := ps.downloading
			ps.mu.Unlock()
			if !dl {
				fmt.Println()
				return
			}
		case <-downloadDone:
			fmt.Println()
			return
		}
	}
}

// stallWatcher monitors transfer progress. On stall it closes the DCC
// connection (not the IRC connection) so the download can be retried.
func (c *Client) stallWatcher() {
	// Capture packState locally so the goroutine does not dynamically read
	// c.ps, which resetForPack() replaces between packs. Same safety
	// argument as ackSender and progressPrinter.
	ps := c.ps
	stall := time.Duration(c.opts.StallTimeout) * time.Second
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ps.downloadDone:
			return
		case <-ticker.C:
			last := ps.lastActivity.Load()
			if last == 0 {
				continue
			}
			idle := time.Since(time.Unix(0, last))
			if idle >= stall {
				c.noticef("Transfer stalled for %s (no data received), aborting",
					idle.Round(time.Second))
				ps.mu.Lock()
				if ps.dccConn != nil {
					ps.dccConn.Close()
					ps.dccConn = nil
				}
				ps.mu.Unlock()
				c.finishWithErrorPS(ps, ErrTimeout)
				return
			}
		}
	}
}
