package logging

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Level.String()
// ---------------------------------------------------------------------------

func TestLevelString(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{LevelDebug, "DEBUG"},
		{LevelInfo, "INFO"},
		{LevelWarn, "WARN"},
		{LevelError, "ERROR"},
		{Level(99), "UNKNOWN"},
	}
	for _, tt := range tests {
		got := tt.level.String()
		if got != tt.want {
			t.Errorf("Level(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// ParseLevel
// ---------------------------------------------------------------------------

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  Level
	}{
		{"debug", LevelDebug},
		{"info", LevelInfo},
		{"warn", LevelWarn},
		{"warning", LevelWarn},
		{"error", LevelError},
		{"", LevelInfo},
		{"unknown", LevelInfo}, // falls back with error
	}
	for _, tt := range tests {
		got, _ := ParseLevel(tt.input)
		if got != tt.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseLevelError(t *testing.T) {
	_, err := ParseLevel("garbage")
	if err == nil {
		t.Error("expected error for unknown level")
	}
}

// ---------------------------------------------------------------------------
// New / Close
// ---------------------------------------------------------------------------

func TestNewStdoutOnly(t *testing.T) {
	l := New(LevelInfo, "", 0)
	if l == nil {
		t.Fatal("expected non-nil logger")
	}
	// No file → close should be no-op
	if err := l.Close(); err != nil {
		t.Errorf("Close should not error: %v", err)
	}
}

func TestNewWithFile(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/test.log"
	l := New(LevelDebug, path, 0)
	defer l.Close()

	if l == nil {
		t.Fatal("expected non-nil logger")
	}
	// Log something and verify file exists
	l.Info("hello")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("log file should exist after write")
	}
}

func TestClose(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/test.log"
	l := New(LevelInfo, path, 0)
	l.Info("write")
	if err := l.Close(); err != nil {
		t.Errorf("Close error: %v", err)
	}
	// Second close should not panic
	if err := l.Close(); err == nil {
		t.Log("second close returned nil (file already closed)")
	}
}

// ---------------------------------------------------------------------------
// SetLevel
// ---------------------------------------------------------------------------

func TestSetLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelInfo, "", 0)
	l.AddWriter(&buf)

	// Messages pass at Info level
	l.Info("passes")
	if !strings.Contains(buf.String(), "passes") {
		t.Error("info message should pass at Info level")
	}

	// Switch to Error level
	l.SetLevel(LevelError)
	l.Info("filtered_out")
	l.Warn("filtered_out")
	l.Error("still_passes")

	if strings.Contains(buf.String(), "filtered_out") {
		t.Error("filtered messages should not appear after SetLevel")
	}
	if !strings.Contains(buf.String(), "still_passes") {
		t.Error("error messages should still appear after SetLevel")
	}
}

// ---------------------------------------------------------------------------
// Log level filtering via Writer capture
// ---------------------------------------------------------------------------

func TestLogLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelWarn, "", 0)
	l.AddWriter(&buf)

	l.Debug("should not appear")
	l.Info("should not appear")
	l.Warn("should appear")
	l.Error("should appear")

	out := buf.String()
	if strings.Contains(out, "should not appear") {
		t.Error("filtered messages should not appear")
	}
	if !strings.Contains(out, "should appear") {
		t.Error("warn/error messages should appear")
	}
	if !strings.Contains(out, "[WARN]") {
		t.Error("expected [WARN] level tag")
	}
	if !strings.Contains(out, "[ERROR]") {
		t.Error("expected [ERROR] level tag")
	}
}

// ---------------------------------------------------------------------------
// Printf (xdccirc.Logger interface)
// ---------------------------------------------------------------------------

func TestPrintf(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelInfo, "", 0)
	l.AddWriter(&buf)

	l.Printf("hello %s", "world")
	out := buf.String()
	if !strings.Contains(out, "hello world") {
		t.Errorf("expected 'hello world' in output, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// Debugf / Infof / Warnf / Errorf
// ---------------------------------------------------------------------------

func TestFormattedMethods(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelDebug, "", 0)
	l.AddWriter(&buf)

	l.Debugf("debug %d", 1)
	l.Infof("info %d", 2)
	l.Warnf("warn %d", 3)
	l.Errorf("error %d", 4)

	out := buf.String()
	if !strings.Contains(out, "debug 1") {
		t.Error("expected debugf output")
	}
	if !strings.Contains(out, "info 2") {
		t.Error("expected infof output")
	}
	if !strings.Contains(out, "warn 3") {
		t.Error("expected warnf output")
	}
	if !strings.Contains(out, "error 4") {
		t.Error("expected errorf output")
	}
}

// ---------------------------------------------------------------------------
// AddWriter
// ---------------------------------------------------------------------------

func TestAddWriter(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	l := New(LevelInfo, "", 0)
	l.AddWriter(&buf1)
	l.AddWriter(&buf2)

	l.Info("test")

	if !strings.Contains(buf1.String(), "test") {
		t.Error("buf1 should contain the message")
	}
	if !strings.Contains(buf2.String(), "test") {
		t.Error("buf2 should contain the message")
	}
}

// ---------------------------------------------------------------------------
// formatKeyValues
// ---------------------------------------------------------------------------

func TestFormatKeyValues(t *testing.T) {
	tests := []struct {
		kv   []interface{}
		want string
	}{
		{nil, ""},
		{[]interface{}{}, ""},
		{[]interface{}{"a", "b"}, " a=b"},
		{[]interface{}{"x", 42, "y", "z"}, " x=42 y=z"},
		{[]interface{}{"single"}, " single"},
	}
	for _, tt := range tests {
		got := formatKeyValues(tt.kv...)
		if got != tt.want {
			t.Errorf("formatKeyValues(%v) = %q, want %q", tt.kv, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// LevelWriter
// ---------------------------------------------------------------------------

func TestLevelWriter(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelDebug, "", 0)
	l.AddWriter(&buf)

	w := l.Writer(LevelWarn)
	w.Write([]byte("line1\nline2\nline3_no_newline"))

	out := buf.String()
	if !strings.Contains(out, "line1") {
		t.Error("expected line1")
	}
	if !strings.Contains(out, "line2") {
		t.Error("expected line2")
	}
	// line3_no_newline stays in buffer until flushed
	if strings.Contains(out, "line3_no_newline") {
		t.Error("line without newline should not be flushed immediately")
	}
}

func TestLevelWriterMultiline(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelDebug, "", 0)
	l.AddWriter(&buf)

	w := l.Writer(LevelInfo)
	w.Write([]byte("first\nsecond\n"))

	out := buf.String()
	if !strings.Contains(out, "first") && !strings.Contains(out, "second") {
		t.Error("expected both lines")
	}
}

// ---------------------------------------------------------------------------
// Default logger
// ---------------------------------------------------------------------------

func TestDefaultLogger(t *testing.T) {
	// Should not panic
	Debug("test debug")
	Info("test info")
	Warn("test warn")
	Error("test error")
}

func TestSetDefaultLogger(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelDebug, "", 0)
	l.AddWriter(&buf)
	SetDefaultLogger(l)

	Info("custom default")
	if !strings.Contains(buf.String(), "custom default") {
		t.Error("expected message from custom default logger")
	}

	// Restore
	SetDefaultLogger(New(LevelInfo, "", 0))
}

// ---------------------------------------------------------------------------
// Debug / Info / Warn / Error (structured)
// ---------------------------------------------------------------------------

func TestStructuredLogging(t *testing.T) {
	var buf bytes.Buffer
	l := New(LevelDebug, "", 0)
	l.AddWriter(&buf)

	l.Debug("download started", "bot", "TestBot", "pack", 42)
	out := buf.String()
	if !strings.Contains(out, "download started") {
		t.Error("expected message")
	}
	if !strings.Contains(out, "bot=TestBot") {
		t.Error("expected key-value pair")
	}
	if !strings.Contains(out, "pack=42") {
		t.Error("expected key-value pair")
	}
}

// ---------------------------------------------------------------------------
// Log file rotation (basic smoke test)
// ---------------------------------------------------------------------------

func TestRotationCheckDoesNotPanic(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/rotate.log"
	// maxSizeMB=0 disables rotation
	l := New(LevelInfo, path, 0)
	defer l.Close()

	// Write many lines — should not trigger rotation
	for i := 0; i < 200; i++ {
		l.Info("line")
	}
	// Just verifying no panic
}

// TestRotationSmoke writes enough lines to exercise the rotation check code path
// without hitting the actual rotation threshold (1MB is too large for a unit test).
func TestRotationSmoke(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/rotate.log"
	l := New(LevelInfo, path, 1)
	defer l.Close()

	// Write enough lines to trigger the periodic rotation check (every 100 writes)
	msg := strings.Repeat("x", 80)
	for i := 0; i < 300; i++ {
		l.Info(msg)
	}
}
