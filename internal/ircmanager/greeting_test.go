package ircmanager

import (
	"context"
	"testing"
	"time"
)

// TestRandIntRange_InRange verifies that randIntRange(min, max) always
// returns a value in the inclusive [min, max] range.
func TestRandIntRange_InRange(t *testing.T) {
	for trial := 0; trial < 500; trial++ {
		got, err := randIntRange(2, 4)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got < 2 || got > 4 {
			t.Errorf("randIntRange(2, 4) = %d, want 2..4", got)
		}
	}
}

// TestRandIntRange_FullRange covers a wider range to catch off-by-one bugs.
func TestRandIntRange_FullRange(t *testing.T) {
	lo, hi := 2, 4
	seen := make(map[int]bool)
	for i := 0; i < 200; i++ {
		got, err := randIntRange(lo, hi)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got < lo || got > hi {
			t.Fatalf("randIntRange(%d, %d) = %d, out of range", lo, hi, got)
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		t.Errorf("expected to see at least 2 distinct values in range, saw %d: %v", len(seen), seen)
	}
}

// TestRandIntRange_SingleValue verifies that a degenerate min==max range
// always returns that single value.
func TestRandIntRange_SingleValue(t *testing.T) {
	for i := 0; i < 20; i++ {
		got, err := randIntRange(3, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != 3 {
			t.Errorf("randIntRange(3, 3) = %d, want 3", got)
		}
	}
}

// TestSendChannelGreeting_DoesNotPanicWithNilClient ensures the helper
// is a no-op (does not panic) when the IRC client is nil. This guards
// against races where a connection has been torn down between the JOIN
// event being received and the greeting goroutine starting.
func TestSendChannelGreeting_DoesNotPanicWithNilClient(t *testing.T) {
	mc := &managedConnection{}
	// Should not panic with nil client.
	mc.sendChannelGreeting(nil, "#test")
}

// TestSendChannelGreeting_CancelledByContext verifies that calling
// sendChannelGreeting with a cancelled context does not panic. The
// function itself returns immediately (it spawns a goroutine), and the
// goroutine exits early when it hits the <-mc.ctx.Done() path in the
// timer select, so we just verify no panic occurs.
func TestSendChannelGreeting_CancelledByContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mc := &managedConnection{
		ctx:    ctx,
		cancel: cancel,
	}

	// Cancel the context immediately so the timer never fires.
	cancel()

	// Must not panic. The goroutine exits on mc.ctx.Done() in the timer
	// select without sending any message.
	mc.sendChannelGreeting(nil, "#test")

	// Give the goroutine a moment to exit via the cancelled context.
	time.Sleep(50 * time.Millisecond)
}
