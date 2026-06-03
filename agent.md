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
| **Backend** | Go 1.25+, SQLite (modernc.org/sqlite), girc (IRC), chi (HTTP router) |
| **Frontend** | Svelte 5, Vite |
| **Protocol** | IRC + DCC (file transfer), Server-Sent Events (real-time updates) |
| **Storage** | SQLite (persistent queue, config, watchlists) |
| **Deployment** | Docker, systemd, bare metal |

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
├── api/          # REST API handlers (chi router, JSON responses)
├── cli/          # Shared CLI utilities (verbosity, flag parsing)
├── client/       # HTTP client for CLI → server delegation
├── config/       # Configuration loading (YAML + env + flags)
├── diskmon/      # Disk space monitoring
├── downloader/   # DCC transfer implementation
├── entities/     # Core domain models (XDCCPack, IrcServer, parsing logic)
├── irc/          # IRC protocol client (girc wrapper, DCC handling)
├── ircmanager/   # Multi-server connection manager (daemon only)
├── logging/      # Structured logging
├── queue/        # Download queue orchestration (SQLite-backed)
├── search/       # Search engine implementations
├── searchagg/    # Search aggregation + caching + deduplication
├── sse/          # Server-Sent Events hub for real-time browser updates
└── store/        # SQLite persistence layer
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

**Real-time Updates:**
```
Download event → sse.Hub.Broadcast() → SSE stream → web UI updates
```

### Core Abstractions

| Type | Responsibility |
|------|----------------|
| `entities.XDCCPack` | Represents a downloadable pack (server, bot, pack#, filename, size) |
| `irc.Client` | Handles IRC protocol + DCC transfers for a single connection |
| `ircmanager.Manager` | Manages persistent IRC connections (daemon only) |
| `queue.Manager` | Orchestrates download queue, retries, parallelism limits |
| `store.SQLiteStore` | All persistent state (downloads, config, watchlists, servers) |
| `searchagg.Aggregator` | Coordinates multiple search providers with caching |

---

## Coding Standards

### Configuration Loading

**Priority** (highest to lowest):
1. CLI flags (e.g., `--port 9090`)
2. Environment variables (e.g., `XDCC_HTTP_PORT=9090`)
3. `config.yaml` file

Implemented in `config.Load()` which merges all three sources.

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
// Use api.writeError for consistent JSON responses
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
store.InsertDownload(record)
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
- Manual versioning in `store/schema.go`
- Always test migrations with existing databases
- Maintain backward compatibility

### Concurrency

**Rules:**
1. Always pass `context.Context` as first parameter
2. Use channels to communicate between goroutines (especially with girc event handlers)
3. Spawn download workers per active download (managed by `queue.Manager`)
4. SSE broadcasts are concurrent-safe via `sse.Hub.Broadcast()`

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

**Example (download worker):**
```go
func (m *Manager) processDownload(ctx context.Context, packID int64) error {
    // Context for cancellation
    select {
    case <-ctx.Done():
        return ctx.Err()
    case result := <-downloadCh:
        // process result
    }
}
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

### Test Practices

**DO:**
- Use `t.Parallel()` for independent tests
- Keep fixtures minimal (prefer inline test data)
- Test error paths explicitly
- Use in-memory SQLite (`:memory:`) for database tests

**DON'T:**
- Rely on external services (IRC, HTTP)
- Use shared global state
- Skip race detector for concurrent code

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
- `/data`: SQLite database
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
2. `web/frontend.go` embeds dist via `//go:embed dist`
3. `xdcc-server` serves embedded files via `http.FileServer`

**Routing:**
- `/api/*`: REST API endpoints
- All other paths: Serve `index.html` (SPA routing)

**Real-time Updates:**
- Server-Sent Events (SSE) at `/api/events`
- `sse.Hub` broadcasts to all connected clients
- Events: download progress, status changes, queue updates

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
✅ **Always** access database via `store.SQLiteStore` methods  
✅ **Always** use `t.Parallel()` for independent tests  
✅ **Always** check configuration priority: flags > env > yaml

### DON'T

❌ **Never** use CGO (all builds must be `CGO_ENABLED=0`)  
❌ **Never** use `log.Println`, `log.Printf`, or `*log.Logger` (use `*logging.Logger` everywhere)  
❌ **Never** pass a `*log.Logger` adapter to any constructor — all constructors accept `*logging.Logger`  
❌ **Never** swallow errors without wrapping  
❌ **Never** access database outside `store.SQLiteStore`  
❌ **Never** use shared global state in tests  
❌ **Never** skip the race detector for concurrent code  
❌ **Never** hardcode configuration (use flags/env/yaml)  
❌ **Never** block the main goroutine in girc event handlers (use channels)  
❌ **Never** commit without asking user if he wants to run `go test ./...`  
❌ **Never** modify database schema without migration logic

---

## Common Tasks

### Adding a New Search Engine

1. Create `internal/search/<engine_name>.go`
2. Implement `search.Engine` interface
3. Register in `searchagg.New()` call in `cmd/xdcc-server/main.go`
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

1. Update table definitions in `store/schema.go`
2. Add migration logic in `store/sqlite.go` (manual versioning)
3. Test with existing databases (backward compatibility)
4. Document migration in commit message

### Adding Real-Time Events

1. Define event type in `sse/events.go` (if new type needed)
2. Emit via `api.SSEHub.Broadcast(sse.Event{Type: "event_name", Data: payload})`
3. Update frontend to listen for the event type
4. Test event delivery in integration tests

---

## Notes for AI Agents

### When Modifying Code

1. **Check the full context**: This is a multi-binary project. Changes to `internal/` affect all four binaries.
2. **Respect the dual-mode design**: CLI tools work standalone AND with `--command-server` delegation.
3. **Understand IRC timing**: IRC operations are inherently asynchronous. Don't assume immediate responses.
4. **Test concurrency**: Downloads, IRC handlers, and SSE broadcasts all run concurrently. Use `-race` detector.
5. **Preserve CGO=0**: All builds must remain pure Go. No C dependencies.

### When Adding Features

1. **Search engines**: Implement `search.Engine` interface, register in aggregator
2. **API endpoints**: Follow REST conventions, use `api.writeError()` for errors
3. **CLI flags**: Use cobra, respect configuration priority
4. **Database changes**: Manual migrations, test backward compatibility
5. **Real-time updates**: Use `sse.Hub` for browser notifications

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
