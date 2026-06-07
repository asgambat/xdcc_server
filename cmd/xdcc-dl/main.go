package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"xdcc_server/internal/cli"
	"xdcc_server/internal/client"
	"xdcc_server/internal/downloader"
	"xdcc_server/internal/entities"
	"xdcc_server/internal/store"
)

func main() {
	var (
		server           string
		out              string
		throttle         string
		connectTimeout   int
		stallTimeout     int
		fallbackChannel  string
		waitTime         int
		username         string
		channelJoinDelay int
		verbosity        int
		quietLevel       int
		dnsServer        string
		commandServer    string
	)

	cmd := &cobra.Command{
		Use:   "xdcc-dl <message>",
		Short: "Download a file via XDCC IRC protocol",
		Long: `xdcc-dl downloads files from IRC bots using the XDCC protocol.

The message must be in the format:  /msg <bot> xdcc send #<pack>
Pack number supports ranges, steps, and comma lists:
  /msg MyBot xdcc send #5        single pack
  /msg MyBot xdcc send #1-10     packs 1 through 10
  /msg MyBot xdcc send #1-10;2   packs 1,3,5,7,9 (every 2nd)
  /msg MyBot xdcc send #1,3,7    specific packs

The IRC server is detected automatically from the bot name prefix when
possible. Use --server to override with an explicit address.

With --command-server, the download is delegated to a remote xdcc-server
instance instead of being performed locally.

Verbosity levels:
  (default)  show connection and download progress
  -v         also show bot notices, channel joins, WHOIS results
  -vv        full debug (DNS, DCC internals, all IRC events)
  -q         hide connection info; show only errors, bot notices and progress
  -qq        suppress all output

If -q and -v are used together, -q takes precedence and -v is ignored.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			message := args[0]

			// ── Command-server mode ──────────────────────────────────────
			if commandServer != "" {
				return runWithServer(commandServer, message, out)
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			packs, err := entities.ParseXDCCMessage(message, ".", server)
			if err != nil {
				return fmt.Errorf("invalid XDCC message: %w", err)
			}
			if len(packs) == 0 {
				return fmt.Errorf("no packs found in message: %s", message)
			}

			entities.PreparePacks(packs, out)

			// Explicit --server overrides bot-prefix auto-detection
			if cmd.Flags().Changed("server") {
				srv := entities.ParseIrcServer(server)
				for _, p := range packs {
					p.Server = srv
				}
			}

			throttleBytes, err := entities.ParseThrottle(throttle)
			if err != nil {
				return fmt.Errorf("invalid throttle value %q: %w", throttle, err)
			}

			downloader.DownloadPacks(ctx, packs, downloader.Options{
				ConnectTimeout:   connectTimeout,
				StallTimeout:     stallTimeout,
				FallbackChannel:  fallbackChannel,
				ThrottleBytes:    throttleBytes,
				WaitTime:         waitTime,
				Username:         username,
				ChannelJoinDelay: channelJoinDelay,
				Verbosity:        cli.VerbosityLevel(verbosity, quietLevel),
				DNSServer:        dnsServer,
			})
			return nil
		},
	}

	cmd.Flags().StringVarP(&server, "server", "s", "",
		"Override IRC server (host or host:port). Without this flag, the server is auto-detected from the bot name")
	cmd.Flags().StringVarP(&out, "out", "o", "",
		"Output directory or file path (defaults to current directory with pack filename)")
	cmd.Flags().StringVarP(&throttle, "throttle", "t", "-1",
		"Download speed limit in bytes/s (e.g. 512K, 2M, 1G). -1 = unlimited")
	cmd.Flags().IntVarP(&connectTimeout, "connect-timeout", "C", 120,
		"Seconds to wait for the bot to initiate the DCC transfer")
	cmd.Flags().IntVarP(&stallTimeout, "stall-timeout", "S", 60,
		"Seconds of no transfer progress before aborting. 0 = disabled")
	cmd.Flags().StringVarP(&fallbackChannel, "fallback-channel", "f", "",
		"IRC channel to join if WHOIS returns no channels for the bot")
	cmd.Flags().IntVarP(&waitTime, "wait-time", "w", 0,
		"Extra seconds to wait before sending the XDCC request")
	cmd.Flags().StringVarP(&username, "username", "u", "",
		"IRC nickname to use (a random suffix is always appended; default: random)")
	cmd.Flags().IntVarP(&channelJoinDelay, "channel-join-delay", "D", -1,
		"Seconds to wait after connecting before sending WHOIS (0 = no delay, -1 = random 5-10s, default: -1)")
	cmd.Flags().CountVarP(&verbosity, "verbose", "v", "Increase verbosity: -v shows bot notices, -vv shows full debug info")
	cmd.Flags().CountVarP(&quietLevel, "quiet", "q", "Reduce output: -q hides connection info (keeps errors/notices/progress), -qq suppresses all output")
	cmd.Flags().StringVarP(&dnsServer, "dns-server", "d", "",
		"Fallback DNS resolver used when system DNS is blocked (host:port, default: 8.8.8.8:53)")
	cmd.Flags().StringVarP(&commandServer, "command-server", "", "",
		"Delegate download to a remote xdcc-server (e.g. http://localhost:8080)")

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// runWithServer delegates a download to a remote xdcc-server.
func runWithServer(serverURL, message, outDir string) error {
	c := client.New(serverURL)

	// Check version compatibility
	ver, err := c.CheckVersion()
	if err != nil {
		return fmt.Errorf("cannot connect to xdcc-server at %s: %w", serverURL, err)
	}
	fmt.Fprintf(os.Stderr, "Connected to xdcc-server %s (v%s)\n", serverURL, ver.Version)

	// Parse the XDCC message
	packs, err := entities.ParseXDCCMessage(message, ".", "")
	if err != nil {
		return fmt.Errorf("invalid XDCC message: %w", err)
	}
	if len(packs) == 0 {
		return fmt.Errorf("no packs found in message: %s", message)
	}

	entities.PreparePacks(packs, outDir)

	for _, p := range packs {
		// Use the canonical channel hint for known bot families; otherwise
		// leave the channel empty so the server discovers it via WHOIS.
		channel := entities.ResolveChannel(p.Bot)

		rec := store.DownloadRecord{
			PackMessage:   fmt.Sprintf("xdcc send #%d", p.PackNumber),
			Bot:           p.Bot,
			ServerAddress: p.Server.Address,
			Channel:       channel,
			Filename:      p.Filename,
			FileSize:      p.Size,
		}

		// Send to server
		id, err := c.EnqueueDownload(rec)
		if err != nil {
			// Check for specific error types
			errStr := err.Error()
			if strings.Contains(errStr, "duplicate") {
				fmt.Fprintf(os.Stderr, "⚠️  Skipped %s (already in queue): %v\n", p.Filename, err)
				continue
			}
			return fmt.Errorf("delegating download %s: %w", p.Filename, err)
		}

		fmt.Fprintf(os.Stderr, "📥 Queued %s (id=%d) on server\n", p.Filename, id)

		// Poll for progress
		fmt.Fprintf(os.Stderr, "\n")
		lastProgress := int64(0)
		lastSpeed := float64(0)
		fileSize := p.Size

		_, pollErr := c.PollDownload(id, 1*time.Second, 0, func(rec *store.DownloadRecord) {
			switch rec.Status {
			case store.DownloadStatusQueued:
				fmt.Fprintf(os.Stderr, "\r⏳ Waiting in queue...")
			case store.DownloadStatusDownloading:
				if rec.ProgressBytes > lastProgress || rec.SpeedBPS != int64(lastSpeed) {
					progressPct := 0.0
					if fileSize > 0 && rec.ProgressBytes > 0 {
						progressPct = float64(rec.ProgressBytes) / float64(fileSize) * 100
					}
					speed := formatSpeed(rec.SpeedBPS)
					eta := formatETA(fileSize-rec.ProgressBytes, rec.SpeedBPS)
					fmt.Fprintf(os.Stderr, "\r⬇️  %s — %s/%s (%.1f%%) — %s — ETA %s",
						p.Filename,
						formatBytes(rec.ProgressBytes),
						formatBytes(fileSize),
						progressPct,
						speed,
						eta,
					)
					lastProgress = rec.ProgressBytes
					lastSpeed = float64(rec.SpeedBPS)
				}
			default:
				// Terminal / paused state — will be handled by PollDownload return
			}
		})

		// Print final result
		final, _ := c.GetDownload(id)
		if final != nil {
			switch final.Status {
			case store.DownloadStatusCompleted:
				fmt.Fprintf(os.Stderr, "\r✅ %s — completed successfully                       \n", p.Filename)
			case store.DownloadStatusFailed:
				errMsg := final.ErrorMessage
				if errMsg == "" {
					errMsg = "unknown error"
				}
				fmt.Fprintf(os.Stderr, "\r❌ %s — FAILED: %s                       \n", p.Filename, errMsg)
				if pollErr != nil {
					return pollErr
				}
			case store.DownloadStatusSkipped:
				fmt.Fprintf(os.Stderr, "\r⏭️  %s — skipped (already exists)                       \n", p.Filename)
			default:
				fmt.Fprintf(os.Stderr, "\rℹ️  %s — status: %s                       \n", p.Filename, final.Status)
			}
		} else if pollErr != nil {
			fmt.Fprintf(os.Stderr, "\r⚠️  %s — polling error: %v                       \n", p.Filename, pollErr)
		}
	}

	return nil
}

// formatBytes returns a human-readable byte count (KB/MB/GB).
func formatBytes(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	if b < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	}
	if b < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(b)/(1024*1024*1024))
}

// formatSpeed returns a human-readable speed string.
func formatSpeed(bps int64) string {
	if bps <= 0 {
		return "—"
	}
	if bps < 1024 {
		return fmt.Sprintf("%d B/s", bps)
	}
	if bps < 1024*1024 {
		return fmt.Sprintf("%.1f KB/s", float64(bps)/1024)
	}
	return fmt.Sprintf("%.1f MB/s", float64(bps)/(1024*1024))
}

// formatETA returns a human-readable ETA string.
func formatETA(remaining, speedBPS int64) string {
	if speedBPS <= 0 || remaining <= 0 {
		return "—"
	}
	secs := remaining / speedBPS
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	if secs < 3600 {
		return fmt.Sprintf("%dm %ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%dh %dm", secs/3600, (secs%3600)/60)
}
