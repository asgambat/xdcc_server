package entities

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// SafeJoin
// ---------------------------------------------------------------------------

func TestSafeJoin(t *testing.T) {
	tests := []struct {
		base, name string
		want       string
		wantErr    bool
	}{
		{"/downloads", "file.mkv", "/downloads/file.mkv", false},
		{"/downloads", "subdir/file.mkv", "/downloads/file.mkv", false}, // filepath.Base strips dir
		{"/downloads", "../escape.mkv", "/downloads/escape.mkv", false}, // filepath.Base strips ../
		{"/downloads", "", "", true},
		{"/downloads", ".", "", true},
		{"/downloads", "  file.mkv  ", "/downloads/file.mkv", false},
	}
	for _, tt := range tests {
		got, err := SafeJoin(tt.base, tt.name)
		if tt.wantErr && err == nil {
			t.Errorf("SafeJoin(%q, %q): expected error, got %q", tt.base, tt.name, got)
		}
		if !tt.wantErr && err != nil {
			t.Errorf("SafeJoin(%q, %q): unexpected error: %v", tt.base, tt.name, err)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("SafeJoin(%q, %q) = %q, want %q", tt.base, tt.name, got, tt.want)
		}
	}
}

func TestSafeJoin_TempDir(t *testing.T) {
	dir := t.TempDir()
	path, err := SafeJoin(dir, "test.mkv")
	if err != nil {
		t.Fatalf("SafeJoin error: %v", err)
	}
	if path != filepath.Join(dir, "test.mkv") {
		t.Errorf("got %q, want %q", path, filepath.Join(dir, "test.mkv"))
	}

	// Verify the file can be created
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("cannot create file: %v", err)
	}
	f.Close()
}

// ---------------------------------------------------------------------------
// cleanFilename
// ---------------------------------------------------------------------------

func TestCleanFilename(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"file.mkv", "file.mkv"},
		{"../escape.mkv", "escape.mkv"},
		{"/absolute/path/file.mkv", "file.mkv"},
		{"", "unknown"},
		{".", "unknown"},
		{"   file.mkv   ", "file.mkv"},
		{"subdir/../file.mkv", "file.mkv"}, // filepath.Base resolves ".."
	}
	for _, tt := range tests {
		got := cleanFilename(tt.in)
		if got != tt.want {
			t.Errorf("cleanFilename(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// ExtractPackNumber
// ---------------------------------------------------------------------------

func TestExtractPackNumber(t *testing.T) {
	tests := []struct {
		msg  string
		want int
	}{
		{"xdcc send #42", 42},
		{"/msg Bot xdcc send #42", 42},
		{"xdcc send #1", 1},
		{"xdcc send #999", 999},
		{"no hash", 0},
		{"", 0},
		{"#", 0},
		{"#abc", 0},
		{"#42abc", 42}, // stops at non-digit
	}
	for _, tt := range tests {
		got := ExtractPackNumber(tt.msg)
		if got != tt.want {
			t.Errorf("ExtractPackNumber(%q) = %d, want %d", tt.msg, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// HumanReadableBytes
// ---------------------------------------------------------------------------

func TestHumanReadableBytes(t *testing.T) {
	tests := []struct {
		b    int64
		want string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{2 * 1024 * 1024 * 1024, "2.0 GB"},
	}
	for _, tt := range tests {
		got := HumanReadableBytes(tt.b)
		if got != tt.want {
			t.Errorf("HumanReadableBytes(%d) = %q, want %q", tt.b, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// GetRequestMessage
// ---------------------------------------------------------------------------

func TestGetRequestMessage(t *testing.T) {
	p := NewXDCCPack(NewIrcServer("irc.test.com"), "TestBot", 42)

	short := p.GetRequestMessage(false)
	if short != "xdcc send #42" {
		t.Errorf("GetRequestMessage(false) = %q, want xdcc send #42", short)
	}

	full := p.GetRequestMessage(true)
	if full != "/msg TestBot xdcc send #42" {
		t.Errorf("GetRequestMessage(true) = %q, want /msg TestBot xdcc send #42", full)
	}
}

// ---------------------------------------------------------------------------
// SetFilename edge cases
// ---------------------------------------------------------------------------

func TestSetFilenameOverride(t *testing.T) {
	p := NewXDCCPack(NewIrcServer("irc.test.com"), "Bot", 1)
	p.SetFilename("first.mkv", true)
	if p.GetFilename() != "first.mkv" {
		t.Errorf("expected first.mkv, got %s", p.GetFilename())
	}

	// Override with new name
	p.SetFilename("second.mp4", true)
	if p.GetFilename() != "second.mp4" {
		t.Errorf("expected second.mp4 after override, got %s", p.GetFilename())
	}
}

func TestSetFilenameNoOverride(t *testing.T) {
	p := NewXDCCPack(NewIrcServer("irc.test.com"), "Bot", 1)
	p.SetFilename("video.mkv", true)

	// override=false appends new extension when it differs
	p.SetFilename("other.mp4", false)
	if p.GetFilename() != "video.mkv.mp4" {
		t.Errorf("expected video.mkv.mp4 (extension updated), got %s", p.GetFilename())
	}

	// Extension-only update: replace .mkv with .avi
	p.SetFilename("video.avi", true)
	if p.GetFilename() != "video.avi" {
		t.Errorf("expected video.avi after override, got %s", p.GetFilename())
	}
}

func TestSetFilenamePathTraversal(t *testing.T) {
	p := NewXDCCPack(NewIrcServer("irc.test.com"), "Bot", 1)

	// Attempt path traversal — should be sanitized
	p.SetFilename("../../../etc/passwd", true)
	if p.GetFilename() == "../../../etc/passwd" {
		t.Error("path traversal was not sanitized")
	}
	// filepath.Base would return "passwd", cleanFilename rejects ".." → "unknown"
	if p.GetFilename() == "passwd" {
		t.Log("filepath.Base stripped traversal, got passwd")
	}
}

// ---------------------------------------------------------------------------
// SetDirectory / GetDirectory
// ---------------------------------------------------------------------------

func TestSetDirectory(t *testing.T) {
	p := NewXDCCPack(NewIrcServer("irc.test.com"), "Bot", 1)
	p.SetDirectory("/downloads")
	if p.GetDirectory() != "/downloads" {
		t.Errorf("expected /downloads, got %s", p.GetDirectory())
	}
}

// ---------------------------------------------------------------------------
// GetFilepath
// ---------------------------------------------------------------------------

func TestGetFilepath(t *testing.T) {
	tests := []struct {
		dir, filename string
		want          string
	}{
		{"", "file.mkv", "file.mkv"},
		{".", "file.mkv", "file.mkv"},
		{"/downloads", "file.mkv", "/downloads/file.mkv"},
	}
	for _, tt := range tests {
		p := NewXDCCPack(NewIrcServer("irc.test.com"), "Bot", 1)
		p.SetDirectory(tt.dir)
		p.SetFilename(tt.filename, true)
		got := p.GetFilepath()
		if got != tt.want {
			t.Errorf("GetFilepath(dir=%q, file=%q) = %q, want %q",
				tt.dir, tt.filename, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// String()
// ---------------------------------------------------------------------------

func TestString(t *testing.T) {
	p := NewXDCCPack(NewIrcServer("irc.test.com"), "TestBot", 42)
	p.SetFilename("video.mkv", true)
	p.SetSize(123456789)

	s := p.String()
	if s == "" {
		t.Error("String() returned empty")
	}
}
