package logging

import (
	"strings"
	"testing"
	"time"

	"xdcc_server/internal/sse"
)

// ---------------------------------------------------------------------------
// NewLogBroadcaster
// ---------------------------------------------------------------------------

func TestNewLogBroadcaster(t *testing.T) {
	h := sse.NewHub(10)
	b := NewLogBroadcaster(h)
	if b == nil {
		t.Fatal("expected non-nil broadcaster")
	}
	if b.hub != h {
		t.Error("expected hub to be set")
	}
	if b.buf == nil {
		t.Error("expected non-nil buffer")
	}
	if b.maxLen != MaxLogBufferLines {
		t.Errorf("expected maxLen=%d, got %d", MaxLogBufferLines, b.maxLen)
	}
}

// ---------------------------------------------------------------------------
// Write
// ---------------------------------------------------------------------------

func TestBroadcasterWrite(t *testing.T) {
	h := sse.NewHub(10)
	b := NewLogBroadcaster(h)
	ch := h.Subscribe()
	go drainSSE(ch)

	line := "2024-01-15T10:30:00Z [INFO] server started\n"
	n, err := b.Write([]byte(line))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != len(line) {
		t.Errorf("expected %d bytes written, got %d", len(line), n)
	}
}

func TestBroadcasterWriteEmptyLine(t *testing.T) {
	h := sse.NewHub(10)
	b := NewLogBroadcaster(h)

	n, err := b.Write([]byte("\n"))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 byte written, got %d", n)
	}
}

func TestBroadcasterWriteBlankLine(t *testing.T) {
	h := sse.NewHub(10)
	b := NewLogBroadcaster(h)

	n, err := b.Write([]byte("   \n"))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != 4 {
		t.Errorf("expected 4 bytes written, got %d", n)
	}
}

func TestBroadcasterWriteWithoutNewline(t *testing.T) {
	h := sse.NewHub(10)
	b := NewLogBroadcaster(h)

	n, err := b.Write([]byte("partial line"))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != 12 {
		t.Errorf("expected 12 bytes written, got %d", n)
	}
}

func TestBroadcasterPublishesToSSE(t *testing.T) {
	h := sse.NewHub(10)
	b := NewLogBroadcaster(h)
	ch := h.Subscribe()

	line := "2024-01-15T10:30:00Z [WARN] low disk space\n"
	b.Write([]byte(line))

	select {
	case evt := <-ch:
		if evt.Type != sse.EventLogEntry {
			t.Errorf("expected event type 'log_entry', got %q", evt.Type)
		}
		if evt.Payload["level"] != "WARN" {
			t.Errorf("expected level 'WARN', got %v", evt.Payload["level"])
		}
		if msg, ok := evt.Payload["message"].(string); !ok || !strings.Contains(msg, "low disk space") {
			t.Errorf("expected message containing 'low disk space', got %v", evt.Payload["message"])
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for SSE event")
	}

	h.Unsubscribe(ch)
}

// ---------------------------------------------------------------------------
// RecentEntries
// ---------------------------------------------------------------------------

func TestRecentEntriesEmpty(t *testing.T) {
	h := sse.NewHub(10)
	b := NewLogBroadcaster(h)

	entries := b.RecentEntries(10)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestRecentEntriesFewerThanBuffer(t *testing.T) {
	h := sse.NewHub(10)
	b := NewLogBroadcaster(h)

	b.Write([]byte("2024-01-15T10:30:00Z [INFO] line1\n"))
	b.Write([]byte("2024-01-15T10:30:01Z [INFO] line2\n"))
	b.Write([]byte("2024-01-15T10:30:02Z [INFO] line3\n"))

	entries := b.RecentEntries(5)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Message, "line1") {
		t.Errorf("first entry: %q", entries[0].Message)
	}
	if !strings.Contains(entries[2].Message, "line3") {
		t.Errorf("last entry: %q", entries[2].Message)
	}
}

func TestRecentEntriesRequestMoreThanAvailable(t *testing.T) {
	h := sse.NewHub(10)
	b := NewLogBroadcaster(h)

	b.Write([]byte("2024-01-15T10:30:00Z [INFO] only\n"))

	entries := b.RecentEntries(100)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func TestRecentEntriesChronologicalOrder(t *testing.T) {
	h := sse.NewHub(10)
	b := NewLogBroadcaster(h)

	// Write in order
	b.Write([]byte("2024-01-15T10:30:00Z [INFO] first\n"))
	b.Write([]byte("2024-01-15T10:30:01Z [INFO] second\n"))
	b.Write([]byte("2024-01-15T10:30:02Z [INFO] third\n"))

	entries := b.RecentEntries(3)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if !strings.Contains(entries[0].Message, "first") {
		t.Errorf("entry 0: %q", entries[0].Message)
	}
	if !strings.Contains(entries[1].Message, "second") {
		t.Errorf("entry 1: %q", entries[1].Message)
	}
	if !strings.Contains(entries[2].Message, "third") {
		t.Errorf("entry 2: %q", entries[2].Message)
	}
}

// ---------------------------------------------------------------------------
// parseLine
// ---------------------------------------------------------------------------

func TestParseLineFull(t *testing.T) {
	b := NewLogBroadcaster(sse.NewHub(1))
	entry := b.parseLine("2024-01-15T10:30:00Z [WARN] something happened key=val")

	if entry.Level != "WARN" {
		t.Errorf("expected level WARN, got %q", entry.Level)
	}
	if !strings.Contains(entry.Message, "something happened") {
		t.Errorf("expected message, got %q", entry.Message)
	}
	if entry.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestParseLineNoTimestamp(t *testing.T) {
	b := NewLogBroadcaster(sse.NewHub(1))
	entry := b.parseLine("just a message")

	if entry.Level != "INFO" {
		t.Errorf("expected default level INFO, got %q", entry.Level)
	}
	if entry.Message != "just a message" {
		t.Errorf("expected message, got %q", entry.Message)
	}
}

func TestParseLineRFC3339WithTZ(t *testing.T) {
	b := NewLogBroadcaster(sse.NewHub(1))
	entry := b.parseLine("2024-01-15T10:30:00+02:00 [DEBUG] trace data")

	if entry.Level != "DEBUG" {
		t.Errorf("expected level DEBUG, got %q", entry.Level)
	}
	if entry.Message != "trace data" {
		t.Errorf("expected 'trace data', got %q", entry.Message)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func drainSSE(ch chan sse.Event) {
	for range ch {
		continue
	}
}
