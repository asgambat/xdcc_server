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
	if len(parts) < 5 {
		c.logf("Malformed DCC SEND: %v", parts)
		return
	}
	filename := parts[1]
	ipNum := parts[2]
	port := parts[3]
	sizeStr := parts[4]

	// Capture pack once at handler entry (fix 2.19) so all references within
	// this function use the same snapshot even if packIdxVal advances.
	pack := c.currentPack()

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

	c.ps.mu.Lock()
	c.ps.filesize = filesize
	c.ps.peerAddr = peerAddr
	c.ps.mu.Unlock()

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
			c.finishWithError(ErrAlreadyDownloaded)
			return
		}
		atomic.StoreInt64(&c.ps.progress, pos)
		resumeParam := fmt.Sprintf("%q %s %d", filename, port, pos)
		c.debugf("Resuming download from %s / %s",
			entities.HumanReadableBytes(pos), entities.HumanReadableBytes(filesize))
		c.logf("Sending DCC RESUME: %s", resumeParam)
		c.conn.irc.Cmd.SendCTCP(pack.Bot, "DCC", "RESUME "+resumeParam)
		return
	}

	c.startDownload(peerAddr, false)
}

func (c *Client) handleDCCAccept(parts []string) {
	if len(parts) < 4 {
		return
	}
	c.debugf("DCC ACCEPT: resuming download")
	c.startDownloadAppend()
}

func (c *Client) startDownload(addr string, appendMode bool) {
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendMode {
		flag = os.O_APPEND | os.O_WRONLY
	}

	pack := c.currentPack()
	path := pack.GetFilepath()
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		c.finishWithError(fmt.Errorf("cannot open file: %w", err))
		return
	}

	conn, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		f.Close()
		c.finishWithError(fmt.Errorf("DCC connection failed: %w", err))
		return
	}

	c.ps.mu.Lock()
	c.ps.dccFile = f
	c.ps.dccConn = conn
	c.ps.downStartTime = time.Now()
	c.ps.dccTimestamp = time.Now()
	c.ps.downloading = true
	size := c.ps.filesize
	c.ps.mu.Unlock()

	c.debugf("Starting download (append=%v) to %s", appendMode, path)
	c.infof("Downloading %s → %s", entities.HumanReadableBytes(size), path)

	c.ps.downloadStartedOnce.Do(func() {
		close(c.ps.downloadStarted)
	})
	c.ps.lastActivity.Store(time.Now().UnixNano())

	go c.ackSender()
	go c.progressPrinter()
	go c.receiveData()
}

func (c *Client) startDownloadAppend() {
	c.ps.mu.Lock()
	peerAddr := c.ps.peerAddr
	c.ps.mu.Unlock()
	if peerAddr == "" {
		c.finishWithError(ErrDownloadFailed)
		return
	}
	c.startDownload(peerAddr, true)
}

// receiveData reads incoming bytes from the DCC TCP connection and writes them
// to the destination file. It sends an ACK after every chunk (IRC DCC protocol
// requires the receiver to acknowledge each received byte count).
// When the connection closes (EOF) the defer block decides success/failure by
// comparing progress to the expected file size.
func (c *Client) receiveData() {
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
		c.ps.mu.Lock()
		c.ps.downloading = false
		if c.ps.dccFile != nil {
			c.ps.dccFile.Close()
		}
		if c.ps.dccConn != nil {
			c.ps.dccConn.Close()
			c.ps.dccConn = nil
		}
		size := c.ps.filesize
		c.ps.mu.Unlock()

		prog := atomic.LoadInt64(&c.ps.progress)
		if prog >= size {
			c.logf("Download complete")
			c.finishSuccess()
		} else {
			c.logf("Download incomplete: got %d of %d bytes", prog, size)
			c.finishWithError(ErrDownloadFailed)
		}
	}()

	// Take a local reference to dccConn under lock to avoid a data race:
	// stallWatcher may concurrently set c.ps.dccConn = nil under c.ps.mu.
	c.ps.mu.Lock()
	conn := c.ps.dccConn
	c.ps.mu.Unlock()
	if conn == nil {
		return
	}

	bufPtr := bufPool.Get().(*dccBuffer) //nolint:errcheck // pool.New always returns *dccBuffer

	buf := bufPtr.data
	defer bufPool.Put(bufPtr)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			c.ps.mu.Lock()
			_, werr := c.ps.dccFile.Write(buf[:n])
			c.ps.mu.Unlock()
			if werr != nil {
				c.logf("Write error: %v", werr)
				return
			}
			atomic.AddInt64(&c.ps.progress, int64(n))
			c.ps.lastActivity.Store(time.Now().UnixNano())

			if c.opts.ThrottleBytes > 0 {
				c.ps.mu.Lock()
				delta := time.Since(c.ps.dccTimestamp).Seconds()
				chunkTime := float64(n) / float64(c.opts.ThrottleBytes)
				sleepTime := chunkTime - delta
				c.ps.dccTimestamp = time.Now()
				c.ps.mu.Unlock()
				if sleepTime > 0 {
					throttleTimer.Reset(time.Duration(sleepTime * float64(time.Second)))
					select {
					case <-throttleTimer.C:
					case <-c.ps.downloadDone:
						return
					}
				}
			}
			c.enqueueACK()
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) ackSender() {
	for {
		select {
		case ack := <-c.ps.ackQueue:
			c.ps.mu.Lock()
			conn := c.ps.dccConn
			c.ps.mu.Unlock()
			if conn == nil {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Write(ack); err != nil {
				c.debugf("ACK write failed: %v", err)
				return
			}
		case <-c.ps.downloadDone:
			return
		}
	}
}

// enqueueACK builds a big-endian ACK packet containing the current progress
// counter and queues it for the ackSender goroutine. The packet is 4 bytes for
// transfers ≤ 4 GiB, and 8 bytes for larger files (extended DCC ACK, RFC 2571).
// If the queue is full the ACK is dropped — the next chunk will enqueue a fresh one.
func (c *Client) enqueueACK() {
	prog := atomic.LoadInt64(&c.ps.progress)
	var ack []byte
	if prog >= 0 && prog <= 0xFFFFFFFF {
		ack = make([]byte, 4)
		binary.BigEndian.PutUint32(ack, uint32(prog)) //nolint:gosec // prog is always >=0 when ≤0xFFFFFFFF
	} else {
		ack = make([]byte, 8)
		binary.BigEndian.PutUint64(ack, uint64(prog))
	}
	select {
	case c.ps.ackQueue <- ack:
	default:
	}
}

func (c *Client) progressPrinter() {
	// Wait for the DCC transfer to start instead of busy-polling
	// c.ps.downloading with lock/unlock every 50ms.
	select {
	case <-c.ps.downloadStarted:
	case <-c.ps.downloadDone:
		return
	}

	// Guard against future misuse: verify c.ps.downloading is actually true
	// before entering the progress loop.
	c.ps.mu.Lock()
	dl := c.ps.downloading
	c.ps.mu.Unlock()
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
			prog := atomic.LoadInt64(&c.ps.progress)
			c.ps.mu.Lock()
			total := c.ps.filesize
			c.ps.mu.Unlock()
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

			c.ps.mu.Lock()
			dl := c.ps.downloading
			c.ps.mu.Unlock()
			if !dl {
				fmt.Println()
				return
			}
		case <-c.ps.downloadDone:
			fmt.Println()
			return
		}
	}
}

// stallWatcher monitors transfer progress. On stall it closes the DCC
// connection (not the IRC connection) so the download can be retried.
func (c *Client) stallWatcher() {
	stall := time.Duration(c.opts.StallTimeout) * time.Second
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.ps.downloadDone:
			return
		case <-ticker.C:
			last := c.ps.lastActivity.Load()
			if last == 0 {
				continue
			}
			idle := time.Since(time.Unix(0, last))
			if idle >= stall {
				c.noticef("Transfer stalled for %s (no data received), aborting",
					idle.Round(time.Second))
				c.ps.mu.Lock()
				if c.ps.dccConn != nil {
					c.ps.dccConn.Close()
					c.ps.dccConn = nil
				}
				c.ps.mu.Unlock()
				c.finishWithError(ErrTimeout)
				return
			}
		}
	}
}
