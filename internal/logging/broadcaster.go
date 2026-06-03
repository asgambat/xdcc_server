package logging

import (
	"strings"
	"sync"
	"time"

	"xdcc-go/internal/sse"
)

// ---------------------------------------------------------------------------
// LogBroadcaster — bridges log output to SSE hub with a ring buffer replay
// ---------------------------------------------------------------------------

// MaxLogBufferLines is the max number of log lines kept in the ring buffer
// for SSE replay (sent to clients on initial connect).
const MaxLogBufferLines = 200

// LogEntry represents a single log line sent via SSE.
type LogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// LogBroadcaster implements io.Writer. It parses incoming log lines,
// stores them in a ring buffer, and publishes them to the SSE hub.
type LogBroadcaster struct {
	mu       sync.Mutex
	hub      *sse.Hub
	buf      []LogEntry
	bufPos   int
	bufCount int
	maxLen   int
}

// NewLogBroadcaster creates a new LogBroadcaster that publishes to the given hub.
func NewLogBroadcaster(hub *sse.Hub) *LogBroadcaster {
	return &LogBroadcaster{
		hub:    hub,
		buf:    make([]LogEntry, MaxLogBufferLines),
		maxLen: MaxLogBufferLines,
	}
}

// Write implements io.Writer. It parses the log line format:
//
//	"2024-01-01T00:00:00Z [INFO] message key=val\n"
//
// and publishes it as an SSE event. Timestamp and level are extracted from
// the log line prefix; the rest becomes the message.
func (b *LogBroadcaster) Write(p []byte) (int, error) {
	n := len(p)
	line := strings.TrimRight(string(p), "\n\r")
	if line == "" {
		return n, nil
	}

	entry := b.parseLine(line)

	b.mu.Lock()
	// Ring buffer store
	b.buf[b.bufPos] = entry
	b.bufPos = (b.bufPos + 1) % b.maxLen
	if b.bufCount < b.maxLen {
		b.bufCount++
	}
	b.mu.Unlock()

	// Publish to SSE hub (non-blocking)
	if b.hub != nil {
		b.hub.Publish(sse.EventLogEntry, map[string]interface{}{
			"timestamp": entry.Timestamp,
			"level":     entry.Level,
			"message":   entry.Message,
		})
	}

	return n, nil
}

// RecentEntries returns the most recent n log entries from the ring buffer,
// in chronological order (oldest first, newest last). Used for SSE initial replay.
func (b *LogBroadcaster) RecentEntries(n int) []LogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()

	if n <= 0 || n > b.bufCount {
		n = b.bufCount
	}

	result := make([]LogEntry, n)
	// Start n entries before the next write position to get the newest n entries.
	// Adding b.maxLen keeps the modulo operation positive.
	startIdx := (b.bufPos - n + b.maxLen) % b.maxLen
	for i := 0; i < n; i++ {
		idx := (startIdx + i) % b.maxLen
		result[i] = b.buf[idx]
	}
	return result
}

// parseLine extracts timestamp, level, and message from a log line.
// Format: "2024-01-01T00:00:00Z [LEVEL] message..."
// or:     "2024-01-01T00:00:00+02:00 [LEVEL] message..."
func (b *LogBroadcaster) parseLine(line string) LogEntry {
	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     "INFO",
		Message:   line,
	}

	// Try to parse timestamp prefix — find the first space after position 19
	// (minimum RFC3339 is 20 chars "2006-01-02T15:04:05Z", max is 25 with tz offset)
	if len(line) >= 21 && line[10] == 'T' {
		if spaceIdx := strings.IndexByte(line, ' '); spaceIdx > 19 && spaceIdx <= 25 {
			if ts, err := time.Parse(time.RFC3339, line[:spaceIdx]); err == nil {
				entry.Timestamp = ts
				line = line[spaceIdx+1:]
			}
		}
	}

	// Try to parse level: "[LEVEL] "
	if len(line) >= 3 && line[0] == '[' {
		if end := strings.IndexByte(line, ']'); end > 0 && end < 10 {
			entry.Level = line[1:end]
			line = line[end+1:]
			if line != "" && line[0] == ' ' {
				line = line[1:]
			}
		}
	}

	entry.Message = line
	return entry
}
