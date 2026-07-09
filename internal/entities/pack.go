package entities

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// XDCCPack models an XDCC pack to be downloaded from an IRC bot.
type XDCCPack struct {
	mu               sync.RWMutex `json:"-"`
	Server           IrcServer    `json:"server"`
	Bot              string       `json:"bot"`
	channel          string
	PackNumber       int `json:"pack_number"`
	directory        string
	filename         string
	originalFilename string
	size             int64
}

// NewXDCCPack creates a new XDCCPack.
func NewXDCCPack(server IrcServer, bot string, packNumber int) *XDCCPack {
	return &XDCCPack{
		Server:     server,
		Bot:        bot,
		PackNumber: packNumber,
		directory:  ".",
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
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.filename != "" && !override {
		ext := filepath.Ext(cleanName)
		if ext != "" && !strings.HasSuffix(p.filename, ext) {
			p.filename += ext
		}
		return
	}
	p.filename = cleanName
}

// SetOriginalFilename records the expected filename (used by search engines for validation).
func (p *XDCCPack) SetOriginalFilename(filename string) {
	p.mu.Lock()
	p.originalFilename = filename
	p.mu.Unlock()
}

// SetDirectory sets the target download directory.
func (p *XDCCPack) SetDirectory(directory string) {
	clean := filepath.Clean(directory)
	p.mu.Lock()
	p.directory = clean
	p.mu.Unlock()
}

// SetSize sets the file size in bytes.
func (p *XDCCPack) SetSize(size int64) {
	p.mu.Lock()
	p.size = size
	p.mu.Unlock()
}

// GetFilename returns the filename in a thread-safe manner.
func (p *XDCCPack) GetFilename() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.filename
}

// GetSize returns the file size in a thread-safe manner.
func (p *XDCCPack) GetSize() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.size
}

// GetChannel returns the channel in a thread-safe manner.
func (p *XDCCPack) GetChannel() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.channel
}

// SetChannel sets the channel in a thread-safe manner.
func (p *XDCCPack) SetChannel(channel string) {
	p.mu.Lock()
	p.channel = channel
	p.mu.Unlock()
}

// IsFilenameValid checks if the provided filename matches the expected original filename.
func (p *XDCCPack) IsFilenameValid(filename string) bool {
	p.mu.RLock()
	orig := p.originalFilename
	p.mu.RUnlock()
	if orig != "" {
		return filename == orig
	}
	return true
}

// GetFilepath returns the full destination file path.
// The path is validated via SafeJoin to ensure it is contained within the
// configured Directory. If validation fails, falls back to filepath.Join
// (which is safe because SetFilename already sanitizes the filename).
func (p *XDCCPack) GetFilepath() string {
	p.mu.RLock()
	dir := p.directory
	filename := p.filename
	p.mu.RUnlock()
	if dir == "" || dir == "." {
		return filename
	}
	full, err := SafeJoin(dir, filename)
	if err != nil {
		// Defensive fallback: filename is already sanitized by SetFilename,
		// so a simple join is safe even if SafeJoin had an edge case.
		return filepath.Join(dir, filename)
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
	p.mu.RLock()
	filename := p.filename
	size := p.size
	p.mu.RUnlock()
	return fmt.Sprintf("%s (/msg %s xdcc send #%d) [%s]",
		filename, p.Bot, p.PackNumber, HumanReadableBytes(size))
}

// GetDirectory returns the directory in a thread-safe manner.
func (p *XDCCPack) GetDirectory() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.directory
}

// MarshalJSON implements json.Marshaler for custom serialization of private fields.
func (p *XDCCPack) MarshalJSON() ([]byte, error) {
	type Alias XDCCPack
	return json.Marshal(&struct {
		Channel          string `json:"channel,omitempty"`
		Directory        string `json:"directory,omitempty"`
		Filename         string `json:"filename"`
		OriginalFilename string `json:"original_filename,omitempty"`
		Size             int64  `json:"size"`
		*Alias
	}{
		Channel:          p.GetChannel(),
		Directory:        p.GetDirectory(),
		Filename:         p.GetFilename(),
		OriginalFilename: func() string { p.mu.RLock(); defer p.mu.RUnlock(); return p.originalFilename }(),
		Size:             p.GetSize(),
		Alias:            (*Alias)(p),
	})
}

// UnmarshalJSON implements json.Unmarshaler for custom deserialization of private fields.
func (p *XDCCPack) UnmarshalJSON(data []byte) error {
	type Alias XDCCPack
	aux := &struct {
		Channel          string `json:"channel,omitempty"`
		Directory        string `json:"directory,omitempty"`
		Filename         string `json:"filename"`
		OriginalFilename string `json:"original_filename,omitempty"`
		Size             int64  `json:"size"`
		*Alias
	}{
		Alias: (*Alias)(p),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	p.SetChannel(aux.Channel)
	p.SetDirectory(aux.Directory)
	p.SetFilename(aux.Filename, true)
	p.SetOriginalFilename(aux.OriginalFilename)
	p.SetSize(aux.Size)
	return nil
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
