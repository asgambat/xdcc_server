package entities

import "testing"

// --- ParseIrcServer ----------------------------------------------------------

func TestParseIrcServer_HostOnly(t *testing.T) {
	s := ParseIrcServer("irc.rizon.net")
	if s.Address != "irc.rizon.net" || s.Port != 6667 {
		t.Errorf("got %v", s)
	}
}

func TestParseIrcServer_HostPort(t *testing.T) {
	s := ParseIrcServer("irc.rizon.net:6697")
	if s.Address != "irc.rizon.net" || s.Port != 6697 {
		t.Errorf("got %v", s)
	}
}

func TestParseIrcServer_InvalidPort(t *testing.T) {
	// Non-numeric port → falls back to default 6667.
	s := ParseIrcServer("irc.rizon.net:abc")
	if s.Port != 6667 {
		t.Errorf("expected port 6667, got %d", s.Port)
	}
}

func TestParseIrcServer_OutOfRangePort(t *testing.T) {
	s := ParseIrcServer("irc.rizon.net:99999")
	if s.Port != 6667 {
		t.Errorf("expected port 6667 for out-of-range, got %d", s.Port)
	}
}

func TestNewIrcServerWithPort(t *testing.T) {
	s := NewIrcServerWithPort("irc.rizon.net", 7000)
	if s.Address != "irc.rizon.net" || s.Port != 7000 {
		t.Errorf("got %v", s)
	}
}

func TestSplitHostPort_BareHost(t *testing.T) {
	host, port, err := splitHostPort("irc.rizon.net")
	if err != nil || host != "irc.rizon.net" || port != "" {
		t.Errorf("got host=%q port=%q err=%v", host, port, err)
	}
}

func TestSplitHostPort_HostPort(t *testing.T) {
	host, port, err := splitHostPort("irc.rizon.net:6667")
	if err != nil || host != "irc.rizon.net" || port != "6667" {
		t.Errorf("got host=%q port=%q err=%v", host, port, err)
	}
}

func TestSplitHostPort_IPv6Bare(t *testing.T) {
	// Bare IPv6 address: multiple colons, no brackets → return as-is, no port.
	host, port, err := splitHostPort("::1")
	if err != nil || host != "::1" || port != "" {
		t.Errorf("got host=%q port=%q err=%v", host, port, err)
	}
}

func TestSplitHostPort_IPv6Bracketed(t *testing.T) {
	host, port, err := splitHostPort("[::1]:6697")
	if err != nil || host != "::1" || port != "6697" {
		t.Errorf("got host=%q port=%q err=%v", host, port, err)
	}
}

func TestNewIrcServer(t *testing.T) {
	s := NewIrcServer("irc.example.com")
	if s.Address != "irc.example.com" {
		t.Errorf("Address = %q, want irc.example.com", s.Address)
	}
	if s.Port != 6667 {
		t.Errorf("Port = %d, want 6667", s.Port)
	}
}

func TestParseIrcServer_NegativePort(t *testing.T) {
	s := ParseIrcServer("host:-1")
	if s.Port != 6667 {
		t.Errorf("Port = %d, want 6667 for negative port", s.Port)
	}
}

func TestParseIrcServer_ZeroPort(t *testing.T) {
	s := ParseIrcServer("host:0")
	if s.Port != 6667 {
		t.Errorf("Port = %d, want 6667 for zero port", s.Port)
	}
}

func TestParseIrcServer_BareIPv6Long(t *testing.T) {
	s := ParseIrcServer("2001:db8::8a2e:370:7334")
	if s.Address != "2001:db8::8a2e:370:7334" {
		t.Errorf("Address = %q, want bare IPv6", s.Address)
	}
	if s.Port != 6667 {
		t.Errorf("Port = %d, want 6667", s.Port)
	}
}
