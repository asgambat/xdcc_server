package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lrstanley/girc"
	"xdcc-go/internal/ircmanager"
	"xdcc-go/internal/store"
)

// ===========================================================================
// Mock IRCManager for testing
// ===========================================================================

type mockIRCManager struct {
	servers []store.ServerRecord
	client  *girc.Client
}

func (m *mockIRCManager) GetServers() []store.ServerRecord                  { return m.servers }
func (m *mockIRCManager) GetClient(serverID int64) *girc.Client             { return m.client }
func (m *mockIRCManager) ConnectServerByID(id int64) error                  { return nil }
func (m *mockIRCManager) DisconnectServer(id int64) error                   { return nil }
func (m *mockIRCManager) JoinChannel(serverID int64, channel string) error  { return nil }
func (m *mockIRCManager) LeaveChannel(serverID int64, channel string) error { return nil }
func (m *mockIRCManager) GetChannels(serverID int64) []store.ChannelRecord  { return nil }
func (m *mockIRCManager) GetChannelTopic(serverID int64, channel string) (string, error) {
	return "", nil
}
func (m *mockIRCManager) Subscribe() chan ircmanager.Event     { return nil }
func (m *mockIRCManager) Unsubscribe(ch chan ircmanager.Event) {}

// ===========================================================================
// Race test: whoisBotOnServer — sync.Once prevents data race when RPL_WHOISUSER
// and ERR_NOSUCHNICK are fired concurrently from separate goroutines (as girc does).
// ===========================================================================

func TestWhoisBotOnServerRace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping race test in short mode")
	}

	// Create a girc.Client (no connection needed — just to register handlers)
	client := girc.New(girc.Config{
		Server: "test.irc.net",
		Port:   6667,
		Nick:   "testbot",
		User:   "testuser",
		Name:   "test",
	})

	mgr := &mockIRCManager{
		client: client,
		servers: []store.ServerRecord{
			{ID: 1, Address: "test.irc.net", Port: 6667, Status: "connected"},
		},
	}

	bot := "TestBot"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Run whoisBotOnServer in a goroutine
	resultCh := make(chan bool, 1)
	go func() {
		resultCh <- whoisBotOnServer(ctx, mgr, 1, bot)
	}()

	// Give the goroutine time to register handlers
	time.Sleep(50 * time.Millisecond)

	// Simulate concurrent girc callback firing:
	// RPL_WHOISUSER and ERR_NOSUCHNICK can fire concurrently (girc dispatches
	// events in separate goroutines). The sync.Once in whoisBotOnServer must
	// ensure only one result is sent.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		// Simulate RPL_WHOISUSER (311) — bot found
		client.RunHandlers(&girc.Event{
			Command: "311",
			Params:  []string{bot, "user", "host", "Test User"},
		})
	}()

	go func() {
		defer wg.Done()
		// Simulate ERR_NOSUCHNICK (401) — bot not found
		client.RunHandlers(&girc.Event{
			Command: "401",
			Params:  []string{bot, "No such nick"},
		})
	}()

	wg.Wait()

	// Check that we got a result (either true or false, but shouldn't panic or race)
	select {
	case result := <-resultCh:
		t.Logf("whoisBotOnServer returned: %v", result)
	case <-ctx.Done():
		t.Fatal("whoisBotOnServer timed out — possible deadlock")
	}
}

// ===========================================================================
// Race test: multiple concurrent callbacks for the same bot name
// ===========================================================================

func TestWhoisBotOnServerConcurrentCallbacks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping race test in short mode")
	}

	client := girc.New(girc.Config{
		Server: "test.irc.net",
		Port:   6667,
		Nick:   "testbot",
		User:   "testuser",
		Name:   "test",
	})

	mgr := &mockIRCManager{client: client}
	bot := "TestBot"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := make(chan bool, 1)
	go func() {
		resultCh <- whoisBotOnServer(ctx, mgr, 1, bot)
	}()

	time.Sleep(50 * time.Millisecond)

	// Fire 10 concurrent events — only the first should win via sync.Once
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client.RunHandlers(&girc.Event{
				Command: "311",
				Params:  []string{bot, "user", "host", "Test User"},
			})
		}()
	}

	wg.Wait()

	select {
	case result := <-resultCh:
		t.Logf("whoisBotOnServer returned: %v (expected true)", result)
		if !result {
			t.Error("expected true from RPL_WHOISUSER, got false")
		}
	case <-ctx.Done():
		t.Fatal("whoisBotOnServer timed out")
	}
}

// ===========================================================================
// Early cancellation test: findBotOnConnectedServers with cancelled context
// ===========================================================================

func TestFindBotOnConnectedServers_EarlyCancel(t *testing.T) {
	// Create a cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	client := girc.New(girc.Config{
		Server: "test.irc.net",
		Port:   6667,
		Nick:   "testbot",
		User:   "testuser",
		Name:   "test",
	})

	mgr := &mockIRCManager{
		client: client,
		servers: []store.ServerRecord{
			{ID: 1, Address: "test.irc.net", Port: 6667, Status: "connected"},
			{ID: 2, Address: "irc.other.net", Port: 6667, Status: "connected"},
			{ID: 3, Address: "irc.another.net", Port: 6667, Status: "connected"},
		},
	}

	// Should return immediately without blocking
	start := time.Now()
	result := findBotOnConnectedServers(ctx, mgr, "TestBot")
	elapsed := time.Since(start)

	if result != "" {
		t.Errorf("expected empty result for cancelled context, got %q", result)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("findBotOnConnectedServers took %v with cancelled context, expected < 500ms", elapsed)
	}
}

// ===========================================================================
// Normal flow test: findBotOnConnectedServers with no connected servers
// ===========================================================================

func TestFindBotOnConnectedServers_NoServers(t *testing.T) {
	ctx := context.Background()
	mgr := &mockIRCManager{servers: nil} // no servers

	result := findBotOnConnectedServers(ctx, mgr, "TestBot")
	if result != "" {
		t.Errorf("expected empty result with no servers, got %q", result)
	}
}

// ===========================================================================
// Test that whoisBotOnServer returns false when IRCManager returns nil client
// ===========================================================================

func TestWhoisBotOnServer_NilClient(t *testing.T) {
	mgr := &mockIRCManager{client: nil}
	ctx := context.Background()

	result := whoisBotOnServer(ctx, mgr, 1, "TestBot")
	if result {
		t.Error("expected false when client is nil")
	}
}
