// Package downloader orchestrates downloading multiple XDCC packs sequentially.
// Packs that share the same IRC server are downloaded on a single connection.
package downloader

import (
	"context"
	"errors"
	"fmt"

	"xdcc-go/internal/entities"
	xdccirc "xdcc-go/internal/irc"
)

// Options configures the download session.
type Options struct {
	ConnectTimeout   int
	StallTimeout     int
	FallbackChannel  string
	ThrottleBytes    int64
	WaitTime         int
	Username         string
	ChannelJoinDelay int
	Verbosity        int            // 0=normal, 1=verbose (-v), 2=debug (-vv), -1=quiet
	DNSServer        string         // fallback DNS resolver (host:port); empty = use default
	Logger           xdccirc.Logger // custom logger; nil = default
}

// DownloadPacks downloads all packs sequentially.
// Consecutive packs that share the same IRC server address reuse a single
// connection; the client joins channels as needed and stays in channels
// already joined so they do not need to be rejoined for later packs.
func DownloadPacks(ctx context.Context, packs []*entities.XDCCPack, opts Options) {
	ircOpts := xdccirc.DownloadOptions{
		ConnectTimeout:   opts.ConnectTimeout,
		StallTimeout:     opts.StallTimeout,
		FallbackChannel:  opts.FallbackChannel,
		ThrottleBytes:    opts.ThrottleBytes,
		WaitTime:         opts.WaitTime,
		Username:         opts.Username,
		ChannelJoinDelay: opts.ChannelJoinDelay,
		DNSServer:        opts.DNSServer,
		Logger:           opts.Logger,
	}

	for _, group := range groupByServer(packs) {
		client := xdccirc.NewClient(ctx, group, ircOpts, opts.Verbosity)
		results := client.DownloadAll()
		for i, pack := range group {
			printResult(pack, results[i])
		}
	}
}

// groupByServer splits packs into groups of consecutive packs sharing the
// same IRC server address. Order within each group is preserved.
func groupByServer(packs []*entities.XDCCPack) [][]*entities.XDCCPack {
	if len(packs) == 0 {
		return nil
	}
	var groups [][]*entities.XDCCPack
	current := []*entities.XDCCPack{packs[0]}
	for _, p := range packs[1:] {
		if p.Server.Address == current[0].Server.Address {
			current = append(current, p)
		} else {
			groups = append(groups, current)
			current = []*entities.XDCCPack{p}
		}
	}
	groups = append(groups, current)
	return groups
}

func printResult(pack *entities.XDCCPack, r xdccirc.PackResult) {
	if r.Error == nil {
		return // success already printed by the client
	}
	switch {
	case errors.Is(r.Error, xdccirc.ErrAlreadyDownloaded):
		fmt.Printf("File already downloaded (skipping): %s\n", pack.Filename)
	case errors.Is(r.Error, xdccirc.ErrBotDenied):
		if r.LastBotNotice != "" {
			fmt.Printf("Bot denied XDCC request: %s\n", r.LastBotNotice)
		} else {
			fmt.Printf("Bot denied XDCC request for: %s\n", pack.Filename)
		}
	case errors.Is(r.Error, xdccirc.ErrBotNotFound):
		fmt.Printf("Bot %s not found on server %s\n", pack.Bot, pack.Server.Address)
	case errors.Is(r.Error, xdccirc.ErrServerUnreachable):
		fmt.Printf("Server unreachable (%s): %v\n", pack.Server.Address, r.Error)
		fmt.Println("Tip: use --server to override the IRC server address.")
	case errors.Is(r.Error, xdccirc.ErrUnrecoverable):
		fmt.Println("Unrecoverable error (IP banned?). Aborting.")
	case errors.Is(r.Error, xdccirc.ErrCancelled):
		fmt.Println("Download cancelled.")
	case errors.Is(r.Error, xdccirc.ErrTimeout):
		fmt.Printf("Download of pack #%d timed out after all retries\n", pack.PackNumber)
	case errors.Is(r.Error, xdccirc.ErrDownloadFailed):
		fmt.Printf("Download of %s failed after all retries\n", pack.Filename)
	default:
		fmt.Printf("Error downloading pack %d: %v\n", pack.PackNumber, r.Error)
	}
}
