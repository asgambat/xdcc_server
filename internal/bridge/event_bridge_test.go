package bridge

import (
	"context"
	"sync"
	"testing"

	"xdcc_server/internal/ircmanager"
	"xdcc_server/internal/logging"
	"xdcc_server/internal/queue"
	"xdcc_server/internal/sse"
)

// ---------------------------------------------------------------------------
// New / Hub
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	h := sse.NewHub(10)
	logger := logging.New(logging.LevelDebug, "", 0)
	b := New(h, logger)
	if b == nil {
		t.Fatal("expected non-nil bridge")
	}
	if b.sseHub != h {
		t.Error("expected hub to be set")
	}
}

func TestHub(t *testing.T) {
	h := sse.NewHub(10)
	logger := logging.New(logging.LevelDebug, "", 0)
	b := New(h, logger)
	if b.Hub() != h {
		t.Error("Hub() should return the underlying hub")
	}
}

// ---------------------------------------------------------------------------
// ForwardIRCEvents
// ---------------------------------------------------------------------------

func TestForwardIRCEvents(t *testing.T) {
	h := sse.NewHub(10)
	logger := logging.New(logging.LevelDebug, "", 0)
	b := New(h, logger)

	ch := make(chan ircmanager.Event, 3)
	ch <- ircmanager.Event{Type: ircmanager.EventServerConnected, ServerID: 1, ServerAddr: "irc.test.com"}
	ch <- ircmanager.Event{Type: ircmanager.EventChannelJoined, ServerID: 1, Channel: "#xdcc"}
	close(ch)

	var wg sync.WaitGroup
	wg.Add(1)
	b.ForwardIRCEvents(context.Background(), ch, &wg)
	wg.Wait()
}

func TestForwardIRCEventsCancellation(t *testing.T) {
	h := sse.NewHub(10)
	logger := logging.New(logging.LevelDebug, "", 0)
	b := New(h, logger)

	ch := make(chan ircmanager.Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	b.ForwardIRCEvents(ctx, ch, &wg)
	wg.Wait()
}

func TestForwardIRCEventsDrain(t *testing.T) {
	h := sse.NewHub(10)
	ch := h.Subscribe()
	go drainSSE2(ch)

	logger := logging.New(logging.LevelDebug, "", 0)
	b := New(h, logger)

	evtCh := make(chan ircmanager.Event, 5)
	evtCh <- ircmanager.Event{Type: ircmanager.EventServerConnected, ServerID: 1}
	evtCh <- ircmanager.Event{Type: ircmanager.EventChannelJoined, ServerID: 1, Channel: "#test"}
	close(evtCh)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	b.ForwardIRCEvents(ctx, evtCh, &wg)
	wg.Wait()
}

// ---------------------------------------------------------------------------
// ForwardQueueEvents
// ---------------------------------------------------------------------------

func TestForwardQueueEvents(t *testing.T) {
	h := sse.NewHub(10)
	logger := logging.New(logging.LevelDebug, "", 0)
	b := New(h, logger)

	ch := make(chan queue.Event, 2)
	ch <- queue.Event{Type: queue.EventDownloadCompleted, DownloadID: 1, Filename: "test.mkv"}
	ch <- queue.Event{Type: queue.EventDownloadFailed, DownloadID: 2, Filename: "fail.mkv", ErrorMessage: "timeout"}
	close(ch)

	var wg sync.WaitGroup
	wg.Add(1)
	b.ForwardQueueEvents(context.Background(), ch, &wg)
	wg.Wait()
}

func TestForwardQueueEventsCancellation(t *testing.T) {
	h := sse.NewHub(10)
	logger := logging.New(logging.LevelDebug, "", 0)
	b := New(h, logger)

	ch := make(chan queue.Event, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	b.ForwardQueueEvents(ctx, ch, &wg)
	wg.Wait()
}

func TestForwardQueueEventsDrain(t *testing.T) {
	h := sse.NewHub(10)
	ch := h.Subscribe()
	go drainSSE2(ch)

	logger := logging.New(logging.LevelDebug, "", 0)
	b := New(h, logger)

	evtCh := make(chan queue.Event, 5)
	evtCh <- queue.Event{Type: queue.EventDownloadCompleted, DownloadID: 1, Filename: "test.mkv"}
	evtCh <- queue.Event{Type: queue.EventDownloadQueued, DownloadID: 2, Filename: "queued.mkv"}
	close(evtCh)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	b.ForwardQueueEvents(ctx, evtCh, &wg)
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Drain helpers
// ---------------------------------------------------------------------------

func TestDrainIRCEventsEmpty(t *testing.T) {
	h := sse.NewHub(10)
	ch := make(chan ircmanager.Event)
	close(ch)
	drainIRCEvents(ch, h)
}

func TestDrainQueueEventsEmpty(t *testing.T) {
	h := sse.NewHub(10)
	ch := make(chan queue.Event)
	close(ch)
	drainQueueEvents(ch, h)
}

func TestDrainIRCEventsWithEvents(t *testing.T) {
	h := sse.NewHub(10)
	subCh := h.Subscribe()
	go drainSSE2(subCh)

	ch := make(chan ircmanager.Event, 2)
	ch <- ircmanager.Event{Type: ircmanager.EventServerConnected, ServerID: 1}
	ch <- ircmanager.Event{Type: ircmanager.EventChannelJoined, ServerID: 1, Channel: "#test"}
	close(ch)
	drainIRCEvents(ch, h)
}

func TestDrainQueueEventsWithEvents(t *testing.T) {
	h := sse.NewHub(10)
	subCh := h.Subscribe()
	go drainSSE2(subCh)

	ch := make(chan queue.Event, 2)
	ch <- queue.Event{Type: queue.EventDownloadCompleted, DownloadID: 1}
	ch <- queue.Event{Type: queue.EventDownloadFailed, DownloadID: 2}
	close(ch)
	drainQueueEvents(ch, h)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func drainSSE2(ch chan sse.Event) {
	for range ch {
		continue
	}
}
