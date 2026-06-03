package ircmanager

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"
)

// randomSuffix returns a random alphanumeric string of length n.
func randomSuffix(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[idx.Int64()]
	}
	return string(b)
}

// isOwnNick checks if the given nick matches the client's nick (case-sensitive).
// The source may be in nick!user@host format.
func isOwnNick(source, ownNick string) bool {
	// Extract nick from nick!user@host format
	if idx := strings.Index(source, "!"); idx > 0 {
		source = source[:idx]
	}
	return source == ownNick
}

// ServerAddr formats address:port as a string.
func ServerAddr(address string, port int) string {
	return fmt.Sprintf("%s:%d", address, port)
}

// stripIRCFormatting removes IRC formatting control codes from a string.
// IRC uses the following control bytes:
//
//	\x02 — Bold
//	\x03 — Color: \x03 followed by 0-2 digits (fg), optionally [,digits] (bg)
//	\x0F — Reset (plain text)
//	\x16 — Reverse video
//	\x1D — Italic
//	\x1F — Underline
//
// Invalid UTF-8 sequences are also cleaned up, replacing them with U+FFFD.
// Iterates byte-by-byte to avoid allocating an intermediate []rune slice.
func stripIRCFormatting(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	// Ensure the output is valid UTF-8
	s = strings.ToValidUTF8(s, string(utf8.RuneError))

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		switch r {
		case 0x02: // Bold
			i += size
		case 0x03: // Color — skip up to 2 digits for fg + optional "," + digits for bg
			i += size
			for j := 0; j < 2 && i < len(s); {
				r2, s2 := utf8.DecodeRuneInString(s[i:])
				if !isDigit(r2) {
					break
				}
				i += s2
				j++
			}
			if i < len(s) && s[i] == ',' {
				i++
				for j := 0; j < 2 && i < len(s); {
					r2, s2 := utf8.DecodeRuneInString(s[i:])
					if !isDigit(r2) {
						break
					}
					i += s2
					j++
				}
			}
		case 0x0F, 0x16, 0x1D, 0x1F: // Reset, Reverse, Italic, Underline
			i += size
		default:
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}

// isDigit returns true if r is an ASCII digit.
func isDigit(r rune) bool {
	return r >= '0' && r <= '9'
}
