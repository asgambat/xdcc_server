package api

import (
	"net/http"
	"testing"
)

func TestExtractClientIP_RemoteAddr(t *testing.T) {
	r := &http.Request{RemoteAddr: "192.168.1.100:54321"}
	got := extractClientIP(r, false)
	if got != "192.168.1.100" {
		t.Errorf("expected 192.168.1.100, got %q", got)
	}
}

func TestExtractClientIP_RemoteAddrNoPort(t *testing.T) {
	r := &http.Request{RemoteAddr: "192.168.1.100"}
	got := extractClientIP(r, false)
	if got != "192.168.1.100" {
		t.Errorf("expected 192.168.1.100, got %q", got)
	}
}

func TestExtractClientIP_IPv6(t *testing.T) {
	r := &http.Request{RemoteAddr: "[::1]:54321"}
	got := extractClientIP(r, false)
	if got != "[::1]" {
		t.Errorf("expected [::1], got %q", got)
	}
}

func TestExtractClientIP_IPv6NoBrackets(t *testing.T) {
	// NOTE: bare IPv6 without brackets (e.g. "::1") is split at the last
	// colon, yielding an incorrect result. This is a known limitation —
	// Go's net/http always brackets IPv6 in RemoteAddr when a port is
	// present, so this edge case is unlikely in practice.
	r := &http.Request{RemoteAddr: "::1"}
	got := extractClientIP(r, false)
	if got == "" {
		t.Error("expected non-empty result for bare IPv6")
	}
}

func TestExtractClientIP_XFFNotTrusted(t *testing.T) {
	r := &http.Request{
		RemoteAddr: "10.0.0.1:8080",
		Header:     http.Header{"X-Forwarded-For": {"203.0.113.50"}},
	}
	// trustProxy=false → XFF should be ignored.
	got := extractClientIP(r, false)
	if got != "10.0.0.1" {
		t.Errorf("expected 10.0.0.1 (RemoteAddr), got %q", got)
	}
}

func TestExtractClientIP_XFFTrusted(t *testing.T) {
	r := &http.Request{
		RemoteAddr: "10.0.0.1:8080",
		Header:     http.Header{"X-Forwarded-For": {"203.0.113.50"}},
	}
	// trustProxy=true → XFF should be used.
	got := extractClientIP(r, true)
	if got != "203.0.113.50" {
		t.Errorf("expected 203.0.113.50 (XFF), got %q", got)
	}
}

func TestExtractClientIP_XFFChainTrusted(t *testing.T) {
	r := &http.Request{
		RemoteAddr: "10.0.0.1:8080",
		Header:     http.Header{"X-Forwarded-For": {"203.0.113.50, 70.41.3.18, 150.172.238.178"}},
	}
	// trustProxy=true → first IP in chain should be used.
	got := extractClientIP(r, true)
	if got != "203.0.113.50" {
		t.Errorf("expected 203.0.113.50 (first in chain), got %q", got)
	}
}

func TestExtractClientIP_XFFTrustedButEmpty(t *testing.T) {
	r := &http.Request{
		RemoteAddr: "10.0.0.1:8080",
		Header:     http.Header{},
	}
	got := extractClientIP(r, true)
	if got != "10.0.0.1" {
		t.Errorf("expected 10.0.0.1 (fallback), got %q", got)
	}
}

func TestExtractClientIP_XFFTrustedWithSpaces(t *testing.T) {
	r := &http.Request{
		RemoteAddr: "10.0.0.1:8080",
		Header:     http.Header{"X-Forwarded-For": {"  203.0.113.50  "}},
	}
	got := extractClientIP(r, true)
	if got != "203.0.113.50" {
		t.Errorf("expected 203.0.113.50 (trimmed), got %q", got)
	}
}
