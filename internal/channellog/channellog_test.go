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

	// getOrOpen sanitizes the key "private" and appends ".log",
	// producing "private.log" on disk.
	path := filepath.Join(dir, "private.log")
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

	// Same as TestLogPrivate_CreatesFile: key "private" → "private.log".
	path := filepath.Join(dir, "private.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 30 {
		t.Errorf("expected 30 private log lines, got %d", len(lines))
	}
}

// TestReconcileChannels_ClosesStaleHandles verifies that channels no longer
// in the logged set have their file handles closed and removed from the map.
func TestReconcileChannels_ClosesStaleHandles(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	l.Log("#chan1", "alice", KindMessage, "hello from chan1")
	l.Log("#chan2", "bob", KindMessage, "hello from chan2")

	// Verify both channels have open handles.
	l.mu.Lock()
	if len(l.files) != 2 {
		t.Fatalf("expected 2 open files, got %d", len(l.files))
	}
	cf1 := l.files["#chan1"]
	cf2 := l.files["#chan2"]
	l.mu.Unlock()

	cf1.mu.Lock()
	handleOpen := cf1.f != nil
	cf1.mu.Unlock()
	if !handleOpen {
		t.Fatal("#chan1 handle should be non-nil before reconcile")
	}

	// Reconcile: keep #chan2, close #chan1.
	l.ReconcileChannels(func(ch string) bool {
		return ch == "#chan2"
	})

	// #chan1 should be removed from the files map.
	l.mu.Lock()
	_, chan1Exists := l.files["#chan1"]
	_, chan2Exists := l.files["#chan2"]
	l.mu.Unlock()
	if chan1Exists {
		t.Error("#chan1 should have been removed from files map after reconcile")
	}
	if !chan2Exists {
		t.Error("#chan2 should still be in files map after reconcile")
	}

	// cf1 still points to the old channelFile struct (Go GC keeps it alive).
	// ReconcileChannels closed the handle and set it to nil.
	cf1.mu.Lock()
	handleClosed := cf1.f == nil
	cf1.mu.Unlock()
	if !handleClosed {
		t.Error("#chan1 handle should be nil (closed) after reconcile")
	}

	// #chan2 handle should still be open.
	cf2.mu.Lock()
	handleStillOpen := cf2.f != nil
	cf2.mu.Unlock()
	if !handleStillOpen {
		t.Error("#chan2 handle should still be non-nil after reconcile")
	}

	// Writing to #chan1 again should transparently reopen it.
	l.Log("#chan1", "alice", KindMessage, "back to chan1")
	path := filepath.Join(dir, "#chan1.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile #chan1.log: %v", err)
	}
	if !strings.Contains(string(data), "back to chan1") {
		t.Error("expected 'back to chan1' in reopened #chan1 log")
	}
}

// TestReconcileChannels_KeepsAllWhenAllLogged verifies that ReconcileChannels
// is a no-op when all channels are still in the logged set.
func TestReconcileChannels_KeepsAllWhenAllLogged(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	l.Log("#chan1", "alice", KindMessage, "hi")
	l.Log("#chan2", "bob", KindMessage, "hey")

	// Reconcile keeping both.
	l.ReconcileChannels(func(ch string) bool {
		return true
	})

	// Both should still be open.
	l.mu.Lock()
	n := len(l.files)
	l.mu.Unlock()
	if n != 2 {
		t.Errorf("expected 2 files after reconcile-all, got %d", n)
	}
}

// TestReconcileChannels_PrivateKeyPreserved verifies the pattern used by
// ircmanager: the "private" key (from LogPrivate) is always kept even when
// IsChannelLogged returns false for it.
func TestReconcileChannels_PrivateKeyPreserved(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	l.LogPrivate("irc.example.com", "alice", "secret")
	l.Log("#news", "bob", KindMessage, "public")

	// Simulate the ircmanager pattern: always keep "private", delegate
	// channel decisions to IsChannelLogged (which returns false for #news).
	l.ReconcileChannels(func(ch string) bool {
		if ch == "private" {
			return true
		}
		// Simulating IsChannelLogged: #news is not in the list.
		return false
	})

	// "private" should be kept.
	l.mu.Lock()
	_, privExists := l.files["private"]
	_, newsExists := l.files["#news"]
	l.mu.Unlock()
	if !privExists {
		t.Error("private key should be preserved after reconcile")
	}
	if newsExists {
		t.Error("#news should have been removed after reconcile")
	}
}

// TestReconcileChannels_NilSafe verifies that ReconcileChannels is safe to
// call on a nil Logger or with a nil isLogged function.
func TestReconcileChannels_NilSafe(t *testing.T) {
	var l *Logger
	// Should not panic.
	l.ReconcileChannels(func(ch string) bool { return true })

	dir := t.TempDir()
	l2, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l2.Close()

	// Should not panic with nil isLogged.
	l2.ReconcileChannels(nil)

	// Logger should be unchanged.
	l2.mu.Lock()
	n := len(l2.files)
	l2.mu.Unlock()
	if n != 0 {
		t.Errorf("expected 0 files after nil-isLogged reconcile, got %d", n)
	}
}

// TestReconcileChannels_EmptyLogger verifies that ReconcileChannels works
// on an empty Logger (no files open).
func TestReconcileChannels_EmptyLogger(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer l.Close()

	// Reconcile with no files open — should be a no-op.
	l.ReconcileChannels(func(ch string) bool { return true })

	l.mu.Lock()
	n := len(l.files)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("expected 0 files, got %d", n)
	}
}
