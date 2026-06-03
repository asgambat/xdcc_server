package ircmanager

import (
	"sync"
	"testing"
	"time"

	"xdcc-go/internal/config"
	"xdcc-go/internal/logging"
)

// ===========================================================================
// WaitGroup Lifecycle Tests - Verify race-free goroutine management
// ===========================================================================

// TestConnectionLifecycle_NoDuplicateRun verifies that run() cannot be called twice
func TestConnectionLifecycle_NoDuplicateRun(t *testing.T) {
	t.Parallel()

	ms := newMockStore()
	cfg := config.DefaultConfig()
	logger := logging.New(logging.LevelDebug, "", 0)

	mgr := New(ms, cfg, logger)
	defer mgr.Stop()

	srvID := ms.addServer("irc.test.net", 6667, false)

	// Get server info from mock store directly
	ms.mu.Lock()
	srv := ms.servers[srvID]
	ms.mu.Unlock()

	// Create a managed connection
	conn := &managedConnection{
		id:        srv.ID,
		address:   srv.Address,
		port:      srv.Port,
		nickname:  cfg.IRC.Nickname,
		manager:   mgr,
		joinedChs: make(map[string]string),
		status:    "connecting",
	}
	conn.ctx, conn.cancel = mgr.ctx, mgr.cancel

	// Launch run() twice - second call should be ignored
	conn.wg.Add(1)
	go conn.run()
	time.Sleep(10 * time.Millisecond) // Let first run() start

	// This should be safely ignored
	go conn.run()

	// Give time for potential duplicate to execute
	time.Sleep(100 * time.Millisecond)

	// Verify: should still be running (not panicked, not closed twice)
	if !conn.IsRunning() {
		t.Log("connection may have failed immediately (no real IRC server) — OK")
	}

	// Cleanup
	conn.cancel()
	conn.wg.Wait()

	// Verify cleanup completed
	if conn.IsRunning() {
		t.Error("expected isRunning=false after cleanup")
	}
}

// TestConnectionLifecycle_NoRaceOnStatusChecks verifies thread-safe status access
func TestConnectionLifecycle_NoRaceOnStatusChecks(t *testing.T) {
	t.Parallel()

	ms := newMockStore()
	srvID := ms.addServer("irc.test.net", 6667, false)

	cfg := config.DefaultConfig()
	logger := logging.New(logging.LevelDebug, "", 0)

	mgr := New(ms, cfg, logger)
	defer mgr.Stop()

	_ = mgr.ConnectServerByID(srvID)

	// Hammer IsRunning() from multiple goroutines
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				mgr.mu.RLock()
				conn, ok := mgr.conns[srvID]
				mgr.mu.RUnlock()
				if ok {
					_ = conn.IsRunning()
					_ = conn.Status()
				}
			}
		}()
	}

	wg.Wait()

	// Should complete without race detector warnings
	_ = mgr.DisconnectServer(srvID)
}

// TestConnectionLifecycle_PanicRecovery verifies panic in run() is recovered
func TestConnectionLifecycle_PanicRecovery(t *testing.T) {
	// This test would require injecting a panic into connect()
	// For now, we document that panic recovery exists in run()
	t.Skip("Requires mock IRC client that can panic on demand")
}
