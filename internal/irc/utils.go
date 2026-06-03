package irc

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

// resolveAllHosts resolves the IRC server hostname to all usable IP addresses.
//
// Strategy:
//  1. Try the system DNS resolver first (fast path, works for most users).
//  2. If the system DNS fails or returns only blocked addresses (0.0.0.0 / ::),
//     fall back to the configured public DNS resolver (default 8.8.8.8:53).
//  3. Merge results from both resolvers, deduplicated, preserving order.
//
// Returns all valid IP addresses found, or ErrServerUnreachable if both
// attempts fail.  The caller should use the returned IPs (not the original
// hostname) as the girc Server address so that a blocked DNS lookup does not
// recur inside the IRC library.
func (c *Client) resolveAllHosts(host string) ([]string, error) {
	// validAddrs filters out blocked sentinel addresses returned by ISP DNS.
	validAddrs := func(addrs []string) []string {
		var out []string
		for _, a := range addrs {
			if a != "0.0.0.0" && a != "::" && a != "" {
				out = append(out, a)
			}
		}
		return out
	}

	// seen tracks already-collected IPs to avoid duplicates.
	seen := make(map[string]bool)
	var allIPs []string
	addUnique := func(ips []string) {
		for _, ip := range ips {
			if !seen[ip] {
				seen[ip] = true
				allIPs = append(allIPs, ip)
			}
		}
	}

	// --- Attempt 1: system resolver ---
	addrs, err := net.LookupHost(host)
	if err == nil {
		if valid := validAddrs(addrs); len(valid) > 0 {
			c.debugf("DNS resolved %s → %v (system)", host, valid)
			addUnique(valid)
		} else {
			c.noticef("System DNS returned blocked address for %s: %v — trying fallback DNS", host, addrs)
		}
	} else {
		c.noticef("System DNS failed for %s: %v — trying fallback DNS (%s)", host, err, c.opts.DNSServer)
	}

	// --- Attempt 2: public DNS resolver (always tried to collect extra IPs) ---
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			// Use UDP for the DNS query; fall back to TCP if UDP is blocked.
			conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "udp", c.opts.DNSServer)
			if err != nil {
				return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", c.opts.DNSServer)
			}
			return conn, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err = resolver.LookupHost(ctx, host)
	if err == nil {
		if valid := validAddrs(addrs); len(valid) > 0 {
			newCount := 0
			for _, ip := range valid {
				if !seen[ip] {
					newCount++
				}
			}
			if newCount > 0 {
				c.debugf("Fallback DNS (%s) added %d new IP(s) for %s", c.opts.DNSServer, newCount, host)
			}
			addUnique(valid)
		}
	} else if len(allIPs) == 0 {
		c.noticef("Fallback DNS (%s) also failed for %s: %v", c.opts.DNSServer, host, err)
	}

	if len(allIPs) == 0 {
		return nil, fmt.Errorf("%w: cannot resolve %s (system and %s both failed)",
			ErrServerUnreachable, host, c.opts.DNSServer)
	}

	c.debugf("Resolved %s to %d IP(s): %v", host, len(allIPs), allIPs)
	return allIPs, nil
}

// NormalizeChannel lowercases and ensures a leading '#'.
// Returns empty string if input is empty.
func NormalizeChannel(ch string) string {
	ch = strings.ToLower(strings.TrimSpace(ch))
	if ch != "" && !strings.HasPrefix(ch, "#") {
		ch = "#" + ch
	}
	return ch
}

func isConnectError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, k := range []string{
		"connection refused", "no route to host", "network is unreachable",
		"i/o timeout", "no such host", "dial ",
	} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

func randomUsername() string {
	firstNames := []string{"Alice", "Bob", "Charlie", "Dave", "Eve", "Frank", "Grace", "Hank"}
	lastNames := []string{"Smith", "Jones", "Brown", "Wilson", "Taylor", "Davis", "Clark", "Lewis"}
	n1, _ := rand.Int(rand.Reader, big.NewInt(int64(len(firstNames))))
	n2, _ := rand.Int(rand.Reader, big.NewInt(int64(len(lastNames))))
	num, _ := rand.Int(rand.Reader, big.NewInt(90))
	return fmt.Sprintf("%s%s%d%s",
		firstNames[n1.Int64()], lastNames[n2.Int64()], num.Int64()+10, randomSuffix(3))
}

func randomSuffix(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[idx.Int64()]
	}
	return string(b)
}

func formatDuration(d time.Duration) string {
	if d < 60*time.Second {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm %ds", m, s)
}

// formatSpeed formats a transfer speed given in bytes/s as a human-readable string.
// Values below 1 MB/s are shown as KB/s; values >= 1 MB/s are shown as MB/s.
func formatSpeed(bytesPerSec float64) string {
	speedKB := bytesPerSec / 1024
	if speedKB >= 1024 {
		return fmt.Sprintf("%.2f MB/s", speedKB/1024)
	}
	return fmt.Sprintf("%.1f KB/s", speedKB)
}

func ipNumToQuad(ipNum string) string {
	n := parseU32(ipNum)
	return fmt.Sprintf("%d.%d.%d.%d",
		(n>>24)&0xFF, (n>>16)&0xFF, (n>>8)&0xFF, n&0xFF)
}

func parseI64(s string) int64 {
	var v int64
	_, _ = fmt.Sscanf(s, "%d", &v)
	return v
}

func parseU32(s string) uint32 {
	var v uint32
	_, _ = fmt.Sscanf(s, "%d", &v)
	return v
}

func randN(n int) int {
	r, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(r.Int64())
}

// splitDCC splits a DCC message text, respecting quoted filenames.
func splitDCC(s string) []string {
	var parts []string
	s = strings.TrimSpace(s)
	for s != "" {
		if s[0] == '"' {
			end := strings.Index(s[1:], "\"")
			if end < 0 {
				parts = append(parts, s[1:])
				break
			}
			parts = append(parts, s[1:end+1])
			s = strings.TrimSpace(s[end+2:])
		} else {
			sp := strings.IndexByte(s, ' ')
			if sp < 0 {
				parts = append(parts, s)
				break
			}
			parts = append(parts, s[:sp])
			s = strings.TrimSpace(s[sp+1:])
		}
	}
	return parts
}
