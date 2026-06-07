// xdcc-server starts the persistent XDCC IRC download manager with REST API + web UI.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"xdcc-go/internal/api"
	"xdcc-go/internal/bridge"
	"xdcc-go/internal/config"
	"xdcc-go/internal/ircmanager"
	"xdcc-go/internal/logging"
	"xdcc-go/internal/metrics"
	"xdcc-go/internal/notifier"
	"xdcc-go/internal/queue"
	"xdcc-go/internal/searchagg"
	"xdcc-go/internal/sse"
	"xdcc-go/internal/store"
)

// Version is set at build time via ldflags.
var Version = "0.9.5"

func main() {
	var (
		configPath string
		dbPath     string
		pprof      bool
	)

	cmd := &cobra.Command{
		Use:   "xdcc-server",
		Short: "XDCC download manager server",
		Long: `xdcc-server runs a persistent XDCC IRC download manager with a REST API
and web UI for managing downloads, search, and watchlists.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer(configPath, dbPath, pprof)
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "config.yaml",
		"Path to config.yaml configuration file")
	cmd.Flags().StringVarP(&dbPath, "db", "d", "",
		"Full path to SQLite database file (overrides config db_path directory; default: <db_path>/xdcc-server.db)")
	cmd.Flags().BoolVar(&pprof, "pprof", false,
		"Enable block and mutex profiling at maximum rate for /debug/pprof endpoints")

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runServer(configPath, dbPath string, pprof bool) error {
	// ── Load configuration ──────────────────────────────────────────────
	cfg, err := config.Load(configPath, nil)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Use config DBPath (directory) if CLI flag was not provided
	// The actual db file is always named xdcc-server.db
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Storage.DBPath, "xdcc-server.db")
	}

	// ── Set up structured logging ────────────────────────────────────────
	logLevel, err := logging.ParseLevel(cfg.Logging.Level)
	if err != nil {
		return fmt.Errorf("invalid log level %q: %w", cfg.Logging.Level, err)
	}
	srvLogger := logging.New(logLevel, cfg.Logging.FilePath, 0)

	// ── Ensure database directory exists ─────────────────────────────────
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return fmt.Errorf("creating database directory %s: %w", dbDir, err)
	}
	srvLogger.Infof("config: %s", configPath)
	srvLogger.Infof("database: %s", dbPath)

	// ── Initialize store and run migrations ─────────────────────────────
	st, err := store.NewSQLiteStore(dbPath, srvLogger)
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer st.Close()

	if err := st.Migrate(context.Background()); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	// ── Reset persisted server statuses on startup ────────────────────
	// Any "connected" or "reconnecting" status from a previous run is
	// stale — connections don't survive a restart. Reset them all to
	// "disconnected" so ircMgr.Start() sees the correct initial state
	// and reconnects auto-connect servers from scratch.
	if err := st.ResetAllServerStatuses(context.Background()); err != nil {
		srvLogger.Warnf("resetting server statuses on startup: %v", err)
	}

	// ── SSE hub ──────────────────────────────────────────────────────────
	sseHub := sse.NewHub(100)

	// ── Log broadcaster (bridges log output to SSE) ──────────────────────
	logBroadcaster := logging.NewLogBroadcaster(sseHub)
	srvLogger.AddWriter(logBroadcaster)

	// ── Event bridge context (cancelled before SSE hub close) ──────────
	// This context controls the forwarding goroutines that bridge queue/IRC
	// events to the SSE hub. It is cancelled during shutdown BEFORE the SSE
	// hub is closed, allowing the forwarders to drain remaining events.
	// Deferred so bridge goroutines are always cleaned up on early return.
	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	defer bridgeCancel()

	// ── Enable block and mutex profiling for /debug/pprof endpoints ────
	// 0 = disabled (default in production). Set via profiling.block_profile_rate
	// and profiling.mutex_profile_fraction in config.yaml, or env vars
	// XDCC_PROFILING_BLOCK_RATE / XDCC_PROFILING_MUTEX_FRACTION.
	// The --pprof CLI flag forces both to 1 (max detail, measurable overhead).
	// Rate 1 = profile every event (max detail, measurable overhead).
	if pprof {
		wasBlock := cfg.Profiling.BlockProfileRate
		wasMutex := cfg.Profiling.MutexProfileFraction
		cfg.Profiling.BlockProfileRate = 1
		cfg.Profiling.MutexProfileFraction = 1
		srvLogger.Infof("profiling: --pprof flag set, block rate %d→1, mutex fraction %d→1", wasBlock, wasMutex)
	}
	if cfg.Profiling.BlockProfileRate > 0 {
		runtime.SetBlockProfileRate(cfg.Profiling.BlockProfileRate)
	}
	if cfg.Profiling.MutexProfileFraction > 0 {
		runtime.SetMutexProfileFraction(cfg.Profiling.MutexProfileFraction)
	}

	// ── Initialize and start subsystems ──────────────────────────────────
	ircMgr := ircmanager.New(st, cfg, srvLogger)
	queueMgr := queue.New(st, cfg, srvLogger)
	searchAgg := searchagg.New(st, &cfg.Search, srvLogger)
	met := metrics.New()

	// ── Event bridge (forwards queue + IRC events to SSE hub) ──────────
	// Subscriptions happen before Start() so no events are missed.
	eventBridge := bridge.New(sseHub, srvLogger)
	var bridgeWg sync.WaitGroup

	// Forward IRC events (server connect/disconnect, channel join/leave, etc.)
	ircCh := ircMgr.Subscribe()
	bridgeWg.Add(1)
	go eventBridge.ForwardIRCEvents(bridgeCtx, ircCh, &bridgeWg)

	// Forward queue events (download queued/started/progress/completed/failed, etc.)
	queueCh := queueMgr.Subscribe()
	bridgeWg.Add(1)
	go eventBridge.ForwardQueueEvents(bridgeCtx, queueCh, &bridgeWg)

	// Start IRC manager — connects config default servers and DB auto-connect servers.
	if err := ircMgr.Start(); err != nil {
		return fmt.Errorf("starting IRC manager: %w", err)
	}

	// Attach IRC manager to queue so downloads use persistent connections.
	queueMgr.SetIRCManager(ircMgr)
	if err := queueMgr.Start(); err != nil {
		return fmt.Errorf("starting queue manager: %w", err)
	}

	searchAgg.SetMetrics(met)
	if err := searchAgg.Start(context.Background()); err != nil {
		return fmt.Errorf("starting search aggregator: %w", err)
	}

	// ── Notifications (ntfy, pushover, webhook) ───────────────────────────
	notifMgr := notifier.NewManager(cfg.Notifications, srvLogger)
	var notifWg sync.WaitGroup
	if notifMgr != nil && len(notifMgr.Notifiers()) > 0 {
		queueChForNotif := queueMgr.Subscribe()
		notifWg.Add(1)
		go notifMgr.Run(bridgeCtx, queueChForNotif, &notifWg)
		srvLogger.Infof("notifier: started (%d providers)", len(notifMgr.Notifiers()))
	} else if notifMgr != nil {
		srvLogger.Debugf("notifier: no providers configured, skipping")
	}

	// Wire watchlist new-results callback to notifier (handles ntfy/pushover/webhook
	// for watchlist_new_results events). Safe to call even if notifMgr is nil or
	// has no providers — notifier.Manager.NotifyWatchlistResults checks internally.
	if notifMgr != nil {
		searchAgg.SetOnWatchlistResults(notifMgr.NotifyWatchlistResults)
	}

	// ── Generate or load admin token ─────────────────────────────────────
	adminToken := cfg.Security.AdminToken
	if adminToken == "" {
		adminToken = generateToken(32)
		srvLogger.Infof("╔══════════════════════════════════════════════════════════════╗")
		srvLogger.Infof("║  ADMIN TOKEN: %-48s║", adminToken)
		srvLogger.Infof("║  Use X-Admin-Token header to access system/security endpoints ║")
		srvLogger.Infof("╚══════════════════════════════════════════════════════════════╝")
	}
	// Set in config so the API middleware can read it
	cfg.Security.AdminToken = adminToken

	// ── SSE debug logger (separate from srvLogger to avoid feedback loop) ─
	// This logger does NOT have logBroadcaster as a writer, so logging here
	// won't trigger SSE events that create infinite loops.
	sseDebugLogger := logging.New(logging.LevelDebug, "", 0)

	// ── HTTP API ─────────────────────────────────────────────────────────
	apiHandler := api.New(st, ircMgr, queueMgr, searchAgg, sseHub,
		logBroadcaster, cfg, configPath, srvLogger, met, sseDebugLogger)

	router := apiHandler.Router()

	// ── Start HTTP server ────────────────────────────────────────────────
	bindAddr := cfg.HTTP.BindAddress
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	listenAddr := fmt.Sprintf("%s:%d", bindAddr, cfg.HTTP.Port)

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", listenAddr, err)
	}

	// ── Create cancellable context for HTTP request lifecycle ────────────
	// This context is passed via BaseContext to all HTTP handlers. When
	// cancelled on shutdown, SSE handlers see r.Context().Done() and return
	// immediately, allowing srv.Shutdown() to complete without timeout.
	httpCtx, httpCancel := context.WithCancel(context.Background())
	defer httpCancel()

	srv := &http.Server{
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE requires unlimited write timeout
		IdleTimeout:  120 * time.Second,
		BaseContext: func(_ net.Listener) context.Context {
			return httpCtx
		},
	}

	srvLogger.Infof("xdcc-server v%s starting on %s", Version, listenAddr)

	// ── Graceful shutdown ────────────────────────────────────────────────
	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		srvLogger.Infof("received signal %v, shutting down...", sig)
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}

	// ── Ordered shutdown ───────────────────────────────────────────────────
	// 0. Cancel bridge and HTTP contexts — forwarders drain + SSE handlers exit.
	bridgeCancel()
	httpCancel()

	// 1. Wait for bridge forwarders to drain and exit (max ~100ms drain timeout).
	//    Must complete before sseHub.Close() to avoid publishing to a closed hub.
	bridgeWg.Wait()

	// 2. Brief pause for SSE handler goroutines to exit their select loops.
	time.Sleep(10 * time.Millisecond)

	// 3. Close SSE hub (cleanup remaining event buffers)
	sseHub.Close()

	// 4. Shutdown HTTP server — now fast because all SSE connections have
	//    been released by the context cancellation in step 0.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		srvLogger.Warnf("HTTP server shutdown: %v", err)
	}

	// 5. Disconnect all IRC connections gracefully
	ircMgr.Stop()

	// 6. Cancel active download queue (monitor loop stops, downloaders saved)
	queueMgr.Stop()

	// 7. Stop search aggregator (cache cleanup, stats flush)
	searchAgg.Stop()

	// 8. Stop notifier (drain remaining events)
	if notifMgr != nil {
		notifWg.Wait()
	}

	srvLogger.Infof("xdcc-server stopped cleanly")
	return nil
}

// generateToken creates a cryptographically random hex-encoded token.
func generateToken(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("generateToken: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
