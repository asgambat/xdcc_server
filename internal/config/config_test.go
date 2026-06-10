package config

import (
	"testing"
)

// TestPickGreeting_EmptyListReturnsDefault verifies the spec: when the
// configured greetings list is empty, "hello everybody" is returned.
func TestPickGreeting_EmptyListReturnsDefault(t *testing.T) {
	c := &Config{}
	got := c.PickGreeting()
	if got != DefaultGreeting {
		t.Errorf("expected default greeting %q, got %q", DefaultGreeting, got)
	}
	if DefaultGreeting != "hello everybody" {
		t.Errorf("DefaultGreeting constant changed: got %q, want %q", DefaultGreeting, "hello everybody")
	}
}

// TestPickGreeting_NilListReturnsDefault verifies the spec for a nil slice too.
func TestPickGreeting_NilListReturnsDefault(t *testing.T) {
	c := &Config{IRC: IRCConfig{Greetings: nil}}
	got := c.PickGreeting()
	if got != DefaultGreeting {
		t.Errorf("expected default greeting %q, got %q", DefaultGreeting, got)
	}
}

// TestPickGreeting_SingleEntryAlwaysReturnsIt verifies that with a single
// configured greeting, that one is always returned.
func TestPickGreeting_SingleEntryAlwaysReturnsIt(t *testing.T) {
	c := &Config{IRC: IRCConfig{Greetings: []string{"ciao a tutti"}}}
	for i := 0; i < 10; i++ {
		if got := c.PickGreeting(); got != "ciao a tutti" {
			t.Errorf("expected configured greeting, got %q", got)
		}
	}
}

// TestPickGreeting_PicksFromConfiguredList verifies that with multiple
// configured greetings, all of them can be picked (statistical check).
func TestPickGreeting_PicksFromConfiguredList(t *testing.T) {
	list := []string{"hi", "hello", "ciao", "salve", "buongiorno"}
	// Build a set for O(1) membership checks.
	listSet := make(map[string]bool, len(list))
	for _, s := range list {
		listSet[s] = true
	}
	c := &Config{IRC: IRCConfig{Greetings: list}}

	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		got := c.PickGreeting()
		// Every returned greeting must be in the configured list.
		if !listSet[got] {
			t.Fatalf("PickGreeting returned %q, which is not in the configured list", got)
		}
		seen[got] = true
	}

	// With 200 draws and 5 entries we should see at least 2 distinct
	// greetings (probability of seeing only one is essentially zero).
	if len(seen) < 2 {
		t.Errorf("expected to see multiple greetings over 200 draws, saw %d distinct: %v", len(seen), seen)
	}
}

// TestPickGreeting_IgnoresBlankEntries ensures a list of one empty string
// still falls back to the default greeting rather than sending a blank
// message. (Empty entries in the user-provided list are not filtered here
// because the spec is "if the list doesn't exist use the default"; we
// interpret that strictly: a non-empty list with only blanks still uses
// the configured list verbatim. So this test only ensures the empty list
// itself is the trigger.)
func TestPickGreeting_EmptyListExplicitReturnsDefault(t *testing.T) {
	c := &Config{IRC: IRCConfig{Greetings: []string{}}}
	if got := c.PickGreeting(); got != DefaultGreeting {
		t.Errorf("expected default greeting, got %q", got)
	}
}

// TestApplyEnvOverrides_Greetings verifies that XDCC_IRC_GREETINGS, when
// set, is parsed as a comma-separated list of greeting phrases.
func TestApplyEnvOverrides_Greetings(t *testing.T) {
	t.Setenv("XDCC_IRC_GREETINGS", "ciao,  hello ,buongiorno")
	c := DefaultConfig()
	c.applyEnvOverrides()

	if len(c.IRC.Greetings) != 3 {
		t.Fatalf("expected 3 greetings, got %d (%v)", len(c.IRC.Greetings), c.IRC.Greetings)
	}
	want := []string{"ciao", "hello", "buongiorno"}
	for i, w := range want {
		if c.IRC.Greetings[i] != w {
			t.Errorf("greeting[%d]: expected %q, got %q", i, w, c.IRC.Greetings[i])
		}
	}
}

// TestApplyEnvOverrides_GreetingsUnsetKeepsEmpty verifies that when the
// environment variable is not set the configured list is preserved as-is.
func TestApplyEnvOverrides_GreetingsUnsetKeepsEmpty(t *testing.T) {
	// Make sure the variable is not set.
	t.Setenv("XDCC_IRC_GREETINGS", "")

	c := DefaultConfig()
	c.applyEnvOverrides()

	if len(c.IRC.Greetings) != 0 {
		t.Errorf("expected greetings to remain empty, got %v", c.IRC.Greetings)
	}
}

// TestDefaultConfig_HasGreetingsField ensures the DefaultConfig initializes
// the Greetings field as an empty slice (so YAML serialization is stable and
// "no greetings configured" is the documented default).
func TestDefaultConfig_HasGreetingsField(t *testing.T) {
	c := DefaultConfig()
	if c.IRC.Greetings == nil {
		t.Error("DefaultConfig().IRC.Greetings is nil, expected empty slice")
	}
	if len(c.IRC.Greetings) != 0 {
		t.Errorf("expected empty greetings, got %v", c.IRC.Greetings)
	}
}

// TestApplyEnvOverrides_TrustProxy verifies that XDCC_HTTP_TRUST_PROXY
// sets the TrustProxy field correctly for both "true" and "false" values.
func TestApplyEnvOverrides_TrustProxy(t *testing.T) {
	// "true" → true
	t.Setenv("XDCC_HTTP_TRUST_PROXY", "true")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if !c.HTTP.TrustProxy {
		t.Error("expected TrustProxy=true when env is 'true'")
	}

	// "1" → true
	t.Setenv("XDCC_HTTP_TRUST_PROXY", "1")
	c = DefaultConfig()
	c.applyEnvOverrides()
	if !c.HTTP.TrustProxy {
		t.Error("expected TrustProxy=true when env is '1'")
	}

	// "false" → false
	t.Setenv("XDCC_HTTP_TRUST_PROXY", "false")
	c = DefaultConfig()
	c.applyEnvOverrides()
	if c.HTTP.TrustProxy {
		t.Error("expected TrustProxy=false when env is 'false'")
	}

	// unset → false (default)
	t.Setenv("XDCC_HTTP_TRUST_PROXY", "")
	c = DefaultConfig()
	c.applyEnvOverrides()
	if c.HTTP.TrustProxy {
		t.Error("expected TrustProxy=false when env is unset")
	}
}

// TestDefaultConfig_TrustProxy verifies the default is false.
func TestDefaultConfig_TrustProxy(t *testing.T) {
	c := DefaultConfig()
	if c.HTTP.TrustProxy {
		t.Error("DefaultConfig().HTTP.TrustProxy should be false")
	}
}

// TestApplyEnvOverrides_EnableMessageSend verifies the env override for enable_message_send.
func TestApplyEnvOverrides_EnableMessageSend(t *testing.T) {
	t.Setenv("XDCC_IRC_ENABLE_MESSAGE_SEND", "true")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if !c.IRC.EnableMessageSend {
		t.Error("expected EnableMessageSend=true when env is 'true'")
	}

	t.Setenv("XDCC_IRC_ENABLE_MESSAGE_SEND", "0")
	c = DefaultConfig()
	c.applyEnvOverrides()
	if c.IRC.EnableMessageSend {
		t.Error("expected EnableMessageSend=false when env is '0'")
	}
}

// TestApplyEnvOverrides_LogPrivateMessages verifies the env override.
func TestApplyEnvOverrides_LogPrivateMessages(t *testing.T) {
	t.Setenv("XDCC_IRC_LOG_PRIVATE_MESSAGES", "1")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if !c.IRC.LogPrivateMessages {
		t.Error("expected LogPrivateMessages=true when env is '1'")
	}
}

// TestIsChannelLogged verifies the IsChannelLogged helper.
func TestIsChannelLogged(t *testing.T) {
	c := &Config{IRC: IRCConfig{ChannelLog: []string{"#general", "&local"}}}

	tests := []struct {
		channel string
		want    bool
	}{
		{"#general", true},
		{"#GENERAL", true}, // case-insensitive
		{"general", true},  // no '#' prefix → normalized
		{"&local", true},
		{"#other", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := c.IsChannelLogged(tt.channel); got != tt.want {
			t.Errorf("IsChannelLogged(%q) = %v, want %v", tt.channel, got, tt.want)
		}
	}
}

// TestIsChannelLogged_EmptyList verifies empty list returns false for everything.
func TestIsChannelLogged_EmptyList(t *testing.T) {
	c := &Config{}
	if c.IsChannelLogged("#general") {
		t.Error("expected false when ChannelLog is nil")
	}
}

// TestPickGreeting_ConcurrentSafe sanity-checks that concurrent calls to
// PickGreeting never panic and never return a string outside the configured
// list. This is important because the IRC manager spawns one goroutine per
// channel JOIN, all of which call PickGreeting.
func TestPickGreeting_ConcurrentSafe(t *testing.T) {
	list := []string{"hi", "hello", "ciao"}
	listSet := make(map[string]bool, len(list))
	for _, s := range list {
		listSet[s] = true
	}
	c := &Config{IRC: IRCConfig{Greetings: list}}

	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				got := c.PickGreeting()
				if !listSet[got] {
					t.Errorf("got unexpected greeting %q", got)
					return
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		<-done
	}
}
