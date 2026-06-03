package entities

import (
	"net"
	"strconv"
	"strings"
)

// IrcServer models an IRC server connection target.
type IrcServer struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// NewIrcServer creates a new IrcServer with the default IRC port 6667.
func NewIrcServer(address string) IrcServer {
	return IrcServer{Address: address, Port: 6667}
}

// NewIrcServerWithPort creates a new IrcServer with a specific port.
func NewIrcServerWithPort(address string, port int) IrcServer {
	return IrcServer{Address: address, Port: port}
}

// ParseIrcServer parses a server string which may be "host", "host:port",
// "ip", or "ip:port". If no port is specified, defaults to 6667.
func ParseIrcServer(s string) IrcServer {
	// net.SplitHostPort handles IPv6 [::1]:port syntax too
	host, portStr, err := splitHostPort(s)
	if err != nil || portStr == "" {
		return IrcServer{Address: s, Port: 6667}
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return IrcServer{Address: s, Port: 6667}
	}
	return IrcServer{Address: host, Port: port}
}

// splitHostPort splits "host:port" but also accepts bare "host" (no port).
// It handles IPv6 addresses correctly: bare [::1] or bracketed [::1]:port.
func splitHostPort(s string) (host, port string, err error) {
	// If it contains "[" it's IPv6 — always use net.SplitHostPort
	if strings.Contains(s, "[") {
		host, port, err = net.SplitHostPort(s)
		return
	}
	// Count colons: 0 or 1 → hostname or host:port; >1 → bare IPv6
	colons := strings.Count(s, ":")
	if colons == 0 {
		return s, "", nil
	}
	if colons == 1 {
		host, port, err = net.SplitHostPort(s)
		return
	}
	// Multiple colons → bare IPv6 address (no port)
	return s, "", nil
}
