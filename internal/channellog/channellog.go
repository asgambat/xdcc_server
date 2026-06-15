// Package channellog implements the hidden per-channel IRC message logger.
//
// When a channel name is added to irc.channel_log in config.yaml, every
// PRIVMSG and NOTICE sent to that channel is appended to a per-channel log
// file (one file per channel, named "<channel>.log") in the directory of
// the main logging.file_path (or ./logs/ if the main log is on stderr).
//
// The package is intentionally undocumented in user-facing surfaces (no
// REST endpoint, no UI) and is gated on the operator opting in via YAML.
package channellog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EventKind distinguishes a PRIVMSG ("msg") from a NOTICE ("notice") in
// the log file. The kind is written as a short prefix on each line so the
// file is human-readable.
type EventKind string

const (
	KindMessage EventKind = "msg"
	KindNotice  EventKind = "notice"
)

// syncInterval is how often the background goroutine calls Sync() on all
// open log files to guarantee durability on crash/power-loss.
const syncInterval = 30 * time.Second

// Logger appends per-channel IRC traffic to one file per channel.
// Safe for concurrent use from multiple goroutines (girc dispatch + ticker).
//
// Files are created lazily on the first write for a given channel and
// kept open for the lifetime of the Logger so the hot path does not pay
// the cost of Open/Close on every line. Each file is protected by its
// own mutex to keep ordering correct within a single channel.
//
// A background goroutine periodically calls Sync() on all open files
// (every 30s) so that recent entries survive a crash or power loss.
type Logger struct {
	dir string // absolute directory where <channel>.log files live

	mu    sync.Mutex
	files map[string]*channelFile

	stopCh chan struct{} // closed by Close() to stop the sync goroutine
}

// channelFile holds the per-channel state: the open file handle and its
// own mutex so writes to different channels never block each other.
type channelFile struct {
	mu   sync.Mutex
	f    *os.File
	size int64
}

// New creates a Logger that writes into the given directory.
// The directory is created (with parents, mode 0o755) if it does not exist.
// An empty dir means "use the current working directory"; pass "./logs" or
// the resolved directory of the main log file to keep the per-channel logs
// alongside the main server log.
func New(dir string) (*Logger, error) {
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating channel log directory %q: %w", dir, err)
	}
	l := &Logger{
		dir:    dir,
		files:  make(map[string]*channelFile),
		stopCh: make(chan struct{}),
	}
	// Start a background goroutine that periodically flushes all open
	// files to disk. This ensures that log entries survive a crash or
	// power-loss even if the OS page cache hasn't been flushed yet.
	go l.syncLoop()
	return l, nil
}

// syncLoop periodically calls Sync() on all open file handles.
// Exits when stopCh is closed (by Close()).
func (l *Logger) syncLoop() {
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			l.syncAll()
		}
	}
}

// syncAll calls Sync() on every open file handle. Best-effort: errors
// are silently ignored since the OS will eventually flush anyway, and
// we don't want sync failures to disrupt the hot path.
func (l *Logger) syncAll() {
	l.mu.Lock()
	files := make([]*channelFile, 0, len(l.files))
	for _, cf := range l.files {
		files = append(files, cf)
	}
	l.mu.Unlock()

	for _, cf := range files {
		cf.mu.Lock()
		if cf.f != nil {
			_ = cf.f.Sync()
		}
		cf.mu.Unlock()
	}
}

// sanitizeChannelName maps an arbitrary IRC channel name to a safe filename
// component. IRC channel names are limited to a small character set, but we
// defensively replace anything else with '_' to prevent path-traversal or
// collisions with system files. A leading '#' or '&' is preserved.
//
//	"#foo bar!"  → "#foo_bar!"  ('!' is in the IRC-safe allowed set)
//	"&local"     → "&local"
//	"../etc"     → ".._etc"  (cannot escape the parent directory)
func sanitizeChannelName(channel string) string {
	if channel == "" {
		return "unknown"
	}
	allowed := func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z':
			return true
		case r >= 'A' && r <= 'Z':
			return true
		case r >= '0' && r <= '9':
			return true
		case r == '#' || r == '&' || r == '+' || r == '!' || r == '_' || r == '-' || r == '.':
			return true
		}
		return false
	}
	var b strings.Builder
	b.Grow(len(channel))
	for _, r := range channel {
		if allowed(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}

// Log appends a single event to the per-channel log file. The format is a
// single line per event, easy to grep / tail:
//
//	2026-05-25T14:23:11Z [msg] <nick> hello everyone
//	2026-05-25T14:23:42Z [notice] -SomeBot- pack #42 ready
//
// Lines that exceed maxBytes (default 4000) are truncated to keep the log
// readable. If the open file handle for the channel has been closed by a
// concurrent process (extremely rare), a new handle is opened transparently.
func (l *Logger) Log(channel, sender string, kind EventKind, message string) {
	if channel == "" {
		return
	}

	cf := l.getOrOpen(channel)
	if cf == nil {
		return
	}

	cf.mu.Lock()
	defer cf.mu.Unlock()

	// Re-open if the file was closed underneath us (e.g. a previous write
	// encountered an error and called Close to flush state).
	if cf.f == nil {
		path := filepath.Join(l.dir, sanitizeChannelName(channel)+".log")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		cf.f = f
	}

	// Truncate excessively long messages to a sane upper bound so a
	// malicious or runaway bot cannot fill the disk with a single line.
	const maxBytes = 4000
	if len(message) > maxBytes {
		message = message[:maxBytes]
	}

	line := fmt.Sprintf("%s [%s] <%s> %s\n",
		time.Now().UTC().Format(time.RFC3339),
		kind,
		sanitizeForLog(sender),
		sanitizeForLog(message),
	)
	n, err := cf.f.WriteString(line)
	if err != nil {
		// On error close the handle so the next call reopens it.
		_ = cf.f.Close()
		cf.f = nil
		return
	}
	cf.size += int64(n)
}

// LogPrivate appends a private PRIVMSG event to the private.log file.
// The format includes the server address, sender nick, and message:
//
//	2026-06-10T15:23:11Z [irc.rizon.net] <mario> hello
//
// Lines are truncated at maxBytes for safety. The same file handle reuse,
// lazy open, and auto-reopen logic from Log() applies.
//
// This method is independent from Log() — private.log is a single shared
// file, not a per-channel file.
func (l *Logger) LogPrivate(serverAddr, sender, message string) {
	// Use "private" as the lookup key so that getOrOpen creates the file
	// as "private.log" on disk (sanitizeChannelName("private")+".log").
	// The previous key "private.log" resulted in "private.log.log" because
	// "." is in the allowed character set and getOrOpen always appends
	// ".log" to the sanitized name.
	const privateLogKey = "private"
	const privateFileName = "private.log"

	cf := l.getOrOpen(privateLogKey)
	if cf == nil {
		return
	}

	cf.mu.Lock()
	defer cf.mu.Unlock()

	// Re-open if the file was closed underneath us.
	if cf.f == nil {
		path := filepath.Join(l.dir, privateFileName)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		cf.f = f
	}

	// Truncate excessively long messages.
	const maxBytes = 4000
	if len(message) > maxBytes {
		message = message[:maxBytes]
	}

	line := fmt.Sprintf("%s [%s] <%s> %s\n",
		time.Now().UTC().Format(time.RFC3339),
		sanitizeForLog(serverAddr),
		sanitizeForLog(sender),
		sanitizeForLog(message),
	)
	n, err := cf.f.WriteString(line)
	if err != nil {
		_ = cf.f.Close()
		cf.f = nil
		return
	}
	cf.size += int64(n)
}

// Close syncs and closes all open file handles, then stops the background
// sync goroutine. Safe to call multiple times.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	// Stop the background sync goroutine first (idempotent via close).
	select {
	case <-l.stopCh:
		// Already closed.
	default:
		close(l.stopCh)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	var firstErr error
	for name, cf := range l.files {
		cf.mu.Lock()
		if cf.f != nil {
			// Sync before close to flush any buffered data.
			_ = cf.f.Sync()
			if err := cf.f.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
			cf.f = nil
		}
		cf.mu.Unlock()
		delete(l.files, name)
	}
	return firstErr
}

// getOrOpen returns the channelFile for the given channel, opening it on
// first access. Returns nil if the file cannot be opened.
func (l *Logger) getOrOpen(channel string) *channelFile {
	l.mu.Lock()
	cf, ok := l.files[channel]
	if !ok {
		path := filepath.Join(l.dir, sanitizeChannelName(channel)+".log")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			l.mu.Unlock()
			return nil
		}
		cf = &channelFile{f: f}
		l.files[channel] = cf
	}
	l.mu.Unlock()
	return cf
}

// sanitizeForLog strips embedded newlines from sender / message so each event
// occupies exactly one line in the log file. Other whitespace is preserved.
func sanitizeForLog(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}
