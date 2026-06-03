package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"xdcc-go/internal/entities"
	"xdcc-go/internal/search"
)

func main() {
	var (
		engineName   string
		verbosity    int
		compact      bool
		prefixFilter bool
	)

	cmd := &cobra.Command{
		Use:   "xdcc-search <search_term> [engine]",
		Short: "Search for XDCC packs and print download commands",
		Long: `xdcc-search queries an XDCC search engine and prints one result per line
with the corresponding xdcc-dl command ready to copy-paste.

The engine argument is optional; default is xdcc-eu.
Available engines: ` + strings.Join(search.AvailableEngines(), ", ") + `

Output format per result:
  <filename> [<size>] (xdcc-dl "<message>" [--server <host>])

Filters:
  --compact  remove duplicate results with same filename, size and bot family
  --prefix   keep only files whose name starts with the search term (case-insensitive)

Verbosity levels:
  (default)  results only
  -v         also show search engine debug info (e.g. HTTP requests)`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			term := args[0]
			if len(args) == 2 {
				engineName = args[1]
			}

			engine := search.EngineByName(engineName, verbosity >= 1)
			if engine == nil {
				return fmt.Errorf("unknown search engine %q. Available: %s",
					engineName, strings.Join(search.AvailableEngines(), ", "))
			}

			results, err := engine.Search(context.Background(), term)
			if err != nil {
				return fmt.Errorf("search failed: %w", err)
			}

			// Filter by filename prefix if requested
			if prefixFilter {
				results = filterByPrefix(results, term)
			}

			if compact {
				before := len(results)
				results = entities.CompactPacks(results)
				if len(results) < before {
					fmt.Fprintf(os.Stderr, "Compact: %d results reduced to %d\n", before, len(results))
				}
			}

			// Apply bot-prefix → server mapping (TLT→williamgattone, WeC→explosionirc)
			// so the printed commands show the correct --server flag.
			entities.PreparePacks(results, "")

			if len(results) == 0 {
				fmt.Fprintln(os.Stderr, "No results found.")
				return nil
			}

			for _, pack := range results {
				msg := pack.GetRequestMessage(true)
				line := fmt.Sprintf("%s [%s] (xdcc-dl %q)",
					pack.Filename,
					entities.HumanReadableBytes(pack.Size),
					msg)
				if pack.Server.Address != "irc.rizon.net" {
					line = fmt.Sprintf("%s [%s] (xdcc-dl %q --server %s)",
						pack.Filename,
						entities.HumanReadableBytes(pack.Size),
						msg,
						pack.Server.Address)
				}
				fmt.Println(line)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&engineName, "search-engine", "e", "xdcc-eu",
		"Search engine to use: nibl, xdcc-eu, subsplease. Can also be passed as second positional argument")
	cmd.Flags().CountVarP(&verbosity, "verbose", "v", "Increase verbosity: -v shows search engine debug info")
	cmd.Flags().BoolVarP(&compact, "compact", "c", false,
		"Remove duplicate results with same filename, size and bot family")
	cmd.Flags().BoolVarP(&prefixFilter, "prefix", "p", false,
		"Keep only results whose filename starts with the search term (case-insensitive)")

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// filterByPrefix returns only packs whose filename starts with the given term (case-insensitive).
func filterByPrefix(packs []*entities.XDCCPack, term string) []*entities.XDCCPack {
	prefix := strings.ToLower(term)
	var out []*entities.XDCCPack
	for _, p := range packs {
		if strings.HasPrefix(strings.ToLower(p.Filename), prefix) {
			out = append(out, p)
		}
	}
	return out
}
