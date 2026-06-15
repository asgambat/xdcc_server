package queue

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xdcc_server/internal/config"
	"xdcc_server/internal/diskmon"
	"xdcc_server/internal/entities"
	xdccirc "xdcc_server/internal/irc"
	"xdcc_server/internal/logging"
	"xdcc_server/internal/pubsub"
	"xdcc_server/internal/store"
)

// slotKey builds the compound key used in channelSlots to enforce
// one-active-download-per-(server,channel) pair.
//
// When the channel is empty (to be discovered via WHOIS), the bot name is
// used as a discriminator so that different bots on the same server can
// download in parallel even before their channels are known.
func slotKey(serverAddr, channel, bot string) string {
	normCh := xdccirc.NormalizeChannel(channel)
	if normCh == "" {
		// Channel unknown — use bot name as discriminator so different bots
		// on the same server don't all serialize on "serverAddr|".
		return fmt.Sprintf("%s|~whois|%s", serverAddr, bot)
	}
	return fmt.Sprintf("%s|%s", serverAddr, normCh)
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// Manager manages the download queue. It enforces:
//   - Max 1 active download per IRC channel
//   - A global parallel download limit (default 5)
//   - FIFO priority per-channel queue
//   - Persistence via SQLite store
//   - Real-time events for SSE propagation
//   - Persistent IRC connections via IRCManager (when available)
type Manager struct {
	store store.DownloadStore
	cfg   *config.Config
	log   *logging.Logger

	// IRCManager for persistent connections (optional - if nil, uses temporary connections)
	ircMgr IRCManagerInterface

	mu sync.RWMutex
	//	dispatchMu serializes calls to tryDispatch, preventing concurrent
	// dispatches from racing on activeCount/channelSlots checks and
	// starting more downloads than allowed by the configured limits.
	dispatchMu sync.Mutex
	// activeJobs tracks currently running downloads: download ID → cancel function
	activeJobs map[int64]context.CancelFunc
	// channelSlots tracks which (server, channel) pairs currently have an active
	// or reserved dispatch slot.
	// Key format: "serverAddress|normalizedChannel" so different channels on the
	// same server can download in parallel.
	channelSlots map[string]int64 // "server|channel" → download ID
	// globalCount is the number of active or reserved dispatch slots.
	globalCount int

	// event subscriber hub
	subscriber *pubsub.Hub[Event]

	// main context for lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	// closing prevents new dispatch/enqueue while Stop is in progress.
	closing atomic.Bool

	// Track active download goroutines for clean shutdown
	downloadWg sync.WaitGroup

	// Disk monitor for available space checks
	diskMon       *diskmon.Monitor
	diskLow       bool
	stopDiskCheck func()
	diskCheckDone <-chan struct{}

	// startupReady is closed once the startup grace period has elapsed,
	// allowing tryDispatch to proceed. This prevents downloads from starting
	// before IRC servers have had time to connect and join channels.
	startupReady chan struct{}

	// startupTimer tracks the delayed-start timer so Stop() can cancel it.
	// If the shutdown happens before the timer fires, the callback won't
	// call tryDispatch() outside the intended lifecycle.
	startupTimer *time.Timer
}

// IRCManagerInterface defines the methods needed from ircmanager for downloads.
type IRCManagerInterface interface {
	// DownloadPack performs a download using persistent IRC connections.
	// Returns the downloaded file path on success.
	DownloadPack(ctx context.Context, pack *entities.XDCCPack, channel string, progressFn func(bytesReceived, totalBytes int64, speedBPS float64)) (string, error)
}

// New creates a new Manager.
// ircMgr is optional - if nil, downloads will use temporary IRC connections.
func New(st store.DownloadStore, cfg *config.Config, logger *logging.Logger) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	qm := &Manager{
		store:        st,
		cfg:          cfg,
		log:          logger,
		activeJobs:   make(map[int64]context.CancelFunc),
		channelSlots: make(map[string]int64),
		subscriber:   pubsub.New[Event](512),
		ctx:          ctx,
		cancel:       cancel,
		done:         make(chan struct{}),
		startupReady: make(chan struct{}),
	}

	// Initialize disk monitor if threshold > 0
	if cfg.Download.MinDiskSpace > 0 {
		qm.diskMon = diskmon.New(cfg.Download.TempDir, cfg.Download.MinDiskSpace, nil, logger)
		// Start periodic check — auto-resume when space recovers
		qm.stopDiskCheck, qm.diskCheckDone = qm.diskMon.StartPeriodicCheck(func(low bool, _ int64) {
			qm.mu.Lock()
			qm.diskLow = low
			qm.mu.Unlock()
			if low {
				logger.Warnf("DISK LOW: queue paused until disk space recovers")
				qm.emitEvent(Event{
					Type: EventDiskSpaceLow,
				})
			} else {
				logger.Infof("DISK OK: space recovered, resuming queue")
				qm.emitEvent(Event{
					Type: EventDiskSpaceOK,
				})
				qm.tryDispatch()
			}
		})
	}

	return qm
}

// SetIRCManager sets the IRC manager for persistent connections.
// This should be called before starting the queue manager.
func (qm *Manager) SetIRCManager(ircMgr IRCManagerInterface) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.ircMgr = ircMgr
	qm.log.Infof("IRC Manager attached to Queue Manager - downloads will use persistent connections")
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start begins the periodic monitor goroutine. It should be called after
// the store has been initialized and migrations have run.
//
// On startup, it waits for the configured startup delay before allowing
// dispatch. This gives IRC servers time to connect and join channels.
func (qm *Manager) Start() error {
	// Start the periodic monitor goroutine immediately so Stop() can
	// cleanly shut it down even during the startup delay.
	go qm.monitorLoop()

	if qm.cfg.Download.StartupDelayMinutes > 0 {
		delay := time.Duration(qm.cfg.Download.StartupDelayMinutes) * time.Minute
		qm.log.Infof("queue manager: delaying dispatch by %v to allow IRC connections to establish", delay)
		qm.startupTimer = time.AfterFunc(delay, func() {
			close(qm.startupReady)
			qm.tryDispatch()
		})
	} else {
		close(qm.startupReady)
		qm.tryDispatch()
	}

	return nil
}

// Stop cancels all active downloads and stops the monitor.
func (qm *Manager) Stop() {
	// Mark manager as closing first so Enqueue/tryDispatch reject new work.
	// If Stop was already called, return immediately.
	if qm.closing.Swap(true) {
		return
	}

	// Wait for any in-flight tryDispatch to complete before stopping timers
	// and monitors. This reduces races between shutdown and new starts.
	qm.dispatchMu.Lock()
	qm.dispatchMu.Unlock()

	// Stop the startup timer so the delayed dispatch callback doesn't fire
	// after shutdown has begun, calling tryDispatch() outside the lifecycle.
	if qm.startupTimer != nil {
		qm.startupTimer.Stop()
	}

	// Stop disk monitor first and wait for goroutine to exit
	if qm.stopDiskCheck != nil {
		qm.stopDiskCheck()
		<-qm.diskCheckDone
	}

	qm.cancel()
	<-qm.done

	// Close the subscriber hub to unblock any SSE handlers still
	// waiting on events. This prevents goroutine leaks in SSE clients
	// that outlive the queue manager.
	qm.subscriber.Close()

	// Save progress of all active downloads before canceling
	qm.mu.RLock()
	ids := make([]int64, 0, len(qm.activeJobs))
	for id := range qm.activeJobs {
		ids = append(ids, id)
	}
	qm.mu.RUnlock()

	// Use a timed context to prevent indefinite blocking if SQLite hangs
	// during shutdown. qm.ctx is already cancelled at this point, so a
	// bare context.Background() would let store operations block forever.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	for _, id := range ids {
		// Save progress before cancellation
		if d, err := qm.store.GetDownload(shutdownCtx, id); err == nil && d != nil && d.ProgressBytes > 0 {
			qm.log.Infof("shutdown: saving progress for download %d: %d/%d bytes", id, d.ProgressBytes, d.FileSize)
		}
		_ = qm.cancelDownloadWithCtx(shutdownCtx, id, "server shutting down")
	}

	// Wait for all download workers to complete with timeout
	downloadsDone := make(chan struct{})
	go func() {
		qm.downloadWg.Wait()
		close(downloadsDone)
	}()

	select {
	case <-downloadsDone:
		qm.log.Infof("all download workers stopped cleanly")
	case <-time.After(10 * time.Second):
		qm.log.Warnf("download workers did not stop within 10s")
	}
}

// monitorLoop periodically checks for queued downloads that can be started.
// It runs every 10 seconds by default.
func (qm *Manager) monitorLoop() {
	defer close(qm.done)

	// Dispatch interval: 10 seconds by default
	// (configurable via dedicated config field in a future update)
	interval := 10 * time.Second

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-qm.ctx.Done():
			return
		case <-ticker.C:
			qm.tryDispatch()
		}
	}
}

// ---------------------------------------------------------------------------
// Subscribe / Unsubscribe (for SSE propagation)
// ---------------------------------------------------------------------------

// Subscribe returns a channel that receives queue events.
func (qm *Manager) Subscribe() chan Event {
	return qm.subscriber.Subscribe()
}

// Unsubscribe removes a previously subscribed channel.
func (qm *Manager) Unsubscribe(ch chan Event) {
	qm.subscriber.Unsubscribe(ch)
}

// emitEvent sends an event to all subscribers.
func (qm *Manager) emitEvent(evt Event) {
	evt.Timestamp = time.Now()
	qm.subscriber.Publish(evt)
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Enqueue adds a download to the queue. It persists the record first, then
// tries to dispatch immediately.
//
// packMessage is the raw XDCC message (e.g. "xdcc send #123").
// The caller should already have validated it.
func (qm *Manager) Enqueue(d store.DownloadRecord) (int64, error) {
	if qm.closing.Load() {
		return 0, fmt.Errorf("queue manager is shutting down")
	}

	// Normalize channel (if provided)
	// Channel is optional - if empty, WHOIS will discover it during download
	d.Channel = xdccirc.NormalizeChannel(d.Channel)

	// Check for duplicate by bot + pack message
	dupByMsg, err := qm.store.GetDownloadByBotMessage(qm.ctx, d.Bot, d.PackMessage)
	if err == nil && dupByMsg != nil && dupByMsg.Status != store.DownloadStatusCompleted {
		return 0, fmt.Errorf("duplicate download: already %s (id=%d)", dupByMsg.Status, dupByMsg.ID)
	}

	// Check disk space before enqueuing
	if qm.diskMon != nil {
		_, _, low, err := qm.diskMon.Check()
		if err == nil && low {
			return 0, fmt.Errorf("insufficient disk space: %s available, need %s",
				diskmon.FormatBytes(qm.cfg.Download.MinDiskSpace),
				diskmon.FormatBytes(qm.cfg.Download.MinDiskSpace))
		}
	}

	// Set default priority
	if d.Priority == 0 {
		d.Priority = 100
	}

	if qm.closing.Load() {
		return 0, fmt.Errorf("queue manager is shutting down")
	}

	id, err := qm.store.EnqueueDownload(qm.ctx, d)
	if err != nil {
		return 0, fmt.Errorf("enqueueing download: %w", err)
	}
	d.ID = id

	qm.log.Infof("enqueued download %d: %s from %s on %s/%s",
		id, d.Filename, d.Bot, d.ServerAddress, d.Channel)

	// Emit event
	qm.emitEvent(Event{
		Type:          EventDownloadQueued,
		DownloadID:    id,
		Bot:           d.Bot,
		ServerAddress: d.ServerAddress,
		Channel:       d.Channel,
		Filename:      d.Filename,
		FileSize:      d.FileSize,
	})

	// Try to start the download immediately
	qm.tryDispatch()

	return id, nil
}

// CancelDownload cancels a download by its ID. If the download is active,
// its context is cancelled. If it's queued, it's just removed from the queue.
// The download record is updated in the store.
func (qm *Manager) CancelDownload(id int64, reason string) error {
	return qm.cancelDownloadWithCtx(qm.ctx, id, reason)
}

// cancelDownloadWithCtx is the internal version of CancelDownload that accepts
// an explicit context. During shutdown (Stop), qm.ctx is already cancelled,
// so callers must pass context.Background() to ensure store operations succeed.
func (qm *Manager) cancelDownloadWithCtx(ctx context.Context, id int64, reason string) error {
	qm.mu.Lock()
	cancelFn, active := qm.removeActiveJobLocked(id)
	qm.mu.Unlock()

	if active {
		cancelFn()
		qm.log.Infof("cancelled active download %d: %s", id, reason)
	}

	// Update store with the provided context
	d, err := qm.store.GetDownload(ctx, id)
	if err != nil || d == nil {
		return err
	}

	// If it was active but not yet completed, mark it as queued for retry
	if active && d.Status == store.DownloadStatusDownloading {
		_ = qm.store.RequeueDownload(ctx, id)
	}

	return nil
}

// PauseDownload pauses a download. If it's currently downloading, the
// context is cancelled (the partial file remains for potential resume).
func (qm *Manager) PauseDownload(id int64) error {
	qm.mu.Lock()
	cancelFn, active := qm.removeActiveJobLocked(id)
	qm.mu.Unlock()

	if active {
		cancelFn()
	}

	err := qm.store.MarkDownloadPaused(qm.ctx, id)
	if err != nil {
		return err
	}

	qm.emitEvent(Event{
		Type:       EventDownloadPaused,
		DownloadID: id,
	})

	// Try to dispatch next download
	qm.tryDispatch()

	return nil
}

// ResumeDownload resumes a paused or failed download by re-queueing it.
func (qm *Manager) ResumeDownload(id int64) error {
	err := qm.store.RetryDownload(qm.ctx, id)
	if err != nil {
		return err
	}

	qm.log.Infof("resumed download %d", id)
	qm.tryDispatch()
	return nil
}

// RemoveDownload removes a download from the queue entirely.
func (qm *Manager) RemoveDownload(id int64) error {
	qm.mu.Lock()
	cancelFn, active := qm.removeActiveJobLocked(id)
	qm.mu.Unlock()

	if active {
		cancelFn()
	}

	err := qm.store.DeleteDownload(qm.ctx, id)
	if err != nil {
		return err
	}

	qm.emitEvent(Event{
		Type:       EventDownloadRemoved,
		DownloadID: id,
	})

	if active {
		qm.tryDispatch()
	}

	return nil
}

// BulkAction performs an action on multiple downloads.
// actions: "pause", "resume", "remove"
// Returns per-ID results.
func (qm *Manager) BulkAction(ids []int64, action string) (map[int64]string, error) {
	results := make(map[int64]string)

	for _, id := range ids {
		var err error
		switch strings.ToLower(action) {
		case "pause":
			err = qm.PauseDownload(id)
		case "resume":
			err = qm.ResumeDownload(id)
		case "remove":
			err = qm.RemoveDownload(id)
		default:
			results[id] = fmt.Sprintf("unknown action: %s", action)
			continue
		}
		if err != nil {
			results[id] = err.Error()
		} else {
			results[id] = "success"
		}
	}

	qm.emitEvent(Event{
		Type: EventDownloadBulkResult,
	})

	return results, nil
}

// GetActiveCount returns the number of currently active downloads.
func (qm *Manager) GetActiveCount() int {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return qm.globalCount
}

// GetActiveIDs returns the IDs of all currently active downloads.
func (qm *Manager) GetActiveIDs() []int64 {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	ids := make([]int64, 0, len(qm.activeJobs))
	for id := range qm.activeJobs {
		ids = append(ids, id)
	}
	return ids
}

// ---------------------------------------------------------------------------
// Internal dispatch logic
// ---------------------------------------------------------------------------

// tryDispatch checks the queue and starts as many downloads as possible
// up to the per-channel and global limits.
func (qm *Manager) tryDispatch() {
	// Serialize dispatch to prevent concurrent callers (Enqueue,
	// monitorLoop, completeFn, disk-check callback, etc.) from racing
	// on channelSlots/globalCount and starting duplicate downloads.
	qm.dispatchMu.Lock()
	defer qm.dispatchMu.Unlock()

	if qm.closing.Load() {
		return
	}

	// Check if we're shutting down
	select {
	case <-qm.ctx.Done():
		return
	default:
	}

	// Wait for startup grace period before dispatching downloads.
	// This prevents downloads from starting before IRC servers are ready.
	select {
	case <-qm.startupReady:
		// proceed
	default:
		return
	}

	// Check disk space before dispatching
	qm.mu.RLock()
	diskLow := qm.diskLow
	qm.mu.RUnlock()
	if diskLow {
		return
	}

	// Check fresh disk space if monitor is active
	if qm.diskMon != nil {
		_, _, low, err := qm.diskMon.Check()
		if err == nil && low {
			qm.mu.Lock()
			qm.diskLow = true
			qm.mu.Unlock()
			return
		}
	}

	maxParallel := qm.cfg.Download.MaxParallelTotal
	if maxParallel < 1 {
		maxParallel = 5
	}

	qm.mu.RLock()
	activeCount := qm.globalCount
	qm.mu.RUnlock()

	if activeCount >= maxParallel {
		return // At global limit
	}

	// Get all queued downloads, ordered by priority then creation time
	queue, err := qm.store.GetQueue(qm.ctx)
	if err != nil {
		qm.log.Warnf("failed to get queue: %v", err)
		return
	}

	for _, d := range queue {
		if qm.closing.Load() {
			return
		}

		if d.Status != store.DownloadStatusQueued {
			continue
		}
		if activeCount >= maxParallel {
			break
		}

		// Build compound slot key (server, channel) so downloads on different
		// channels of the same server can run in parallel. When channel is
		// unknown, the bot name is used as discriminator.
		sk := slotKey(d.ServerAddress, d.Channel, d.Bot)

		// Reserve slot and global parallel budget atomically before any
		// potentially slow operation (DB write / goroutine start). This closes
		// the check-then-act window between dispatch decision and activation.
		reserved := false
		channelBusy := false
		qm.mu.Lock()
		if qm.globalCount >= maxParallel {
			activeCount = qm.globalCount
		} else if _, channelBusy = qm.channelSlots[sk]; channelBusy {
			activeCount = qm.globalCount
		} else {
			qm.channelSlots[sk] = d.ID
			qm.globalCount++
			activeCount = qm.globalCount
			reserved = true
		}
		qm.mu.Unlock()

		if !reserved {
			if activeCount >= maxParallel {
				break
			}
			if channelBusy {
				qm.log.Debugf("dispatch: slot [%s] BUSY — download %d (%s/%s bot=%s) waiting",
					sk, d.ID, d.ServerAddress, d.Channel, d.Bot)
			}
			continue // Slot occupied or global limit reached while dispatching.
		}

		qm.log.Debugf("dispatch: slot [%s] RESERVED — starting download %d (%s/%s bot=%s)",
			sk, d.ID, d.ServerAddress, d.Channel, d.Bot)

		if qm.closing.Load() {
			activeCount = qm.rollbackDispatchReservation(d.ID, sk)
			return
		}

		if err := qm.startDownload(d, sk); err != nil {
			activeCount = qm.rollbackDispatchReservation(d.ID, sk)
			qm.log.Warnf("dispatch: failed to start download %d on slot [%s]: %v", d.ID, sk, err)
			continue
		}
	}
}

// startDownload begins a download in a new goroutine.
// The dispatch slot and global count must already be reserved by tryDispatch.
func (qm *Manager) startDownload(d store.DownloadRecord, sk string) error {
	if qm.closing.Load() {
		return fmt.Errorf("queue manager is shutting down")
	}

	// Mark as downloading in store
	if err := qm.store.MarkDownloadStarted(qm.ctx, d.ID); err != nil {
		return fmt.Errorf("marking download %d as started: %w", d.ID, err)
	}

	if qm.closing.Load() {
		_ = qm.store.RequeueDownload(qm.storeCtxForCallbacks(), d.ID)
		return fmt.Errorf("queue manager is shutting down")
	}

	ctx, cancel := context.WithCancel(qm.ctx)

	qm.mu.Lock()
	existingID, ok := qm.channelSlots[sk]
	if !ok || existingID != d.ID {
		qm.mu.Unlock()
		cancel()
		// Keep store state coherent if reservation vanished after MarkDownloadStarted.
		_ = qm.store.RequeueDownload(qm.storeCtxForCallbacks(), d.ID)
		if !ok {
			return fmt.Errorf("slot reservation missing for download %d (%s)", d.ID, sk)
		}
		return fmt.Errorf("slot reservation mismatch for download %d (%s): owner=%d", d.ID, sk, existingID)
	}
	qm.activeJobs[d.ID] = cancel
	qm.mu.Unlock()

	qm.log.Infof("started download %d: slot [%s] acquired — %s from %s on %s/%s",
		d.ID, sk, d.Filename, d.Bot, d.ServerAddress, d.Channel)

	qm.emitEvent(Event{
		Type:          EventDownloadStarted,
		DownloadID:    d.ID,
		Bot:           d.Bot,
		ServerAddress: d.ServerAddress,
		Channel:       d.Channel,
		Filename:      d.Filename,
		FileSize:      d.FileSize,
	})

	// Build the pack object here so it can be captured in the goroutine closure.
	// The pack object is shared with the IRC client and will be updated
	// by the bot notice handler as metadata is discovered.
	server := entities.NewIrcServerWithPort(d.ServerAddress, 6667)
	packNumber := entities.ExtractPackNumber(d.PackMessage)
	pack := entities.NewXDCCPack(server, d.Bot, packNumber)
	pack.SetFilename(d.Filename, true)
	pack.SetSize(d.FileSize)
	pack.SetDirectory(qm.cfg.Download.TempDir)

	// metadataEmitted and startTime are intentionally confined to this worker
	// closure. Today runDownload invokes progressFn/completeFn in a single
	// serialized execution flow. If callback invocation becomes concurrent in
	// future, protect these variables with a mutex/atomics.
	// Track whether we've already emitted a metadata update for this download.
	// This prevents redundant store writes and SSE events after the first discovery.
	metadataEmitted := false

	// Record download start time for computing actual average speed.
	// Set on the first progress callback with bytesReceived > 0 so only
	// the DCC data transfer time is measured (excluding WHOIS/JOIN/XDCC).
	var startTime time.Time

	// Prepare worker config
	wCfg := DownloadConfig{
		TempDir:          qm.cfg.Download.TempDir,
		DestDir:          qm.cfg.Download.DestDir,
		ConflictPolicy:   qm.cfg.Download.ConflictPolicy,
		MaxRateBPS:       qm.cfg.Download.MaxRateBPS,
		Nickname:         qm.cfg.IRC.Nickname,
		ChannelJoinDelay: qm.cfg.Download.ChannelJoinDelay, // from config: -1=random, 0=no delay, >0=fixed
		Logger:           qm.log,
		IRCManager:       qm.ircMgr, // Pass IRC Manager for persistent connections
	}

	// Track download goroutine for clean shutdown
	qm.downloadWg.Add(1)
	go func() {
		defer qm.downloadWg.Done()

		// Progress callback: update store and emit events.
		// Guard: skip updates if the download context has been cancelled
		// (e.g. by PauseDownload or RemoveDownload) to avoid racing with
		// store status changes.
		progressFn := func(bytesReceived, totalBytes int64, speedBPS float64) {
			// Capture DCC transfer start on first data received. This
			// excludes WHOIS/JOIN/XDCC overhead from the speed calculation.
			if bytesReceived > 0 && startTime.IsZero() {
				startTime = time.Now()
			}

			select {
			case <-ctx.Done():
				return
			default:
			}

			// Update store (status is NOT set here — MarkDownloadStarted
			// already set it before this callback began).
			storeCtx := qm.storeCtxForCallbacks()
			_ = qm.store.UpdateDownloadProgress(storeCtx, d.ID, bytesReceived, int64(speedBPS))

			// Emit progress event
			qm.emitEvent(Event{
				Type:          EventDownloadProgress,
				DownloadID:    d.ID,
				ProgressBytes: bytesReceived,
				FileSize:      totalBytes,
				SpeedBPS:      speedBPS,
				Filename:      pack.Filename,
			})

			// If we discovered filename/size from the pack, update store and emit metadata event.
			// Only emit once (when metadataEmitted is still false) to avoid redundant SSE events.
			if !metadataEmitted && pack.Filename != "" && (d.Filename == "" || strings.HasPrefix(d.Filename, "manual_download")) {
				_ = qm.store.UpdateDownloadMetadata(storeCtx, d.ID, pack.Filename, pack.Size)
				qm.emitEvent(Event{
					Type:       EventDownloadMetadataUpdate,
					DownloadID: d.ID,
					Filename:   pack.Filename,
					FileSize:   pack.Size,
				})
				metadataEmitted = true
			}
		}

		// Completion callback
		completeFn := func(result workerResult) {
			// Only clean up if the download is still tracked as active.
			// If CancelDownload/PauseDownload/RemoveDownload already
			// removed it from activeJobs, skip cleanup to prevent
			// double-decrement of globalCount and double slot release.
			qm.mu.Lock()
			_, stillActive := qm.removeActiveJobLocked(d.ID)
			qm.mu.Unlock()

			if !stillActive {
				// Another goroutine (Pause/Remove/Cancel) already handled
				// cleanup and set the appropriate store status. Just try
				// to dispatch the next queued download.
				qm.tryDispatch()
				return
			}

			// Check if the store status was changed externally (e.g. paused,
			// cancelled/requeued) before we overwrite it. If the user
			// explicitly paused, cancelled, or removed the download, respect
			// that decision.
			storeCtx := qm.storeCtxForCallbacks()
			current, err := qm.store.GetDownload(storeCtx, d.ID)
			if err == nil && current != nil {
				if current.Status == store.DownloadStatusPaused ||
					current.Status == store.DownloadStatusQueued {
					// Status was already set externally — don't overwrite
					return
				}
			} else if current == nil {
				// Record was deleted (removed from queue) — nothing to update
				return
			}

			if result.Error != nil {
				// Download failed
				errStr := result.Error.Error()
				_ = qm.store.MarkDownloadFailed(storeCtx, d.ID, errStr)

				qm.log.Errorf("download %d FAILED — bot=%s server=%s channel=%q file=%q error=%s",
					d.ID, d.Bot, d.ServerAddress, d.Channel, d.Filename, errStr)

				// Emit failure event
				qm.emitEvent(Event{
					Type:          EventDownloadFailed,
					DownloadID:    d.ID,
					Bot:           d.Bot,
					ServerAddress: d.ServerAddress,
					Channel:       d.Channel,
					Filename:      d.Filename,
					ErrorMessage:  errStr,
				})

				// Try fallback or next from queue
				qm.handleFallback(d, result)
			} else if result.Skipped {
				// File was skipped because destination already exists
				_ = qm.store.MarkDownloadSkipped(storeCtx, d.ID)

				// Use discovered filename if record filename was empty
				skippedFilename := d.Filename
				if skippedFilename == "" && pack.Filename != "" {
					skippedFilename = pack.Filename
				}

				qm.log.Infof("download %d skipped: %s already exists at %s", d.ID, skippedFilename, result.FilePath)

				qm.emitEvent(Event{
					Type:          EventDownloadSkipped,
					DownloadID:    d.ID,
					Bot:           d.Bot,
					ServerAddress: d.ServerAddress,
					Channel:       d.Channel,
					Filename:      skippedFilename,
					FileSize:      result.FileSize,
				})
			} else {
				// Download completed successfully
				// Use discovered filename/size from pack if record was empty.
				finalFilename := d.Filename
				if finalFilename == "" && pack.Filename != "" {
					finalFilename = pack.Filename
				}
				finalSize := d.FileSize
				if finalSize == 0 && pack.Size > 0 {
					finalSize = pack.Size
				}
				_ = qm.store.MarkDownloadCompleted(storeCtx, d.ID, finalFilename, finalSize)

				qm.log.Infof("download %d COMPLETED — bot=%s server=%s file=%s -> %s",
					d.ID, d.Bot, d.ServerAddress, finalFilename, result.FilePath)

				// Update channel average download speed using EMA.
				// Use the channel from the DownloadRecord, but if it was
				// discovered via WHOIS during the download, fall back to
				// the channel stored on the pack object.
				ch := d.Channel
				if ch == "" {
					ch = pack.Channel
				}
				// Compute actual average speed: file_size / elapsed_seconds.
				// This is more accurate than the last instantaneous speedBPS.
				if ch != "" && result.FileSize > 0 && !startTime.IsZero() {
					elapsed := time.Since(startTime).Seconds()
					if elapsed > 0 {
						avgSpeedBPS := float64(result.FileSize) / elapsed
						_ = qm.store.UpdateChannelAvgSpeed(storeCtx, d.ServerAddress, ch, avgSpeedBPS)
					}
				}

				// Emit completion event with discovered filename
				qm.emitEvent(Event{
					Type:          EventDownloadCompleted,
					DownloadID:    d.ID,
					Bot:           d.Bot,
					ServerAddress: d.ServerAddress,
					Channel:       d.Channel,
					Filename:      finalFilename,
					FileSize:      result.FileSize,
				})
			}

			// Try to dispatch the next download for this channel
			qm.tryDispatch()
		}

		runDownload(ctx, d, pack, wCfg, progressFn, completeFn)
	}()

	return nil
}

// rollbackDispatchReservation releases a previously reserved (server, channel)
// slot and decrements the global active count if the reservation still belongs
// to the specified download ID.
func (qm *Manager) rollbackDispatchReservation(downloadID int64, sk string) int {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	if existingID, ok := qm.channelSlots[sk]; ok && existingID == downloadID {
		delete(qm.channelSlots, sk)
		if qm.globalCount > 0 {
			qm.globalCount--
		}
	}

	return qm.globalCount
}

// storeCtxForCallbacks returns a context suitable for store updates emitted
// by worker callbacks. During shutdown qm.ctx is cancelled, so use a
// time-bounded fallback context to allow final state persistence without
// risking indefinite blocking on an unresponsive database.
func (qm *Manager) storeCtxForCallbacks() context.Context {
	if qm.ctx.Err() != nil {
		ctx, _ := context.WithTimeout(context.Background(), 3*time.Second)
		return ctx
	}
	return qm.ctx
}

// removeActiveJobLocked removes an active job and its reserved slot atomically.
// qm.mu must be held by the caller.
func (qm *Manager) removeActiveJobLocked(downloadID int64) (context.CancelFunc, bool) {
	cancelFn, active := qm.activeJobs[downloadID]
	if !active {
		return nil, false
	}

	delete(qm.activeJobs, downloadID)
	if qm.globalCount > 0 {
		qm.globalCount--
	}

	for sk, existingID := range qm.channelSlots {
		if existingID == downloadID {
			delete(qm.channelSlots, sk)
			break
		}
	}

	return cancelFn, true
}

// ---------------------------------------------------------------------------
// Fallback handling (Fase 4.11)
// ---------------------------------------------------------------------------

// handleFallback attempts to find and start an alternative download for a
// failed job, based on the configured fallback mode (Fase 9.7).
//
// Guardrails:
//   - Max retry attempts per download (configurable, default 3)
//   - No auto-retry if mode is "suggest_only"
//   - Clear tracking of fallback reason in log
func (qm *Manager) handleFallback(original store.DownloadRecord, result workerResult) {
	mode := qm.cfg.Download.FailFallback
	if mode != "auto_retry_best" {
		qm.log.Errorf("fallback: download %d failed, mode is %q (no auto-retry); suggestion: consider alternative pack for %q",
			original.ID, mode, original.Filename)
		return
	}

	// Defense: only retry downloads that actually failed. Skipped or
	// externally-paused/completed downloads must not be retried.
	if result.Skipped {
		return
	}

	// Fetch the latest record to get the authoritative status and retry count.
	current, err := qm.store.GetDownload(qm.ctx, original.ID)
	if err != nil || current == nil {
		return
	}
	if current.Status != store.DownloadStatusFailed {
		return
	}

	// Check max retry attempts guardrail
	maxRetries := qm.cfg.Download.MaxRetryAttempts
	if maxRetries < 1 {
		maxRetries = 3
	}

	retryCount := current.RetryCount

	if retryCount >= maxRetries {
		qm.log.Errorf("fallback: download %d failed permanently after %d retries (max %d)",
			original.ID, retryCount, maxRetries)
		qm.emitEvent(Event{
			Type:          EventDownloadFailed,
			DownloadID:    original.ID,
			Bot:           original.Bot,
			ServerAddress: original.ServerAddress,
			Channel:       original.Channel,
			Filename:      original.Filename,
			ErrorMessage:  fmt.Sprintf("failed after %d retries: %v", retryCount, result.Error),
		})
		return
	}

	// Requeue the download first, then increment the retry counter
	// so the count reflects a successfully queued retry.
	qm.log.Infof("fallback: auto-retrying download %d (attempt %d/%d)",
		original.ID, retryCount+1, maxRetries)

	if err := qm.store.RetryDownload(qm.ctx, original.ID); err != nil {
		qm.log.Errorf("fallback: retry failed for download %d: %v", original.ID, err)
		return
	}

	if err := qm.store.IncrementDownloadRetry(qm.ctx, original.ID); err != nil {
		qm.log.Warnf("fallback: IncrementDownloadRetry failed for download %d: %v", original.ID, err)
	}

	qm.emitEvent(Event{
		Type:         EventDownloadAlternative,
		DownloadID:   original.ID,
		Filename:     original.Filename,
		ErrorMessage: fmt.Sprintf("auto-retry attempt %d/%d", retryCount+1, maxRetries),
	})
}

// ---------------------------------------------------------------------------
// Bandwidth management helpers (Fase 4.12)
// ---------------------------------------------------------------------------

// GetEffectiveMaxRate returns the effective max rate for the current time slot.
// This respects time-based bandwidth profiles (quiet hours).
// For now, it returns the configured max_rate_bps directly.
func (qm *Manager) GetEffectiveMaxRate() int64 {
	return qm.cfg.Download.MaxRateBPS
}
