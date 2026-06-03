// Package diskmon monitors available disk space and provides helpers to check
// whether a download should proceed based on free space thresholds (Fase 9.2).
package diskmon

import (
	"fmt"
	"sync"
	"time"

	"xdcc-go/internal/logging"
)

// ---------------------------------------------------------------------------
// Monitor
// ---------------------------------------------------------------------------

// Monitor tracks disk space for a given path and emits warnings when space
// drops below a configurable threshold. It also supports auto-resume when
// space recovers.
type Monitor struct {
	mu sync.RWMutex

	path      string
	threshold int64 // minimum free bytes
	checkFn   func(path string) (available, total int64, err error)

	// State
	lowSpace    bool
	available   int64 // cached available bytes, updated by Check()
	lastChecked time.Time
	interval    time.Duration

	logger *logging.Logger
}

// New creates a new disk space monitor.
//   - path: directory to monitor
//   - threshold: minimum free bytes (e.g. 1 GB = 1073741824)
//   - checkFn: function to check available disk space; if nil, uses getDiskInfo
//   - logger: for status messages
func New(path string, threshold int64, checkFn func(string) (int64, int64, error), logger *logging.Logger) *Monitor {
	if checkFn == nil {
		checkFn = getDiskFree
	}
	return &Monitor{
		path:      path,
		threshold: threshold,
		checkFn:   checkFn,
		interval:  30 * time.Second,
		logger:    logger,
	}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Check returns the current disk space status.
//   - available: free bytes on the filesystem
//   - total: total bytes on the filesystem
//   - low: true if available < threshold
//   - error: if the check itself failed
func (m *Monitor) Check() (available, total int64, low bool, err error) {
	available, total, err = m.checkFn(m.path)
	if err != nil {
		return 0, 0, false, fmt.Errorf("disk check failed for %s: %w", m.path, err)
	}

	low = available < m.threshold

	m.mu.Lock()
	m.lowSpace = low
	m.available = available
	m.lastChecked = time.Now()
	m.mu.Unlock()

	return available, total, low, nil
}

// IsLowSpace returns the cached low-space status without performing a new check.
func (m *Monitor) IsLowSpace() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lowSpace
}

// Available returns the cached available bytes from the last Check() call.
func (m *Monitor) Available() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.available
}

// Threshold returns the configured minimum free bytes.
func (m *Monitor) Threshold() int64 {
	return m.threshold
}

// SetThreshold updates the minimum free bytes threshold.
func (m *Monitor) SetThreshold(threshold int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.threshold = threshold
}

// StartPeriodicCheck starts a goroutine that periodically checks disk space.
// The callback is invoked (non-blocking) whenever the low-space status changes.
// Returns a stop function and a done channel that closes when the goroutine exits.
func (m *Monitor) StartPeriodicCheck(onChange func(lowSpace bool, available int64)) (stop func(), done <-chan struct{}) {
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	var once sync.Once

	// Do an initial check
	if available, _, low, err := m.Check(); err == nil {
		if onChange != nil {
			onChange(low, available)
		}
	}

	go func() {
		defer close(doneCh) // Signal goroutine exit
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				// Read OLD value BEFORE Check() updates it
				m.mu.RLock()
				prevLow := m.lowSpace
				m.mu.RUnlock()

				// Check() updates m.lowSpace to NEW value
				available, _, low, err := m.Check()
				if err != nil {
					m.logger.Warnf("disk space check failed: %v", err)
					continue
				}

				// Now comparison works: OLD vs NEW
				if low != prevLow && onChange != nil {
					onChange(low, available)
				}

				if low {
					m.logger.Warnf("low disk space: %d bytes available (threshold: %d)", available, m.threshold)
				}
			}
		}
	}()

	return func() { once.Do(func() { close(stopCh) }) }, doneCh
}

// ---------------------------------------------------------------------------
// Platform-specific disk free check
// ---------------------------------------------------------------------------

// getDiskFree returns available and total bytes for the given path.
// Uses syscall.Statfs on Linux/macOS.
func getDiskFree(path string) (available, total int64, err error) {
	return getDiskUsage(path)
}

// FormatBytes returns a human-readable string for byte counts.
func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
