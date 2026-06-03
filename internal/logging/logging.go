// Package logging provides structured logging with configurable levels,
// file output, and basic log rotation support (Fase 9.6).
package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Level
// ---------------------------------------------------------------------------

// Level represents a log severity level.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// ParseLevel converts a string to a Level.
func ParseLevel(s string) (Level, error) {
	switch s {
	case "debug":
		return LevelDebug, nil
	case "info":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	case "":
		return LevelInfo, nil
	default:
		return LevelInfo, fmt.Errorf("unknown log level %q, using info", s)
	}
}

// ---------------------------------------------------------------------------
// Logger
// ---------------------------------------------------------------------------

// Logger is a structured logger that writes to multiple outputs (stdout + file).
// It supports level filtering and structured key-value pairs.
type Logger struct {
	mu           sync.Mutex
	level        Level
	logger       *log.Logger
	file         io.WriteCloser
	filePath     string
	maxSizeMB    int
	extraWriters []io.Writer // additional outputs added via AddWriter

	// writeCount tracks writes since the last rotation check. Rotation checks
	// (os.Stat on the log file) happen every checkRotateInterval writes to
	// avoid a syscall on every single log line at high throughput.
	writeCount int64
}

// checkRotateInterval is the number of log writes between rotation checks.
const checkRotateInterval = 100

// New creates a new Logger.
//   - level: minimum level to log
//   - filePath: optional path to log file (empty = stdout only)
//   - maxSizeMB: max file size in MB before rotation (0 = no rotation)
func New(level Level, filePath string, maxSizeMB int) *Logger {
	l := &Logger{
		level:     level,
		maxSizeMB: maxSizeMB,
	}

	// Create multi-writer
	if filePath != "" {
		dir := filepath.Dir(filePath)
		if err := os.MkdirAll(dir, 0o755); err == nil {
			f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err == nil {
				l.file = f
				l.filePath = filePath
			}
		}
	}

	l.rebuildMultiWriter()

	return l
}

// Close closes the log file if open.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

// SetLevel changes the minimum log level at runtime.
func (l *Logger) SetLevel(level Level) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// AddWriter adds an additional output destination to the logger.
// Log lines are written to stderr + file (if configured) + all added writers.
// Multiple calls each add a new writer — previous ones are preserved.
func (l *Logger) AddWriter(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.extraWriters = append(l.extraWriters, w)
	l.rebuildMultiWriter()
}

// ---------------------------------------------------------------------------
// Structured logging methods
// ---------------------------------------------------------------------------

// Debug logs a debug message with optional key-value pairs.
func (l *Logger) Debug(msg string, kv ...interface{}) {
	l.log(LevelDebug, msg, kv...)
}

// Info logs an info message with optional key-value pairs.
func (l *Logger) Info(msg string, kv ...interface{}) {
	l.log(LevelInfo, msg, kv...)
}

// Warn logs a warning message with optional key-value pairs.
func (l *Logger) Warn(msg string, kv ...interface{}) {
	l.log(LevelWarn, msg, kv...)
}

// Error logs an error message with optional key-value pairs.
func (l *Logger) Error(msg string, kv ...interface{}) {
	l.log(LevelError, msg, kv...)
}

// Printf logs a formatted message at INFO level.
// This method satisfies the xdccirc.Logger interface, making
// *logging.Logger a drop-in replacement for *log.Logger in all
// components that expect a Printf-based logger.
func (l *Logger) Printf(format string, args ...interface{}) {
	l.log(LevelInfo, fmt.Sprintf(format, args...))
}

// Debugf logs a formatted debug message.
func (l *Logger) Debugf(format string, args ...interface{}) {
	l.log(LevelDebug, fmt.Sprintf(format, args...))
}

// Infof logs a formatted info message.
func (l *Logger) Infof(format string, args ...interface{}) {
	l.log(LevelInfo, fmt.Sprintf(format, args...))
}

// Warnf logs a formatted warning message.
func (l *Logger) Warnf(format string, args ...interface{}) {
	l.log(LevelWarn, fmt.Sprintf(format, args...))
}

// Errorf logs a formatted error message.
func (l *Logger) Errorf(format string, args ...interface{}) {
	l.log(LevelError, fmt.Sprintf(format, args...))
}

// Writer returns an io.Writer that logs each line at the given level.
// This is useful for adapting to APIs that expect a stdlib *log.Logger.
// The returned writer logs at the specified level; it strips trailing newlines.
func (l *Logger) Writer(level Level) io.Writer {
	return &levelWriter{l: l, level: level}
}

type levelWriter struct {
	l     *Logger
	level Level
	buf   []byte
}

// maxLineBuf is the maximum size of the line buffer in a levelWriter.
// If a writer produces output without a newline, the buffer is flushed
// as a complete line to prevent unbounded growth.
const maxLineBuf = 64 * 1024

func (w *levelWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.buf = append(w.buf, p...)
	for {
		i := indexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if line != "" {
			w.l.log(w.level, line)
		}
	}
	// Flush if buffer exceeds the limit to prevent unbounded growth
	if len(w.buf) > maxLineBuf {
		w.l.log(w.level, string(w.buf))
		w.buf = w.buf[:0]
	}
	return n, nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// log formats and writes a structured log entry.
func (l *Logger) log(level Level, msg string, kv ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if level < l.level {
		return
	}

	now := time.Now().Format(time.RFC3339)
	parts := formatKeyValues(kv...)

	// Check file size for rotation (throttled to avoid os.Stat per write)
	if l.file != nil && l.maxSizeMB > 0 {
		l.writeCount++
		if l.writeCount >= checkRotateInterval {
			l.writeCount = 0
			l.rotateIfNeeded()
		}
	}

	line := fmt.Sprintf("%s [%s] %s %s\n", now, level.String(), msg, parts)
	_ = l.logger.Output(2, line)
}

// ---------------------------------------------------------------------------
// Log rotation
// ---------------------------------------------------------------------------

// rotateIfNeeded rotates the log file if it exceeds maxSizeMB.
// On rename failure it switches to a timestamped path to prevent an infinite
// rotation loop (where the oversized file can't be renamed and every log write
// triggers another rotation attempt).
func (l *Logger) rotateIfNeeded() {
	if l.filePath == "" || l.maxSizeMB <= 0 {
		return
	}

	info, err := os.Stat(l.filePath)
	if err != nil {
		return
	}

	maxBytes := int64(l.maxSizeMB) * 1024 * 1024
	if info.Size() < maxBytes {
		return
	}

	// Close current file
	l.file.Close()
	l.file = nil

	// Rename current file to a timestamped backup
	backupPath := fmt.Sprintf("%s.%s", l.filePath, time.Now().Format("20060102-150405"))
	if err := os.Rename(l.filePath, backupPath); err != nil {
		// Rename failed (permissions, file lock, etc.). To prevent an
		// infinite rotation loop, switch to a new log file with a
		// timestamp suffix. The old oversized file is orphaned but
		// won't keep growing.
		fmt.Fprintf(os.Stderr, "WARNING: log rotation rename failed for %s: %v — switching to new path\n", l.filePath, err)
		l.filePath = fmt.Sprintf("%s.%s", l.filePath, time.Now().Format("20060102-150405"))
	}

	// Open new file
	f, err := os.OpenFile(l.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to open new log file %s: %v — logging to stderr only\n", l.filePath, err)
		l.rebuildMultiWriter()
		return
	}
	l.file = f

	l.rebuildMultiWriter()
}

// rebuildMultiWriter reconstructs the io.MultiWriter from stderr + file +
// all extra writers. Creates the underlying log.Logger on first call
// (during New), and updates its output on subsequent calls.
// Caller must hold l.mu (except during New, where the Logger is not yet shared).
func (l *Logger) rebuildMultiWriter() {
	var writers []io.Writer
	writers = append(writers, os.Stderr)
	if l.file != nil {
		writers = append(writers, l.file)
	}
	writers = append(writers, l.extraWriters...)
	multi := io.MultiWriter(writers...)
	if l.logger == nil {
		l.logger = log.New(multi, "", 0)
	} else {
		l.logger.SetOutput(multi)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// formatKeyValues formats alternating key-value pairs into a string.
// e.g. formatKeyValues("key1", "val1", "key2", 42) → "key1=val1 key2=42"
func formatKeyValues(kv ...interface{}) string {
	if len(kv) == 0 {
		return ""
	}

	var result string
	for i := 0; i < len(kv)-1; i += 2 {
		key := fmt.Sprintf("%v", kv[i])
		val := fmt.Sprintf("%v", kv[i+1])
		result += fmt.Sprintf(" %s=%s", key, val)
	}

	// Odd number of kv — last one is just a value
	if len(kv)%2 == 1 {
		result += fmt.Sprintf(" %v", kv[len(kv)-1])
	}

	return result
}

// ---------------------------------------------------------------------------
// Global default logger
// ---------------------------------------------------------------------------

var defaultLogger = New(LevelInfo, "", 0)

// SetDefaultLogger sets the package-level default logger.
func SetDefaultLogger(l *Logger) {
	defaultLogger = l
}

// Debug logs via the default logger.
func Debug(msg string, kv ...interface{}) {
	defaultLogger.Debug(msg, kv...)
}

// Info logs via the default logger.
func Info(msg string, kv ...interface{}) {
	defaultLogger.Info(msg, kv...)
}

// Warn logs via the default logger.
func Warn(msg string, kv ...interface{}) {
	defaultLogger.Warn(msg, kv...)
}

// Error logs via the default logger.
func Error(msg string, kv ...interface{}) {
	defaultLogger.Error(msg, kv...)
}
