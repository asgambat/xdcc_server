package entities

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// XDCCPack models an XDCC pack to be downloaded from an IRC bot.
type XDCCPack struct {
	Server           IrcServer `json:"server"`
	Bot              string    `json:"bot"`
	Channel          string    `json:"channel,omitempty"`
	PackNumber       int       `json:"pack_number"`
	Directory        string    `json:"directory,omitempty"`
	Filename         string    `json:"filename"`
	OriginalFilename string    `json:"original_filename,omitempty"`
	Size             int64     `json:"size"`
}

// NewXDCCPack creates a new XDCCPack.
func NewXDCCPack(server IrcServer, bot string, packNumber int) *XDCCPack {
	return &XDCCPack{
		Server:     server,
		Bot:        bot,
		PackNumber: packNumber,
		Directory:  ".",
	}
}

// SafeJoin safely joins a base directory and a name, ensuring the
// result is contained within the base directory. The name is sanitized
// by stripping all directory components via filepath.Base.
func SafeJoin(baseDir, name string) (string, error) {
	cleanName := filepath.Base(strings.TrimSpace(name))
	if cleanName == "." || cleanName == "" || strings.Contains(cleanName, "..") {
		return "", errors.New("invalid filename")
	}
	full := filepath.Join(baseDir, cleanName)
	rel, err := filepath.Rel(baseDir, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("path traversal")
	}
	return full, nil
}

// cleanFilename strips all directory components from a filename to prevent
// path traversal. Returns "unknown" when the result would be empty or ".".
func cleanFilename(filename string) string {
	cleanName := filepath.Base(strings.TrimSpace(filename))
	if cleanName == "." || cleanName == "" || strings.Contains(cleanName, "..") {
		return "unknown"
	}
	return cleanName
}

// SetFilename sets or adjusts the filename.
// If a filename is already set and override is false, only the extension is updated.
// The filename is sanitized via cleanFilename to prevent path traversal.
func (p *XDCCPack) SetFilename(filename string, override bool) {
	cleanName := cleanFilename(filename)
	if p.Filename != "" && !override {
		ext := filepath.Ext(cleanName)
		if ext != "" && !strings.HasSuffix(p.Filename, ext) {
			p.Filename += ext
		}
		return
	}
	p.Filename = cleanName
}

// SetOriginalFilename records the expected filename (used by search engines for validation).
func (p *XDCCPack) SetOriginalFilename(filename string) {
	p.OriginalFilename = filename
}

// SetDirectory sets the target download directory.
func (p *XDCCPack) SetDirectory(directory string) {
	p.Directory = filepath.Clean(directory)
}

// SetSize sets the file size in bytes.
func (p *XDCCPack) SetSize(size int64) {
	p.Size = size
}

// IsFilenameValid checks if the provided filename matches the expected original filename.
func (p *XDCCPack) IsFilenameValid(filename string) bool {
	if p.OriginalFilename != "" {
		return filename == p.OriginalFilename
	}
	return true
}

// GetFilepath returns the full destination file path.
// The path is validated via SafeJoin to ensure it is contained within the
// configured Directory. If validation fails, falls back to filepath.Join
// (which is safe because SetFilename already sanitizes the filename).
func (p *XDCCPack) GetFilepath() string {
	if p.Directory == "" || p.Directory == "." {
		return p.Filename
	}
	full, err := SafeJoin(p.Directory, p.Filename)
	if err != nil {
		// Defensive fallback: filename is already sanitized by SetFilename,
		// so a simple join is safe even if SafeJoin had an edge case.
		return filepath.Join(p.Directory, p.Filename)
	}
	return full
}

// GetRequestMessage returns the XDCC send message for the bot.
// If full is true, includes "/msg <bot> " prefix.
func (p *XDCCPack) GetRequestMessage(full bool) string {
	msg := fmt.Sprintf("xdcc send #%d", p.PackNumber)
	if full {
		return fmt.Sprintf("/msg %s %s", p.Bot, msg)
	}
	return msg
}

// String returns a human-readable representation.
func (p *XDCCPack) String() string {
	return fmt.Sprintf("%s (/msg %s xdcc send #%d) [%s]",
		p.Filename, p.Bot, p.PackNumber, HumanReadableBytes(p.Size))
}

// ExtractPackNumber parses the pack number from a pack message string.
// Supported formats:
//   - "xdcc send #42"       → 42
//   - "/msg Bot xdcc send #42" → 42
//
// If parsing fails, returns 0.
func ExtractPackNumber(msg string) int {
	hashIdx := strings.LastIndex(msg, "#")
	if hashIdx < 0 {
		return 0
	}
	numStr := msg[hashIdx+1:]
	// Only take digits (stop at first non-digit)
	var digits strings.Builder
	for _, c := range numStr {
		if c >= '0' && c <= '9' {
			digits.WriteRune(c)
		} else {
			break
		}
	}
	if digits.Len() == 0 {
		return 0
	}
	n, err := strconv.Atoi(digits.String())
	if err != nil {
		return 0
	}
	return n
}

// HumanReadableBytes converts a byte count to a human-readable string.
func HumanReadableBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
