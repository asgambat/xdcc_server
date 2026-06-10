package channellog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSanitizeChannelName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "unknown"},
		{"#foo", "#foo"},
		{"&local", "&local"},
		{"#foo bar!", "#foo_bar!"}, // '!' is in the IRC-safe allowed set
		{"../etc", ".._etc"},
		{"normal", "normal"},
		{"#CAPS", "#CAPS"},
		{"+", "+"},
		{"!excl", "!excl"},
		{"test-dash.dot", "test-dash.dot"},
		{"\x00\x01\x02", "___"},
	}
	for _, tt := range tests {
		got := sanitizeChannelName(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeChannelName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLog_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	l.Log("#test", "alice", KindMessage, "hello world")

	path := filepath.Join(dir, "#test.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "[msg]") {
		t.Errorf("expected [msg] in log line, got %q", s)
	}
	if !strings.Contains(s, "<alice>") {
		t.Errorf("expected <alice> in log line, got %q", s)
	}
	if !strings.Contains(s, "hello world") {
		t.Errorf("expected message in log line, got %q", s)
	}
}

func TestLog_TruncatesLongMessage(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	longMsg := strings.Repeat("A", 5000)
	l.Log("#test", "bob", KindMessage, longMsg)

	path := filepath.Join(dir, "#test.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(data)
	// The line should contain the truncated message (4000 chars + metadata).
	if len(s) > 4200 {
		t.Errorf("expected truncated log line, got length %d", len(s))
	}
}

func TestLog_SanitizesNewlines(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	l.Log("#test", "evil\nnick", KindMessage, "line1\nline2\r\nline3")

	path := filepath.Join(dir, "#test.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(data)
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) != 1 {
		t.Errorf("expected exactly 1 log line (newlines sanitized), got %d", len(lines))
	}
}

func TestLog_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			l.Log("#concurrent", "user", KindMessage, "msg")
		}(i)
	}
	wg.Wait()

	path := filepath.Join(dir, "#concurrent.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 50 {
		t.Errorf("expected 50 log lines, got %d", len(lines))
	}
}

func TestLogPrivate_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	l.LogPrivate("irc.rizon.net", "alice", "secret message")

	// getOrOpen sanitizes the key and appends ".log", so "private.log"
	// becomes "private.log.log" on disk ("." is in the allowed char set).
	path := filepath.Join(dir, "private.log.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "[irc.rizon.net]") {
		t.Errorf("expected server address in log, got %q", s)
	}
	if !strings.Contains(s, "<alice>") {
		t.Errorf("expected sender in log, got %q", s)
	}
	if !strings.Contains(s, "secret message") {
		t.Errorf("expected message in log, got %q", s)
	}
}

func TestClose_NilSafe(t *testing.T) {
	var l *Logger
	if err := l.Close(); err != nil {
		t.Errorf("Close on nil Logger should return nil, got %v", err)
	}
}

func TestLog_EmptyChannel(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	// Should be a no-op, not panic.
	l.Log("", "alice", KindMessage, "hello")

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected no files for empty channel, got %d", len(entries))
	}
}

func TestNew_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "logs")
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New should create nested dirs: %v", err)
	}
	defer l.Close()

	l.Log("#test", "user", KindMessage, "hi")

	path := filepath.Join(dir, "#test.log")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected log file to exist in nested directory")
	}
}

func TestLog_NoticeKind(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	l.Log("#test", "bot", KindNotice, "pack #42 ready")

	path := filepath.Join(dir, "#test.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "[notice]") {
		t.Errorf("expected [notice] in log line, got %q", s)
	}
}

func TestLogPrivate_Concurrent(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.LogPrivate("irc.example.com", "user", "msg")
		}()
	}
	wg.Wait()

	// Same as TestLogPrivate_CreatesFile: "private.log" → "private.log.log".
	path := filepath.Join(dir, "private.log.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 30 {
		t.Errorf("expected 30 private log lines, got %d", len(lines))
	}
}
