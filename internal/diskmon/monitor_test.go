package diskmon

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"xdcc_server/internal/logging"
)

// =========================================================================
// New
// =========================================================================

func TestNew_Defaults(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	m := New("/data", 1024*1024*1024, nil, logger)

	if m.path != "/data" {
		t.Errorf("path: %q", m.path)
	}
	if m.threshold != 1024*1024*1024 {
		t.Errorf("threshold: %d", m.threshold)
	}
	if m.checkFn == nil {
		t.Error("checkFn should not be nil (auto-assigned)")
	}
	if m.interval != 30*time.Second {
		t.Errorf("interval: %v", m.interval)
	}
}

func TestNew_WithCustomCheckFn(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	customCheck := func(path string) (int64, int64, error) { return 100, 500, nil }
	m := New("/tmp", 200, customCheck, logger)

	if m.checkFn == nil {
		t.Fatal("checkFn should be set")
	}
	avail, total, err := m.checkFn("/tmp")
	if err != nil {
		t.Fatalf("custom checkFn failed: %v", err)
	}
	if avail != 100 || total != 500 {
		t.Errorf("unexpected values: avail=%d total=%d", avail, total)
	}
}

// =========================================================================
// Check
// =========================================================================

func TestCheck_LowSpace(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	// checkFn returns available < threshold → low=true
	checkFn := func(path string) (int64, int64, error) {
		return 50, 1000, nil // 50 < 100 threshold
	}
	m := New("/data", 100, checkFn, logger)

	avail, total, low, err := m.Check()
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if avail != 50 {
		t.Errorf("available: %d", avail)
	}
	if total != 1000 {
		t.Errorf("total: %d", total)
	}
	if !low {
		t.Error("expected low=true when available < threshold")
	}
	if !m.IsLowSpace() {
		t.Error("IsLowSpace should return true after Check")
	}
	if m.Available() != 50 {
		t.Errorf("Available(): %d", m.Available())
	}
	if m.lastChecked.IsZero() {
		t.Error("lastChecked should be updated")
	}
}

func TestCheck_NotLowSpace(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	checkFn := func(path string) (int64, int64, error) {
		return 500, 1000, nil // 500 > 200 threshold
	}
	m := New("/data", 200, checkFn, logger)

	_, _, low, err := m.Check()
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if low {
		t.Error("expected low=false when available > threshold")
	}
	if m.IsLowSpace() {
		t.Error("IsLowSpace should return false")
	}
}

func TestCheck_ExactlyThreshold(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	checkFn := func(path string) (int64, int64, error) {
		return 100, 1000, nil
	}
	m := New("/data", 100, checkFn, logger)

	_, _, low, err := m.Check()
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	// available (100) is NOT strictly less than threshold (100)
	if low {
		t.Error("expected low=false when available == threshold")
	}
}

func TestCheck_Error(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	checkFn := func(path string) (int64, int64, error) {
		return 0, 0, errors.New("disk not found")
	}
	m := New("/bad", 100, checkFn, logger)

	_, _, _, err := m.Check()
	if err == nil {
		t.Error("expected error from Check")
	}
	// IsLowSpace should retain previous value (false by default)
	if m.IsLowSpace() {
		t.Error("IsLowSpace should be false after failed check")
	}
}

// =========================================================================
// Threshold / SetThreshold
// =========================================================================

func TestThreshold(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	m := New("/data", 2048, nil, logger)
	if m.Threshold() != 2048 {
		t.Errorf("Threshold: %d", m.Threshold())
	}
}

func TestSetThreshold(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	m := New("/data", 100, nil, logger)
	m.SetThreshold(500)
	if m.Threshold() != 500 {
		t.Errorf("Threshold after SetThreshold: %d", m.Threshold())
	}
}

// =========================================================================
// FormatBytes
// =========================================================================

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		b    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{5 * 1024 * 1024, "5.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{100 * 1024 * 1024 * 1024, "100.0 GB"}, // large value
	}
	for _, tt := range tests {
		got := FormatBytes(tt.b)
		if got != tt.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tt.b, got, tt.want)
		}
	}
}

// =========================================================================
// StartPeriodicCheck
// =========================================================================

func TestStartPeriodicCheck_InitialCallback(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	checkFn := func(path string) (int64, int64, error) {
		return 50, 1000, nil
	}
	m := New("/data", 100, checkFn, logger)

	changeCalled := false
	stop, done := m.StartPeriodicCheck(func(low bool, available int64) {
		changeCalled = true
		if !low {
			t.Error("expected low=true")
		}
		if available != 50 {
			t.Errorf("available: %d", available)
		}
	})

	stop()
	<-done

	if !changeCalled {
		t.Error("initial onChange should have been called")
	}
}

func TestStartPeriodicCheck_StateTransition(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	var callCount int64
	checkFn := func(path string) (int64, int64, error) {
		c := atomic.AddInt64(&callCount, 1)
		if c == 1 {
			return 500, 1000, nil // not low
		}
		return 50, 1000, nil // low (transition)
	}

	m := New("/data", 100, checkFn, logger)
	m.interval = 10 * time.Millisecond // speed up for test

	transitions := 0
	stop, done := m.StartPeriodicCheck(func(low bool, available int64) {
		transitions++
	})

	// Wait for at least one tick
	time.Sleep(50 * time.Millisecond)

	stop()
	<-done

	// Should have at least 2 callbacks: initial + transition
	if transitions < 2 {
		t.Errorf("expected at least 2 callbacks, got %d", transitions)
	}
}

func TestStartPeriodicCheck_NoCallback(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	checkFn := func(path string) (int64, int64, error) {
		return 500, 1000, nil
	}
	m := New("/data", 100, checkFn, logger)

	stop, done := m.StartPeriodicCheck(nil) // nil callback

	time.Sleep(20 * time.Millisecond)
	stop()
	<-done
	// Should not panic with nil callback
}

func TestStartPeriodicCheck_CheckError(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	checkFn := func(path string) (int64, int64, error) {
		return 0, 0, errors.New("transient error")
	}
	m := New("/data", 100, checkFn, logger)
	m.interval = 10 * time.Millisecond

	changeCalled := false
	stop, done := m.StartPeriodicCheck(func(low bool, available int64) {
		changeCalled = true
	})

	time.Sleep(50 * time.Millisecond)
	stop()
	<-done

	// Initial Check fails, so onChange is not called initially.
	// Subsequent tick fails too, so onChange should never be called.
	if changeCalled {
		t.Error("onChange should not be called when all checks fail")
	}
}

func TestStartPeriodicCheck_NoDoubleCallbackForSameState(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	// Return the same low state every time
	checkFn := func(path string) (int64, int64, error) {
		return 50, 1000, nil // always low
	}
	m := New("/data", 100, checkFn, logger)
	m.interval = 10 * time.Millisecond

	callbackCount := 0
	stop, done := m.StartPeriodicCheck(func(low bool, available int64) {
		callbackCount++
	})

	time.Sleep(50 * time.Millisecond)
	stop()
	<-done

	// Initial callback counts as 1. The periodic tick should NOT call
	// onChange because state (low=true) did not change.
	if callbackCount != 1 {
		t.Errorf("expected exactly 1 callback (initial only), got %d", callbackCount)
	}
}

// =========================================================================
// Stop multiple calls
// =========================================================================

func TestStopMultipleCalls(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	mon := New(".", 1024*1024*10, nil, logger)

	callCount := 0
	stop, done := mon.StartPeriodicCheck(func(low bool, available int64) {
		callCount++
	})

	// First stop - should work
	stop()
	<-done

	// Second stop - should NOT panic (this was the bug)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("stop() called twice caused panic: %v", r)
		}
	}()
	stop()

	// Third stop - still should not panic
	stop()
}

// TestStopWithoutWait ensures stop can be called without waiting on done channel.
func TestStopWithoutWait(t *testing.T) {
	logger := logging.New(logging.LevelDebug, "", 0)
	mon := New(".", 1024*1024*10, nil, logger)

	stop, done := mon.StartPeriodicCheck(nil)

	time.Sleep(50 * time.Millisecond) // Let it run a bit

	// Stop multiple times rapidly
	stop()
	stop()
	stop()

	// Now wait for cleanup
	<-done
}
