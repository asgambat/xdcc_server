// Package search provides XDCC pack search engine implementations.
package search

import (
	"context"
	"strings"

	"xdcc-go/internal/entities"
)

// Engine is an XDCC pack search engine.
type Engine interface {
	Name() string
	Search(ctx context.Context, term string) ([]*entities.XDCCPack, error)
}

// EngineByName returns the search engine for the given name (case-insensitive).
// verbose enables debug output for engines that support it.
// Returns nil if not found.
func EngineByName(name string, verbose bool) Engine {
	switch strings.ToLower(name) {
	case "nibl":
		return &NiblEngine{}
	case "xdcc-eu":
		return &XdccEuEngine{Verbose: verbose}
	case "subsplease":
		return &SubsPleaseEngine{}
	default:
		return nil
	}
}

// AvailableEngines returns the list of available engine names.
func AvailableEngines() []string {
	return []string{"nibl", "xdcc-eu", "subsplease"}
}

// resolveBaseURL returns override if non-empty, otherwise defaultURL.
// Used by search engines to allow test injection of a local HTTP server.
func resolveBaseURL(override, defaultURL string) string {
	if override != "" {
		return override
	}
	return defaultURL
}
