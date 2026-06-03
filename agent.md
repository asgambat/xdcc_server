# xdcc-go AI Agent Guidelines

> **This file applies to ALL AI tools used in this repository.**
>
> These guidelines are tool-agnostic and serve as the single source of truth for project architecture, coding standards, and development practices.

---

## Project Overview

xdcc-go is a high-performance XDCC downloader for IRC written in Go with a modern web interface.

**Components:**
- **xdcc-server**: Persistent daemon with REST API + Svelte 5 web UI
- **xdcc-dl**: CLI tool for downloading packs (standalone or delegated)
- **xdcc-search**: CLI search tool across multiple providers
- **xdcc-browse**: Interactive terminal UI for search → selection → download

**Key Characteristics:**
- Pure Go binaries (CGO_ENABLED=0) for zero-dependency deployment
- Cross-compilation: linux/amd64, linux/arm64, darwin/amd64, darwin/arm64
- Multi-stage Docker builds with embedded frontend
- Dual-mode CLI tools: standalone OR client-server delegation

---

## Tech Stack

| Layer | Technology |
|-------|------------|
| **Backend** | Go 1.25+, SQLite (modernc.org/sqlite), girc (IRC), chi (HTTP router), cobra (CLI) |
| **Frontend** | Svelte 5, Vite |
| **Protocol** | IRC + DCC (file transfer), Server-Sent Events (real-time updates) |
| **Storage** | SQLite (persistent queue, config, watchlists, presets, provider stats) |
| **Deployment** | Docker (multi-arch, scratch base, non-root user), systemd, bare metal |

---

## Architecture Guidelines

### Four-Binary Design

The project is structured around four independent binaries with shared internal packages:

1. **xdcc-server** (daemon)
   - Manages persistent IRC connections across multiple servers
   - SQLite-backed download queue with retry logic
   - REST API + SSE event stream
   - Serves embedded Svelte web UI

2. **xdcc-dl** (CLI)
   - Downloads XDCC packs from IRC bots
   - **Standalone mode**: Direct IRC connection (ephemeral)
   - **Client mode**: Delegates to xdcc-server via `--command-server`

3. **xdcc-search** (CLI)
   - Searches across multiple providers (nibl, xdcc-eu, subsplease)
   - Outputs machine-parsable results

4. **xdcc-browse** (CLI)
   - Interactive TUI: search → filter → select → download
   - Can delegate downloads to xdcc-server

### Package Structure

```
internal/
├── api/          # REST API handlers (chi router, middleware, JSON responses)
├── bridge/       # Forwards IRC + queue events to SSE hub (extracted from main.go)
├── cli/          # Shared CLI utilities (verbosity, flag parsing)
├── client/       # HTTP client for CLI → server delegation
├── config/       # Configuration loading (YAML + env + flags), validation
├── diskmon/      # Disk space monitoring with platform-specific implementations
├── downloader/   # DCC transfer implementation
├── entities/     # Core domain models (XDCCPack, IrcServer, parsing logic)
├── irc/          # IRC protocol client (girc wrapper, DCC handling)
├── ircmanager/   # Multi-server persistent connection manager (daemon only)
├── logging/      # Structured logging with levels, file rotation, SSE broadcast
├── metrics/      # Runtime metrics collection (HTTP concurrency, provider timeouts, etc.)
├── notifier/     # External notifications (webhook, ntfy, pushover)
├── pubsub/       # Generic typed pub/sub hub for fanning out events
├── queue/        # Download queue orchestration (SQLite-backed, retry, parallelism)
├── search/       # Search engine implementations (nibl, xdcc-eu, subsplease)
├── searchagg/    # Search aggregation, caching, deduplication, presets, watchlists
├── sse/          # Server-Sent Events hub + event type constants
└── store/        # SQLite persistence (focused interfaces, schema migrations, backup)
```

### Key Data Flows

**CLI Standalone:**
```
xdcc-dl → entities.XDCCPack → irc.Client → DCC transfer → file saved
```

**CLI Client Mode:**
```
xdcc-dl --command-server → client.CommandClient → HTTP POST → server API → queue → download
```

**Server Mode:**
```
Web UI → REST API → queue.Manager → ircmanager.Manager → irc.Client → DCC transfer
```

**Real-time Updates (event pipeline):**
```
Download event → queue.Manager publishes to pubsub.Hub
    → bridge.EventBridge forwards to sse.Hub.Broadcast()
    → SSE stream → web UI updates

IRC event → ircmanager.Manager publishes to pubsub.Hub
    → bridge.EventBridge forwards to sse.Hub.Broadcast()
    → SSE stream → web UI updates

Log entry → logging.LogBroadcaster → sse.Hub.Broadcast()
    → SSE stream → web UI log viewer
```

**Event Types (must stay in sync with frontend `api.js`):**
All SSE event types are defined as constants in `internal/sse/events.go`. The frontend SSEClient in `web/src/lib/api.js` must register listeners for the exact same string constants. When adding a new event type:
1. Add constant in `sse/events.go`
2. Add listener name in `api.js` SSEClient eventTypes array
3. Handle the event in the appropriate Svelte component

### Core Abstractions

| Type | Package | Responsibility |
|------|---------|----------------|
| `entities.XDCCPack` | entities | Represents a downloadable pack (server, bot, pack#, filename, size) |
| `irc.Client` | irc | Handles IRC protocol + DCC transfers for a single connection |
| `ircmanager.Manager` | ircmanager | Manages persistent IRC connections across multiple servers |
| `queue.Manager` | queue | Orchestrates download queue, retries, parallelism limits |
| `queue.Event` | queue | Standardized event type for download lifecycle changes |
| `store.Store` | store | Composite interface for all persistence (embeds focused interfaces) |
| `store.ServerStore` | store | Focused interface for IRC server/channel persistence |
| `store.DownloadStore` | store | Focused interface for download queue persistence |
| `store.SearchCacheStore` | store | Focused interface for search result caching |
| `store.SearchPresetStore` | store | Focused interface for search presets |
| `store.WatchlistStore` | store | Focused interface for watchlist persistence |
| `store.ProviderStatsStore` | store | Focused interface for provider metrics |
| `search.Engine` | search | Interface for search providers (`Name()`, `Search()`) |
| `searchagg.Aggregator` | searchagg | Coordinates multiple search providers with caching |
| `sse.Hub` | sse | Server-Sent Events hub for broadcasting to connected clients |
| `pubsub.Hub[T]` | pubsub | Generic typed pub/sub hub for internal event fan-out |
| `bridge.EventBridge` | bridge | Forwards IRC + queue events to SSE hub for real-time push |
| `notifier.Manager` | notifier | Orchestrates multiple notification providers |
| `notifier.Notifier` | notifier | Interface for notification providers (webhook, ntfy, pushover) |
| `metrics.Metrics` | metrics | Runtime metrics collector |
| `diskmon.Monitor` | diskmon | Disk space monitoring with configurable thresholds |
| `logging.Logger` | logging | Structured logger with levels (Debug/Info/Warn/Error) |
| `config.Config` | config | Merged configuration from YAML + env + CLI flags |

### Focused Interface Pattern

The `store` package defines **focused interfaces** (e.g., `ServerStore`, `DownloadStore`) that expose only the methods each consumer needs. These are embedded into a composite `Store` interface for backward compatibility and convenience.

**Rule:** New code should depend on the focused interface, not the composite `Store`.

```go
// ✅ Correct: depend only on what you need
func NewAggregator(store DownloadStore, cacheStore SearchCacheStore, ...) *Aggregator

// ❌ Wrong: depend on the entire Store
func NewAggregator(store Store, ...) *Aggregator
```

This pattern is being progressively applied across the codebase. When refactoring, narrow the dependency to the smallest focused interface.

---

## Coding Standards

### Configuration Loading

**Priority** (highest to lowest):
1. CLI flags (e.g., `--port 9090`)
2. Environment variables (e.g., `XDCC_HTTP_PORT=9090`)
3. `config.yaml` file

Implemented in `config.Load()` which merges all three sources. Config is validated after loading; all validation errors are collected and reported together.

### Error Handling

**DO:**
```go
// Always wrap errors with context
if err != nil {
    return fmt.Errorf("connecting to IRC server: %w", err)
}

// Use typed errors for control flow
if errors.Is(err, irc.ErrBotNotFound) {
    // handle specific case
}
```

**DON'T:**
```go
// Never swallow errors silently
if err != nil {
    log.Println("error occurred")  // Lost context!
}

// Never use naked returns without wrapping
return err
```

**HTTP Handlers:**
```go
// Use api.writeError for consistent JSON error responses
api.writeError(w, http.StatusBadRequest, "invalid_input", "pack number must be positive")
```

### Logging

**Rule: Always use `*logging.Logger` — never `*log.Logger` or `log.Printf`.**

The project provides a single structured logger (`internal/logging`) that supports levels,
file rotation, and broadcasting log lines to SSE clients. All components MUST use it.

The `xdccirc.Logger` interface (used by `irc.Client`) is satisfied by `*logging.Logger`
via its `Printf` method. No other logger type should be used.

**DO:**
```go
// Use the logging.Logger instance with level-specific methods
logger.Infof("download started: %s", filename)
logger.Warnf("retrying download %d: %v", id, err)
logger.Errorf("IRC connection failed: %v", err)

// For compatibility with xdccirc.Logger (used by irc.Client)
logger.Printf("download progress: %d/%d", progress, total)

// To create a logger
logger := logging.New(level, filePath, rotateMB)

// To attach a log broadcaster (SSE)
logger.AddWriter(logBroadcaster)
```

**DON'T:**
```go
// Never use stdlib log package directly
log.Println("something happened")       // Bypasses structured logging!
log.New(os.Stderr, "[tag] ", log.LstdFlags) // No levels, no broadcast!
log.Printf("download %d completed", id) // No level context!

// Never create *log.Logger wrappers
// Wrong: log.New(logger.Writer(logging.LevelInfo), "", 0)
// Right: use logger directly
```

**Levels:**
- **DEBUG**: Verbose diagnostic info (IRC messages, DCC negotiation)
- **INFO**: Normal operational events (download started, completed)
- **WARN**: Recoverable issues (retry attempt, stalled transfer)
- **ERROR**: Unrecoverable errors (connection failed, file write error)

**Constructor Signature:** `logging.New(level Level, filePath string, rotateMB int) *Logger`
- `level`: `logging.LevelDebug`, `logging.LevelInfo`, `logging.LevelWarn`, `logging.LevelError`
- `filePath`: Path to log file, or `""` for stderr-only
- `rotateMB`: Max file size in MB before rotation (0 = no rotation)

### Database Access

**Patterns:**
```go
// All access through store.SQLiteStore
store.EnqueueDownload(record)
store.UpdateDownloadStatus(id, status)
store.GetDownloadByID(id)
store.ListDownloads(filters)

// Explicit transactions
tx, err := store.BeginTx(ctx)
defer tx.Rollback()
// ... multiple operations
tx.Commit()
```

**Schema Migrations:**
- Versioned migrations in `store/schema.go` (`currentSchemaVersion = 7`)
- Each migration is a `migration` struct: `{version, description, up SQL}`
- Before each migration, an auto-backup is created (`{dbPath}.backup.v{N}.{timestamp}`)
- Only the 3 most recent backups are kept; older ones are auto-cleaned
- Always test migrations with existing databases
- Maintain backward compatibility — never modify existing migration versions

### Concurrency

**Rules:**
1. Always pass `context.Context` as first parameter
2. Use channels to communicate between goroutines (especially with girc event handlers)
3. Spawn download workers per active download (managed by `queue.Manager`)
4. SSE broadcasts are concurrent-safe via `sse.Hub.Broadcast()`
5. Use `pubsub.Hub[T]` for internal event fan-out (non-blocking publish)

**Goroutine Lifecycle Management (CRITICAL):**

When managing long-lived goroutines, **ALWAYS use the WaitGroup pattern** to prevent race conditions:

```go
type MyWorker struct {
    wg           sync.WaitGroup  // Tracks active goroutines
    runningMu    sync.Mutex      // Protects isRunning field
    isRunning    bool            // Prevents duplicate start() calls
    ctx          context.Context
    cancel       context.CancelFunc
}

func (w *MyWorker) Start() {
    // Prevent duplicate calls
    w.runningMu.Lock()
    if w.isRunning {
        w.runningMu.Unlock()
        return
    }
    w.isRunning = true
    w.runningMu.Unlock()

    w.wg.Add(1)
    go w.run()
}

func (w *MyWorker) run() {
    defer w.wg.Done()
    defer func() {
        w.runningMu.Lock()
        w.isRunning = false
        w.runningMu.Unlock()
    }()

    // Main loop
    for {
        select {
        case <-w.ctx.Done():
            return
        // ... work ...
        }
    }
}

func (w *MyWorker) Stop() {
    w.cancel()
    w.wg.Wait()  // Guaranteed to wait for completion, no race
}
```

**Why WaitGroup over done channel?**
- ✅ Thread-safe by design (no initialization races)
- ✅ Prevents duplicate goroutine launches
- ✅ Guaranteed cleanup before Stop() returns
- ✅ Easier to extend (multiple goroutines tracked by same WaitGroup)
- ❌ **NEVER** use `done chan struct{}` initialized in the goroutine itself (race condition)

**Drain-on-Shutdown Pattern:**
Forwarding goroutines (bridge, notifier) drain remaining events with a short timeout (100ms) after context cancellation, before returning:

```go
func drainEvents(ch <-chan Event) {
    timeout := time.After(100 * time.Millisecond)
    for {
        select {
        case evt, ok := <-ch:
            if !ok { return }
            // process event
        case <-timeout:
            return
        }
    }
}
```

**Generic Pub/Sub Pattern:**
Use `pubsub.Hub[T any]` for typed event fan-out. Publish is non-blocking (drops events when subscriber buffer is full). Subscribe returns a channel; the caller must drain it.

```go
hub := pubsub.New[MyEvent](64)  // bufSize=64 per subscriber
ch := hub.Subscribe()
hub.Publish(evt)
hub.Unsubscribe(ch)
hub.Close()  // closes all subscriber channels
```

### Naming Conventions

| Pattern | Example | Usage |
|---------|---------|-------|
| `Insert*` | `InsertDownload()` | Create new record |
| `Update*` | `UpdateDownloadStatus()` | Modify existing record |
| `Get*ByID` | `GetDownloadByID()` | Retrieve single record by primary key |
| `List*` | `ListDownloads()` | Retrieve multiple records |
| `*Manager` | `QueueManager`, `IRCManager` | Orchestrator/coordinator types |
| `*Client` | `irc.Client`, `client.CommandClient` | External communication |
| `*Store` | `ServerStore`, `DownloadStore` | Focused persistence interface |
| `New*` | `NewSQLiteStore()` | Constructor function |
| `*Notifier` | `WebhookNotifier`, `NtfyNotifier` | Notification provider |
| `Notifier` | (interface) | Notification provider interface |

### `.gitignore` / `.dockerignore` Conventions (CRITICAL)

**Rule: ALL patterns that target root-level files or directories MUST be anchored with `/`.**

Without `/`, patterns match at ANY depth, which can silently exclude source directories.

```dockerignore
# ✅ CORRECT: anchored to root
/xdcc-server
/xdcc-dl
/bin/
/docs/
/README.md

# ❌ WRONG: matches anywhere in the tree
xdcc-server    # Also matches cmd/xdcc-server/!
bin/            # Also matches internal/foo/bin/!
README.md       # Also matches docs/old/README.md!
```

**Exceptions (safe to leave unanchored):**
- `*.exe`, `*.test` — file extension globs (can't match directory names)
- `*.out` — file extension glob (typically coverage output at root)

---

## Testing Guidelines

### Test Organization

```go
// Colocate tests with source: package_test.go
package entities_test

// Use table-driven tests
func TestParseXDCCMessage(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    *XDCCPack
        wantErr bool
    }{
        {"valid single", "/msg Bot xdcc send #5", &XDCCPack{...}, false},
        {"invalid format", "xdcc #5", nil, true},
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := ParseXDCCMessage(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("unexpected error state: got %v, wantErr %v", err, tt.wantErr)
            }
            // assertions
        })
    }
}
```

### Template Database Pattern

Integration tests that need a populated SQLite database use a **template database** pattern to avoid running migrations for every test:

1. `store/main_test.go` — `TestMain` creates a template DB with all migrations applied
2. Individual test files copy the template into a temp directory: `copyFile(templateDBPath, dbPath)`
3. Tests use the copy with PRAGMA optimizations (`synchronous=OFF`, `journal_mode=MEMORY`)

```go
// In store/main_test.go (or api/main_test.go)
func TestMain(m *testing.M) {
    dir, _ := os.MkdirTemp("", "xdcc-test-*")
    dbPath := filepath.Join(dir, "template.db")
    s, _ := NewSQLiteStore(dbPath, testLog)
    s.Migrate(context.Background())
    templateDBPath = dbPath
    code := m.Run()
    os.RemoveAll(dir)
    os.Exit(code)
}
```

### Test Commands

```bash
# Run all tests
go test ./...

# With coverage
go test -cover ./...

# Race detector (CRITICAL for concurrent code)
go test -race ./...

# Specific package
go test ./internal/entities

# Specific test
go test -run TestParseXDCCMessage ./internal/entities

# Verbose output
go test -v ./...
```

### Test Types

| Type | Location | Purpose |
|------|----------|---------|
| Unit | `package_test.go` | Pure functions, parsing, validation |
| Integration | `api_integration_test.go` | API handlers with in-memory SQLite |
| Mock | Inline interfaces | IRC connections, HTTP clients |
| Concurrency | `pubsub_test.go` | Race detector with multiple goroutines |

### Test Practices

**DO:**
- Use `t.Parallel()` for independent tests
- Keep fixtures minimal (prefer inline test data)
- Test error paths explicitly
- Use in-memory SQLite (`:memory:`) for database tests
- Use template database pattern for integration tests (avoids repeated migrations)

**DON'T:**
- Rely on external services (IRC, HTTP)
- Use shared global state
- Skip race detector for concurrent code
- Run full migrations in every test (use template DB pattern)

---

## Error Handling Patterns

### Retriable vs Non-Retriable Errors

**Retriable** (handled by `queue.Manager`):
- Network timeouts
- IRC server disconnects
- DCC stall/timeout
- "Pack already requested" (wait + retry)

**Non-Retriable** (abort immediately):
- Bot not found
- Pack does not exist
- Bot denied access (slots full)
- File already exists (with skip policy)

### Typed Errors

Define in `irc/errors.go`:
```go
var (
    ErrBotNotFound = errors.New("bot not found")
    ErrPackDenied  = errors.New("pack request denied")
    ErrStalled     = errors.New("transfer stalled")
)
```

Check with `errors.Is()`:
```go
if errors.Is(err, irc.ErrBotNotFound) {
    // Don't retry
    return err
}
```

---

## Frontend Conventions

### Svelte 5 Component Architecture

**State Management:**
- All global state lives in Svelte writable/derived stores (`web/src/lib/stores.js`)
- Components read from stores via `$storeName` syntax
- Components write to stores via `.set()` / `.update()` calls
- No prop drilling beyond 1 level; use stores for shared state

**Component Organization:**
```
web/src/components/
├── Dashboard.svelte       # Main view: overview, stats, active downloads
├── Sidebar.svelte         # Navigation sidebar with view switching
├── Servers.svelte         # IRC server configuration and management
├── Downloads.svelte       # Download queue (embeds DownloadTable)
├── DownloadTable.svelte   # Reusable download table component
├── Search.svelte          # Search interface with filters
├── Presets.svelte         # Saved search presets management
├── Watchlists.svelte      # Automated watchlist management
├── Providers.svelte       # Provider health and enable/disable
├── Settings.svelte        # Server configuration (YAML editor + structured form)
├── Logs.svelte            # Real-time log viewer (SSE-powered)
├── ConnectionStatus.svelte # SSE connection status indicator
├── Toast.svelte           # Toast notification component
└── Modal.svelte           # Reusable modal dialog component
```

### SSE Event Handling

All real-time updates flow through a singleton `SSEClient` in `web/src/lib/api.js`:

1. **App.svelte** creates the SSE client and registers event listeners
2. Each event type updates its corresponding Svelte store
3. Components reactively update when stores change

**Adding a new SSE event:**
1. Add event type constant in `internal/sse/events.go`
2. Emit event with `sseHub.Publish(eventType, payload)`
3. Add event type name to `eventTypes` array in `web/src/lib/api.js` SSEClient
4. Register listener in `App.svelte` that updates the appropriate store

### API Client Pattern

The `api` object in `web/src/lib/api.js` provides:
- `api.request(method, path, body, opts)` — base request with timeout, auth, error handling
- `api.get()`, `api.post()`, `api.put()`, `api.patch()`, `api.del()` — convenience methods
- Domain-specific API wrappers: `ServersAPI`, `DownloadsAPI`, `SearchAPI`, `PresetsAPI`, `WatchlistsAPI`, `ProvidersAPI`, `SystemAPI`

**Admin Token Pattern:**
- Protected endpoints require `X-Admin-Token` header
- Token stored in `localStorage` with configurable TTL (default 15 min)
- `getAdminToken()` retrieves valid token; `setAdminToken(token, ttlMinutes)` stores it
- On 401 response, `notifyAuthFailure()` triggers re-authentication modal
- `SYSTEM_VIEWS` set controls which views require authentication

### Utility Functions

Found in `web/src/lib/utils.js`:
- `formatBytes(n)` — human-readable byte sizes (KB, MB, GB...)
- `formatSpeed(bps)` — transfer rate formatting
- `formatUptime(seconds)` — server uptime display
- `escapeHTML(str)` — XSS prevention
- `debounce(fn, delay)` — input debouncing helper

---

## Build & Deployment

### Build Commands

**Backend:**
```bash
# All binaries
go build ./cmd/...

# Individual binary
go build -o xdcc-server ./cmd/xdcc-server

# Static build (Docker)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o xdcc-server ./cmd/xdcc-server
```

**Frontend:**
```bash
cd web
npm install
npm run build    # → web/dist/
npm run dev      # Development server on :5173
```

**IMPORTANT:** Always `npm run build` before building `xdcc-server` to embed the latest frontend.

**Docker:**
```bash
# Single architecture
docker build -t xdcc-go .

# Multi-architecture
docker buildx build --platform=linux/amd64,linux/arm64 -t xdcc-go .
```

### Deployment Configuration

**Environment Variables** (override `config.yaml`):
- `XDCC_HTTP_PORT`: HTTP server port (default: 8080)
- `XDCC_IRC_NICKNAME`: IRC nickname base (random suffix added)
- `XDCC_DOWNLOAD_TEMP_DIR`: Partial downloads
- `XDCC_DOWNLOAD_DEST_DIR`: Completed downloads
- `XDCC_LOGGING_LEVEL`: debug | info | warn | error
- `XDCC_LOGGING_FILE_PATH`: Log file (default: stderr)

**Docker Volumes:**
- `/var/lib/xdcc-server/db`: SQLite database
- `/data/downloads/tmp`: Partial files
- `/data/downloads/complete`: Finished downloads
- `/data/logs`: Log files

**Systemd:**
See `examples/xdcc-server.service` for production systemd unit.

---

## Domain-Specific Knowledge

### XDCC Protocol

**Message Format:**
```
/msg <bot> xdcc send #<pack>
```

**Pack Number Syntax:**
| Syntax | Meaning |
|--------|---------|
| `#5` | Single pack |
| `#1-10` | Range (packs 1 through 10) |
| `#1-10;2` | Step range (1, 3, 5, 7, 9) |
| `#1,3,7` | List |

**Parsing:**
```go
// Use entities package
pack, err := entities.ParseXDCCMessage("/msg Bot xdcc send #42")
numbers := entities.ExpandPackNumbers("#1-10;2")  // [1, 3, 5, 7, 9]
```

### DCC Transfer

**Features:**
- DCC SEND protocol for file transfer
- DCC RESUME/ACCEPT for partial file resumption
- Progress tracking (bytes, speed, ETA)
- Stall detection (abort if no progress for N seconds)

**Implementation:**
- `irc/dcc.go`: Protocol negotiation
- `downloader/downloader.go`: Transfer orchestration

### IRC Connection Strategies

**CLI Tools (ephemeral):**
- Single connection per download
- Auto-discovery via WHOIS (finds bot's channels)
- Fallback to `--fallback-channel` if WHOIS fails
- DNS fallback resolver (8.8.8.8) for blocked networks
- Multi-IP failover (tries all resolved IPs)

**Server Daemon (persistent):**
- Managed by `ircmanager.Manager`
- Auto-reconnect with exponential backoff
- Multiple simultaneous server connections
- Channel auto-join on connect

### Search Engine Abstraction

**Interface:**
```go
type Engine interface {
    Name() string
    Search(ctx context.Context, query string) ([]entities.XDCCPack, error)
}
```

**Available Engines:**
- `nibl.Engine`: nibl.co.uk (anime-focused)
- `xdcc_eu.Engine`: xdcc.eu (general)
- `subsplease.Engine`: subsplease.org (anime releases)

**Aggregation:**
- `searchagg.Aggregator` queries all enabled providers in parallel
- Deduplicates by filename + size + bot family
- Two-tier caching: fresh TTL (30m), stale TTL (24h fallback)

### Web UI Embedding

**Build Process:**
1. `npm run build` generates `web/dist/`
2. `frontend.go` embeds dist via `//go:embed web/dist`
3. `xdcc-server` serves embedded files via `http.FileServer`

**Routing:**
- `/api/*`: REST API endpoints (registered first)
- `/healthz`, `/readyz`: Health probes
- `/debug/pprof/*`: Profiling (requires admin token)
- All other paths: Serve `index.html` (SPA routing)

**Real-time Updates:**
- Server-Sent Events (SSE) at `/api/events`
- `sse.Hub` broadcasts to all connected clients
- Events: download progress, status changes, queue updates, IRC state, logs, disk space, watchlist results

---

## DO / DON'T Rules

### DO

✅ **Always** wrap errors with context (`fmt.Errorf("context: %w", err)`)  
✅ **Always** use `logging.Logger`, never `log.Println`  
✅ **Always** pass `context.Context` as first parameter  
✅ **Always** use table-driven tests with `t.Run()`  
✅ **Always** ask if user wants to run `go test -race ./...` before committing concurrent code  
✅ **Always** rebuild frontend (`npm run build`) before building server  
✅ **Always** use typed errors for control flow (`errors.Is()`, `errors.As()`)  
✅ **Always** access database via store interface methods  
✅ **Always** use `t.Parallel()` for independent tests  
✅ **Always** check configuration priority: flags > env > yaml  
✅ **Always** anchor `.gitignore`/`.dockerignore` patterns with `/` for root-only files/directories  
✅ **Always** use the WaitGroup pattern for goroutine lifecycle management  
✅ **Always** depend on focused interfaces, not the composite `Store` (new code)  
✅ **Always** use `pubsub.Hub[T]` for internal event fan-out  
✅ **Always** add SSE event types to both `sse/events.go` AND `web/src/lib/api.js`  
✅ **Always** use the template DB pattern for integration tests (avoid repeated migrations)  
✅ **Always** drain events on shutdown with a short timeout (100ms pattern)

### DON'T

❌ **Never** use CGO (all builds must be `CGO_ENABLED=0`)  
❌ **Never** use `log.Println`, `log.Printf`, or `*log.Logger` (use `*logging.Logger` everywhere)  
❌ **Never** pass a `*log.Logger` adapter to any constructor — all constructors accept `*logging.Logger`  
❌ **Never** swallow errors without wrapping  
❌ **Never** access database outside store interfaces  
❌ **Never** use shared global state in tests  
❌ **Never** skip the race detector for concurrent code  
❌ **Never** hardcode configuration (use flags/env/yaml)  
❌ **Never** block the main goroutine in girc event handlers (use channels)  
❌ **Never** commit without asking user if he wants to run `go test ./...`  
❌ **Never** modify database schema without migration logic  
❌ **Never** use `done chan struct{}` initialized in a goroutine (race condition)  
❌ **Never** leave `.gitignore`/`.dockerignore` patterns unanchored without `/` for root-only matches  
❌ **Never** add SSE events without syncing frontend `api.js` eventTypes array  
❌ **Never** use the composite `Store` interface in new constructors (use focused interfaces)

---

## Common Tasks

### Adding a New Search Engine

1. Create `internal/search/<engine_name>.go`
2. Implement `search.Engine` interface
3. Register in the engine list (see `cmd/xdcc-server/main.go` where engines are passed to `searchagg.New()`)
4. Add tests in `internal/search/<engine_name>_test.go`

### Adding a New API Endpoint

1. Add handler function in `internal/api/handlers_*.go`
2. Register route in `internal/api/router.go` (use chi router)
3. Update web UI to consume the endpoint (if needed)
4. Add integration test in `internal/api/api_integration_test.go`

### Adding a New CLI Flag

1. Add flag definition in `cmd/<tool>/main.go` using cobra
2. Pass to relevant function via config struct
3. Update README.md with flag documentation
4. Test flag precedence (flag > env > yaml)

### Modifying Database Schema

1. Add new migration entry to `migrations` slice in `store/schema.go`
2. Increment `currentSchemaVersion` constant
3. Write the `ALTER TABLE` / `CREATE TABLE` SQL for the `up` field
4. Test with existing databases (backward compatibility via the auto-backup system)
5. Document migration in commit message

### Adding Real-Time Events

1. Define event type constant in `sse/events.go`
2. Emit via `sseHub.Publish(eventType, payload)` in the appropriate package
3. Add event type to `eventTypes` array in `web/src/lib/api.js` SSEClient
4. Register event listener in `App.svelte` that updates the corresponding store
5. Test event delivery in integration tests

### Adding a New Internal Package

1. Create `internal/<package>/` with a `// Package <name> provides...` doc comment
2. Define focused interfaces for consumers
3. Follow existing patterns: constructor `New(...)`, `Start()/Stop()` if long-lived, WaitGroup if goroutines
4. Wire into `cmd/xdcc-server/main.go` following the existing orchestration pattern

### Adding a New Notification Provider

1. Create a new type implementing `notifier.Notifier` interface
2. Add constructor function `NewXxxNotifier(cfg config.NotificationConfig)`
3. Register in `notifier.NewManager()` switch statement
4. Add config example in `config.yaml` comments

---

## Notes for AI Agents

### When Modifying Code

1. **Check the full context**: This is a multi-binary project. Changes to `internal/` affect all four binaries.
2. **Respect the dual-mode design**: CLI tools work standalone AND with `--command-server` delegation.
3. **Understand IRC timing**: IRC operations are inherently asynchronous. Don't assume immediate responses.
4. **Test concurrency**: Downloads, IRC handlers, and SSE broadcasts all run concurrently. Use `-race` detector.
5. **Preserve CGO=0**: All builds must remain pure Go. No C dependencies.
6. **Check `.gitignore` and `.dockerignore`**: Patterns without `/` prefix match at any depth. Always anchor root-only patterns.
7. **Keep SSE events in sync**: Changes to `sse/events.go` MUST be reflected in `web/src/lib/api.js`.

### When Adding Features

1. **Search engines**: Implement `search.Engine` interface, register in engine factory
2. **API endpoints**: Follow REST conventions, use `api.writeError()` for errors, respect admin token middleware
3. **CLI flags**: Use cobra, respect configuration priority
4. **Database changes**: Add versioned migration, test backward compatibility, auto-backup is automatic
5. **Real-time updates**: Add event type to both `sse/events.go` AND `api.js` eventTypes
6. **New packages**: Follow focused interface pattern, wire into `cmd/xdcc-server/main.go` orchestration

### When Debugging

1. **Increase verbosity**: Use `--verbose` (or `-v`, `-vv`) for CLI tools
2. **Check logs**: Server logs to stderr or `XDCC_LOGGING_FILE_PATH`
3. **IRC timing issues**: Use `--channel-join-delay` and `--wait-time` flags
4. **DNS problems**: Use `--dns-server=1.1.1.1:53` to bypass blocked DNS
5. **Multi-IP failover**: Check logs for "IP x/y" messages showing connection attempts

### Architecture Decision Records

**Why SQLite?**
- Zero external dependencies
- Embedded in binary
- Sufficient for single-server workload
- Transactional queue operations

**Why pure Go (no CGO)?**
- Cross-compilation without toolchain hell
- Static binaries (Docker, systemd, bare metal)
- Consistent behavior across platforms

**Why dual-mode CLI?**
- Flexibility: standalone for simple use, server for persistent management
- No forced daemon for one-off downloads
- Remote control via HTTP (no direct IRC access needed)

**Why SSE over WebSockets?**
- One-way server → client push (no client → server needed)
- Simpler protocol (HTTP-compatible)
- Auto-reconnect built into EventSource API
- See `docs/SSE-vs-WebSocket-analysis.md` for full analysis

**Why focused interfaces in store?**
- Each consumer depends only on the methods it needs
- Easier to mock in tests (implement only the needed interface)
- Gradual migration from composite `Store` — backward compatible via embedding

**Why pubsub.Hub over channel broadcast loops?**
- Generic reusable pattern (`pubsub.Hub[T any]`)
- Non-blocking publish (drops when full, no publisher blocking)
- Thread-safe subscribe/unsubscribe during operation
- Used by both `ircmanager` and `queue` for event fan-out

---

## Development Workflow

1. **Frontend changes**: `cd web && npm run dev` (hot reload on :5173, proxies API to :8080)
2. **Backend changes**: Rebuild binary and restart server
3. **Full rebuild**: `cd web && npm run build && cd .. && go build ./cmd/xdcc-server`
4. **Before commit**: ask user if he wants to run `go test ./...` and `go test -race ./...`
5. **Docker test**: `docker build -t xdcc-go .` (tests multi-stage build)

---

## Additional Resources

- **README.md**: User-facing documentation, installation, usage examples
- **config.yaml**: Default configuration with inline comments
- **examples/xdcc-server.service**: Production systemd unit file
- **docs/**: Architecture decision records and design documents
- **.golangci.yml**: Linter configuration
- **Taskfile.yml**: Task automation (build, test, lint, docker)
