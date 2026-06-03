// Package cli provides shared utilities for the xdcc command-line tools.
package cli

// VerbosityLevel maps --verbose and --quiet flag counts to a single verbosity
// integer used by the IRC client:
//
//	-qq (quiet>=2) → -2  suppress all output
//	-q  (quiet=1)  → -1  suppress info, keep errors/notices/progress
//	(default)      →  0
//	-v             →  1  show bot notices, channel joins, WHOIS
//	-vv            →  2  full debug
//
// If -q and -v are used together, -q takes precedence and -v is ignored.
func VerbosityLevel(verbose, quiet int) int {
	if quiet >= 2 {
		return -2
	}
	if quiet >= 1 {
		return -1
	}
	return verbose
}
