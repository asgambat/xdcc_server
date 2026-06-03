package diskmon

import (
	"testing"
	"time"

	"xdcc-go/internal/logging"
)

// TestStopMultipleCalls ensures the stop function can be called multiple times
// without causing a panic (double-close of channel).
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
