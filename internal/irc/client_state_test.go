package irc

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestPackStateSnapshotDecisionFlags(t *testing.T) {
	t.Parallel()

	ps := &packState{}
	ps.setNeedsJoin(true)
	ps.setWhoisFoundChannels(true)

	flags := ps.snapshotDecisionFlags()
	if flags.messageSent {
		t.Fatal("messageSent should be false by default")
	}
	if !flags.needsJoin {
		t.Fatal("needsJoin should be true")
	}
	if !flags.whoisFoundChannels {
		t.Fatal("whoisFoundChannels should be true")
	}
}

func TestPackStateMarkMessageSent_Idempotent(t *testing.T) {
	t.Parallel()

	ps := &packState{}
	if ps.markMessageSent() {
		t.Fatal("first markMessageSent call must return false")
	}
	if !ps.markMessageSent() {
		t.Fatal("second markMessageSent call must return true")
	}
	if !ps.isMessageSent() {
		t.Fatal("messageSent should remain true after marking")
	}
}

func TestPackStateMarkMessageSent_ConcurrentSingleWinner(t *testing.T) {
	t.Parallel()

	ps := &packState{}
	const goroutines = 32

	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !ps.markMessageSent() {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()

	if winners.Load() != 1 {
		t.Fatalf("expected exactly 1 first sender, got %d", winners.Load())
	}
	if !ps.isMessageSent() {
		t.Fatal("messageSent should be true after concurrent marks")
	}
}
