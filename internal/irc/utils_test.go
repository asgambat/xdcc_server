package irc

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// NormalizeChannel
// ---------------------------------------------------------------------------

func TestNormalizeChannel(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"#test", "#test"},
		{"Test", "#test"},
		{"#TEST", "#test"},
		{"  #Chan  ", "#chan"},
		{"", ""},
		{"   ", ""},
		{"#AlreadyLower", "#alreadylower"},
	}
	for _, tt := range tests {
		got := NormalizeChannel(tt.in)
		if got != tt.want {
			t.Errorf("NormalizeChannel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// formatDuration
// ---------------------------------------------------------------------------

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{5 * time.Second, "5s"},
		{59 * time.Second, "59s"},
		{1 * time.Minute, "1m 0s"},
		{90 * time.Second, "1m 30s"},
		{5 * time.Minute, "5m 0s"},
		{5*time.Minute + 30*time.Second, "5m 30s"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// formatSpeed
// ---------------------------------------------------------------------------

func TestFormatSpeed(t *testing.T) {
	tests := []struct {
		bps  float64
		want string
	}{
		{0, "0.0 KB/s"},
		{1024, "1.0 KB/s"},
		{512, "0.5 KB/s"},
		{1024 * 1024, "1.00 MB/s"},
		{2 * 1024 * 1024, "2.00 MB/s"},
		{1024*1024 + 512*1024, "1.50 MB/s"},
	}
	for _, tt := range tests {
		got := formatSpeed(tt.bps)
		if got != tt.want {
			t.Errorf("formatSpeed(%v) = %q, want %q", tt.bps, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// ipNumToQuad
// ---------------------------------------------------------------------------

func TestIpNumToQuad(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"0", "0.0.0.0"},
		{"167772161", "10.0.0.1"},         // 10*2^24 + 0*2^16 + 0*2^8 + 1
		{"4294967295", "255.255.255.255"}, // max uint32
	}
	for _, tt := range tests {
		got := ipNumToQuad(tt.in)
		if got != tt.want {
			t.Errorf("ipNumToQuad(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// splitDCC
// ---------------------------------------------------------------------------

func TestSplitDCC(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"SEND file.txt 123 4567 1024", []string{"SEND", "file.txt", "123", "4567", "1024"}},
		{`SEND "long file name.mkv" 123 4567 1024`, []string{"SEND", "long file name.mkv", "123", "4567", "1024"}},
		{"  SEND  file  123  ", []string{"SEND", "file", "123"}},
		{"", nil},
		{"   ", nil},
		{`"unclosed`, []string{"unclosed"}},
	}
	for _, tt := range tests {
		got := splitDCC(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("splitDCC(%q) = %v (len=%d), want %v (len=%d)", tt.in, got, len(got), tt.want, len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitDCC(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// isConnectError
// ---------------------------------------------------------------------------

func TestIsConnectError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{&testError{"connection refused"}, true},
		{&testError{"no route to host"}, true},
		{&testError{"network is unreachable"}, true},
		{&testError{"i/o timeout"}, true},
		{&testError{"no such host"}, true},
		{&testError{"dial tcp 1.2.3.4:6667: connect: connection refused"}, true},
		{&testError{"something else"}, false},
		{&testError{"timeout waiting"}, false},
	}
	for _, tt := range tests {
		got := isConnectError(tt.err)
		if got != tt.want {
			t.Errorf("isConnectError(%q) = %v, want %v", tt.err.Error(), got, tt.want)
		}
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// ---------------------------------------------------------------------------
// parseI64 / parseU32
// ---------------------------------------------------------------------------

func TestParseI64(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"42", 42},
		{"-1", -1},
		{"9999999999", 9999999999},
		{"abc", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := parseI64(tt.in)
		if got != tt.want {
			t.Errorf("parseI64(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseU32(t *testing.T) {
	tests := []struct {
		in   string
		want uint32
	}{
		{"0", 0},
		{"42", 42},
		{"4294967295", 4294967295},
		{"abc", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := parseU32(tt.in)
		if got != tt.want {
			t.Errorf("parseU32(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// randomSuffix
// ---------------------------------------------------------------------------

func TestRandomSuffix(t *testing.T) {
	for n := 0; n <= 10; n++ {
		s := randomSuffix(n)
		if len(s) != n {
			t.Errorf("randomSuffix(%d) length = %d, want %d", n, len(s), n)
		}
	}
}
