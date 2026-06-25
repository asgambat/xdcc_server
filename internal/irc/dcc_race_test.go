package irc

import (
	"context"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xdcc_server/internal/entities"
)

// testLogger implements Logger and discards all output during tests.
type testLogger struct{}

func (testLogger) Printf(string, ...interface{}) {}

// raceTestHarness holds all the resources needed for a single race iteration.
type raceTestHarness struct {
	client     *Client
	ps         *packState
	clientConn net.Conn
	serverConn net.Conn
	tmpFile    *os.File
	tmpName    string
}

// setupRaceTest creates a Client with a packState, a pipe connection, and a
// temp file, ready for the race test. Caller must call h.cleanup() when done.
func setupRaceTest() *raceTestHarness {
	h := &raceTestHarness{}

	// ── Client ─────────────────────────────────────────────────
	h.client = &Client{
		ctx:       context.Background(),
		verbosity: -1,
		logger:    testLogger{},
		packs: []*entities.XDCCPack{{
			Bot:        "TestBot",
			PackNumber: 42,
			Server:     entities.IrcServer{Address: "test", Port: 6667},
		}},
		conn: &Connection{}, // required by resetForPack() which accesses c.conn.timerMu
	}
	h.client.packIdxVal.Store(0)

	// ── packState ──────────────────────────────────────────────
	h.ps = &packState{
		downloadDone:        make(chan struct{}),
		downloadDoneOnce:    &sync.Once{},
		downloadStarted:     make(chan struct{}),
		downloadStartedOnce: &sync.Once{},
		ackQueue:            make(chan []byte, ackQueueBufSize),
	}
	h.client.ps = h.ps

	// ── Pipe (DCC connection) ──────────────────────────────────
	h.clientConn, h.serverConn = net.Pipe()

	// ── Temp file ──────────────────────────────────────────────
	var err error
	h.tmpFile, err = os.CreateTemp("", "dcc-race-test-*")
	if err != nil {
		h.clientConn.Close()
		h.serverConn.Close()
		panic("CreateTemp: " + err.Error())
	}
	h.tmpName = h.tmpFile.Name()

	// ── Initialize packState ───────────────────────────────────
	h.ps.mu.Lock()
	h.ps.dccConn = h.clientConn
	h.ps.dccFile = h.tmpFile
	h.ps.filesize = 10 * 1024 * 1024 // 10 MB — won't be reached
	h.ps.downStartTime = time.Now()
	h.ps.downloading = true
	h.ps.mu.Unlock()

	close(h.ps.downloadStarted)

	return h
}

// cleanup releases all resources held by the harness.
func (h *raceTestHarness) cleanup() {
	h.client.ps.downloadDoneOnce.Do(func() { close(h.client.ps.downloadDone) })
	h.tmpFile.Close()
	os.Remove(h.tmpName)
	h.clientConn.Close()
	h.serverConn.Close()
}

// startSpammer launches a goroutine that calls resetForPack() in a tight loop.
// It returns a done channel (close to stop) and a ranOnce channel that is
// closed after the first resetForPack() call completes, so the caller can
// deterministically wait for at least one pointer swap.
func startSpammer(h *raceTestHarness) (spamDone chan struct{}, ranOnce <-chan struct{}) {
	spamDone = make(chan struct{})
	ranOnceCh := make(chan struct{})
	ranOnce = ranOnceCh

	go func() {
		first := true
		for {
			select {
			case <-spamDone:
				return
			default:
				h.client.resetForPack()
				if first {
					close(ranOnceCh)
					first = false
				}
				runtime.Gosched()
			}
		}
	}()

	return spamDone, ranOnce
}

// TestReceiveDataNoPanicOnResetForPack verifies that receiveData does not panic
// with "sync: unlock of unlocked mutex" when resetForPack() replaces c.ps
// concurrently while receiveData's deferred cleanup is running.
//
// This reproduces the stall → retry race that caused a fatal crash in
// production. The test uses a "spammer" goroutine that calls resetForPack() in
// a tight loop while receiveData's defer executes, maximizing the probability
// of the pointer swap interleaving between the defer's Lock and Unlock.
//
// Without the fix (ps := c.ps capture at receiveData entry), the spammer
// triggers the race reliably: the defer evaluates c.ps for Lock and again for
// Unlock, and if the spammer replaced c.ps between them, Unlock panics on a
// different mutex. With the fix, receiveData uses the captured ps throughout,
// making the spammer harmless.
func TestReceiveDataNoPanicOnResetForPack(t *testing.T) {
	const iterations = 500

	for i := 0; i < iterations; i++ {
		h := setupRaceTest()
		func() {
			defer h.cleanup()

			// Launch goroutines (same as startDownload).
			go h.client.ackSender()
			go h.client.progressPrinter()

			var receivePanic any
			var receiveWg sync.WaitGroup
			receiveWg.Add(1)
			go func() {
				defer receiveWg.Done()
				defer func() {
					if r := recover(); r != nil {
						receivePanic = r
					}
				}()
				h.client.receiveData()
			}()

			// Send a few bytes to establish transfer activity.
			h.serverConn.Write([]byte("hello world"))

			// Start the spammer and wait for at least one resetForPack
			// before closing the connection. This guarantees c.ps has
			// been replaced and the old downloadDone is closed.
			spamDone, ranOnce := startSpammer(h)
			<-ranOnce

			// Close the server side to trigger receiveData's Read() to fail.
			h.serverConn.Close()

			// Wait for receiveData to finish, then stop the spammer.
			receiveWg.Wait()
			close(spamDone)

			if receivePanic != nil {
				t.Fatalf("iter %d: receiveData panicked: %v", i, receivePanic)
			}

			// Old packState's downloadDone should be closed by the
			// first spammer resetForPack call.
			select {
			case <-h.ps.downloadDone:
				// Expected.
			default:
				t.Errorf("iter %d: old packState.downloadDone not closed", i)
			}

			// c.ps should now point to a different packState.
			if h.client.ps == h.ps {
				t.Errorf("iter %d: c.ps not replaced by spammer", i)
			}
		}()
	}
}

// TestReceiveDataRaceStress runs the same race test with multiple parallel
// goroutines to increase scheduling pressure. This is a regression test for
// the stall → retry race causing "sync: unlock of unlocked mutex".
func TestReceiveDataRaceStress(t *testing.T) {
	const goroutines = 10
	const itersPerGoroutine = 50

	var wg sync.WaitGroup
	var panics atomic.Int32

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < itersPerGoroutine; i++ {
				func() {
					defer func() {
						if r := recover(); r != nil {
							panics.Add(1)
						}
					}()
					runRaceIterationSilent()
				}()
			}
		}()
	}

	wg.Wait()

	if c := panics.Load(); c > 0 {
		t.Errorf("race stress test: %d panics across %d iterations",
			c, goroutines*itersPerGoroutine)
	}
}

// runRaceIterationSilent runs a single race iteration without using t (safe
// from non-test goroutines in stress tests). It panics if the race causes a
// fatal error, which is caught by the caller's recover().
func runRaceIterationSilent() {
	h := setupRaceTest()
	defer h.cleanup()

	go h.client.ackSender()
	go h.client.progressPrinter()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.client.receiveData()
	}()

	h.serverConn.Write([]byte("hello world"))

	spamDone, ranOnce := startSpammer(h)
	<-ranOnce
	h.serverConn.Close()

	wg.Wait()
	close(spamDone)
}
