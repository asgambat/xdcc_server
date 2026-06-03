package entities

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var xdccMsgRegex = regexp.MustCompile(`^/msg [^ ]+ xdcc send #[0-9]+((,[0-9]+)*|(-[0-9]+(;[0-9]+)?)?)$`)

// ParseXDCCMessage parses an XDCC message and returns one XDCCPack per requested pack number.
// Accepted formats:
//
//	/msg <bot> xdcc send #42        → single pack
//	/msg <bot> xdcc send #1,3,5     → comma-separated list
//	/msg <bot> xdcc send #1-10      → inclusive range
//	/msg <bot> xdcc send #1-10;2    → range with step
//
// server defaults to "irc.rizon.net" if empty; directory defaults to ".".
func ParseXDCCMessage(msg, directory, server string) ([]*XDCCPack, error) {
	if server == "" {
		server = "irc.rizon.net"
	}
	if directory == "" {
		directory = "."
	}

	if !xdccMsgRegex.MatchString(msg) {
		return nil, fmt.Errorf("invalid XDCC message: %s", msg)
	}

	bot := extractBot(msg)
	ircServer := ResolveServer(bot, server)

	// Everything after the last "#" is the pack specification.
	packPart := msg[strings.LastIndex(msg, "#")+1:]

	return parsePackNums(packPart, ircServer, bot, directory)
}

// extractBot returns the bot name from a validated /msg line.
// Input format: "/msg <bot> xdcc send ..."
func extractBot(msg string) string {
	afterMsg := strings.TrimPrefix(msg, "/msg ")
	return strings.SplitN(afterMsg, " ", 2)[0]
}

// parsePackNums dispatches pack parsing to the right strategy based on syntax:
// comma list, range with optional step, or plain single number.
func parsePackNums(packPart string, srv IrcServer, bot, directory string) ([]*XDCCPack, error) {
	switch {
	case strings.Contains(packPart, ","):
		return parseCommaSeparated(packPart, srv, bot, directory)
	case strings.Contains(packPart, "-"):
		return parseRange(packPart, srv, bot, directory)
	default:
		p, err := parseSingle(packPart, srv, bot, directory)
		if err != nil {
			return nil, err
		}
		return []*XDCCPack{p}, nil
	}
}

// parseCommaSeparated handles "#1,3,5" — each token is an individual pack number.
func parseCommaSeparated(packPart string, srv IrcServer, bot, directory string) ([]*XDCCPack, error) {
	var packs []*XDCCPack
	for _, n := range strings.Split(packPart, ",") {
		num, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return nil, fmt.Errorf("invalid pack number: %s", n)
		}
		packs = append(packs, newPack(srv, bot, directory, num))
	}
	return packs, nil
}

// parseRange handles "#start-end" and "#start-end;step".
// step defaults to 1 when omitted or invalid (< 1).
func parseRange(packPart string, srv IrcServer, bot, directory string) ([]*XDCCPack, error) {
	step := 1
	rangeStr := packPart

	// Optional ";step" suffix.
	if strings.Contains(rangeStr, ";") {
		parts := strings.SplitN(rangeStr, ";", 2)
		rangeStr = parts[0]
		if s, err := strconv.Atoi(parts[1]); err == nil && s >= 1 {
			step = s
		}
	}

	parts := strings.SplitN(rangeStr, "-", 2)
	start, err1 := strconv.Atoi(parts[0])
	end, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("invalid pack range: %s", packPart)
	}
	if start <= 0 || end <= 0 {
		return nil, fmt.Errorf("invalid pack range: numbers must be positive: %s", packPart)
	}
	if start > end {
		return nil, fmt.Errorf("invalid pack range: start > end: %s", packPart)
	}

	var packs []*XDCCPack
	for i := start; i <= end; i += step {
		packs = append(packs, newPack(srv, bot, directory, i))
	}
	return packs, nil
}

// parseSingle handles a plain pack number with no separator.
func parseSingle(packPart string, srv IrcServer, bot, directory string) (*XDCCPack, error) {
	num, err := strconv.Atoi(packPart)
	if err != nil {
		return nil, fmt.Errorf("invalid pack number: %s", packPart)
	}
	return newPack(srv, bot, directory, num), nil
}

// newPack creates an XDCCPack and sets its download directory.
func newPack(srv IrcServer, bot, directory string, num int) *XDCCPack {
	p := NewXDCCPack(srv, bot, num)
	p.SetDirectory(directory)
	return p
}

// ResolveServer returns the appropriate IrcServer for the given bot name.
//
// IMPORTANT RULE — Bot-prefix → server mapping:
//   - Bots whose name starts with "TLT" MUST connect to irc.williamgattone.it
//   - Bots whose name starts with "WeC" MUST connect to irc.explosionirc.net
//
// This mapping is ALWAYS applied regardless of what server the search engine
// returned. The only way to override it is by passing --server explicitly on
// the command line; that override is handled at the CLI level AFTER this
// function runs (see xdcc-dl, xdcc-browse main.go).
//
// Do NOT add conditions that skip these mappings (e.g. checking defaultServer).
func ResolveServer(bot, fallbackServer string) IrcServer {
	switch {
	case strings.HasPrefix(bot, "TLT"):
		return NewIrcServer("irc.williamgattone.it")
	case strings.HasPrefix(bot, "WeC"):
		return NewIrcServer("irc.explosionirc.net")
	default:
		if fallbackServer == "" {
			fallbackServer = "irc.rizon.net"
		}
		return ParseIrcServer(fallbackServer)
	}
}

// ResolveChannel returns the canonical channel hint for a given bot.
// It uses the same prefix rules as ResolveServer so that TLT bots get
// #tlt@XDCC|Bots|Channel and WeC bots get #WeC@XDCC. For all other bots
// an empty string is returned, meaning the channel should be discovered
// via WHOIS.
func ResolveChannel(bot string) string {
	switch {
	case strings.HasPrefix(bot, "TLT"):
		return "#tlt@XDCC|Bots|Channel"
	case strings.HasPrefix(bot, "WeC"):
		return "#WeC@XDCC"
	default:
		return ""
	}
}

// PreparePacks applies output path and server overrides to a list of packs.
//
// Bot-prefix → server mapping (TLT→williamgattone, WeC→explosionirc) is
// always applied here via ResolveServer. This is critical for packs coming
// from search engines that may report a generic server (e.g. irc.rizon.net).
// The only override is the explicit --server CLI flag, handled by the caller
// AFTER this function returns.
//
// If location is an existing directory, it is used as the download directory.
// If location is set but is not a directory and there is only one pack, it
// overrides the filename; for multiple packs, it appends a zero-padded index.
func PreparePacks(packs []*XDCCPack, location string) {
	// Apply server overrides based on bot name.
	// This is intentionally kept even though ParseXDCCMessage also calls
	// ResolveServer, because PreparePacks is also used for packs originating
	// from search engines (e.g. xdcc-browse) that bypass ParseXDCCMessage.
	for _, p := range packs {
		p.Server = ResolveServer(p.Bot, p.Server.Address)
	}

	if location == "" {
		return
	}

	// If location is an existing directory, use it as the download directory.
	if fi, err := os.Stat(location); err == nil && fi.IsDir() {
		for _, p := range packs {
			p.SetDirectory(location)
		}
		return
	}

	// Otherwise treat location as a filename/path.
	if len(packs) == 1 {
		packs[0].SetFilename(location, true)
	} else {
		for i, p := range packs {
			p.SetFilename(fmt.Sprintf("%s-%03d", location, i), true)
		}
	}
}

// ByteStringToByteCount converts a human-readable byte string (e.g. "1.5 MB") to bytes.
func ByteStringToByteCount(s string) int64 {
	s = strings.TrimSpace(s)
	upper := strings.ToUpper(s)
	// Check longer suffixes first to avoid ambiguity (e.g., "KB" before "B").
	units := []struct {
		suffix string
		mult   int64
	}{
		{"GB", 1024 * 1024 * 1024},
		{"MB", 1024 * 1024},
		{"KB", 1024},
		{"G", 1024 * 1024 * 1024},
		{"M", 1024 * 1024},
		{"K", 1024},
		{"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(upper, u.suffix) {
			numStr := strings.TrimSpace(s[:len(s)-len(u.suffix)])
			val, err := strconv.ParseFloat(numStr, 64)
			if err == nil {
				return int64(val * float64(u.mult))
			}
		}
	}
	// Try plain number
	val, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err == nil {
		return val
	}
	return 0
}

// ParseThrottle converts a throttle string (e.g. "50M", "100K") to bytes per second.
// Returns -1 if throttle is disabled (empty or "0" or "-1").
func ParseThrottle(s string) (int64, error) {
	if s == "" || s == "-1" || s == "0" {
		return -1, nil
	}
	upper := strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(upper, "G"):
		mult = 1024 * 1024 * 1024
		upper = upper[:len(upper)-1]
	case strings.HasSuffix(upper, "M"):
		mult = 1024 * 1024
		upper = upper[:len(upper)-1]
	case strings.HasSuffix(upper, "K"):
		mult = 1024
		upper = upper[:len(upper)-1]
	}
	val, err := strconv.ParseFloat(upper, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid throttle value: %s", s)
	}
	return int64(val * float64(mult)), nil
}
