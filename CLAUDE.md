# CLAUDE.md

Behavioral guidelines to reduce common LLM coding mistakes. Merge with project-specific instructions as needed.

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.


## Project Overview

xdcc_server is a high-performance XDCC downloader for IRC written in Go with a modern Svelte 5 web UI.

**Four binaries sharing `internal/` packages:**
- `cmd/xdcc-server/` — Persistent daemon with REST API (chi router), SSE real-time events, Svelte 5 web UI
- `cmd/xdcc-dl/` — CLI download tool (standalone or delegated via `--command-server`)
- `cmd/xdcc-search/` — CLI multi-provider search tool
- `cmd/xdcc-browse/` — Interactive TUI for search → filter → select → download

## TOC

### Commands
- [Build](#build)
- [Test](#test)
- [Run](#run)
- [Lint/Format](#lintformat)

### Architecture
- [Internal Package Map](#internal-package-map)
- [Key Data Flows](#key-data-flows)
- [Event Pipeline](#event-pipeline)
- [Dual-Mode CLI](#dual-mode-cli)
- [Store Focused Interfaces](#store-focused-interfaces)
- [Goroutine Lifecycle Pattern](#goroutine-lifecycle-pattern)
- [Logging Rules (CRITICAL)](#logging-rules-critical)

---

## Build

```bash
task deps          # Go modules + npm install
task all           # Frontend build + all binaries
task build:server  # Single binary
task frontend:dev  # Hot-reload dev server on :5173 (proxies API to :8080)
```

**IMPORTANT:** Always rebuild frontend (`cd web && npm run build`) before building `xdcc-server` — the frontend is embedded via `go:embed` in `web/frontend.go`.

**CGO must remain disabled** (`CGO_ENABLED=0`) for all builds.

## Test

```bash
task test              # go test ./...
task test:race         # CRITICAL for concurrent code — run before committing
task test:cover        # Coverage HTML report
task test:package -- ./internal/entities  # Single package
go test -run TestName ./internal/package  # Single test
task test:verbose      # go test -v ./...
```

Race detector is disabled on arm64 machines before committing changes to concurrent code (IRC handlers, SSE, queue, downloader).

## Run

```bash
task run              # Build & start xdcc-server
task run:dl -- "/msg Bot xdcc send #5"
task run:search -- "search term"
task run:browse -- "search term"
task ci               # Full pipeline: deps → build → test:race → vet
```

## Lint/Format

```bash
task fmt       # go fmt ./...
task vet       # go vet ./...
task lint      # golangci-lint (optional dep)
task check     # fmt + vet + tidy + test + frontend typecheck
```

---

## Architecture

### Internal Package Map

| Package | Responsibility |
|---------|---------------|
| `api/` | REST API handlers (chi router), middleware, JSON responses |
| `bridge/` | Forwards IRC + queue events → SSE hub |
| `channellog/` | Channel-based log aggregation |
| `cli/` | Shared CLI utilities (verbosity levels) |
| `client/` | HTTP client for CLI → server delegation |
| `config/` | Config loading (YAML + env + CLI flags, merged by priority) |
| `diskmon/` | Disk space monitoring (platform-specific) |
| `downloader/` | DCC transfer orchestration |
| `entities/` | Core domain models (XDCCPack, IrcServer, parsing) |
| `irc/` | IRC protocol client (girc wrapper), DCC SEND/RESUME |
| `ircmanager/` | Multi-server persistent connection manager (daemon only) |
| `logging/` | Structured logger with levels, file rotation, SSE broadcast |
| `metrics/` | Runtime metrics collection |
| `notifier/` | External notifications (webhook, ntfy, pushover) |
| `pubsub/` | Generic typed pub/sub hub for internal event fan-out |
| `queue/` | Download queue orchestration (SQLite-backed, retry, parallelism) |
| `search/` | Search engine implementations (nibl, xdcc-eu, subsplease) |
| `searchagg/` | Search aggregation, caching, deduplication, presets, watchlists |
| `sse/` | Server-Sent Events hub + event type constants |
| `store/` | SQLite persistence (focused interfaces, schema migrations, backup) |

### Key Data Flows

```
CLI Standalone:    xdcc-dl → entities.XDCCPack → irc.Client → DCC transfer
CLI Delegated:     xdcc-dl --command-server → HTTP POST → server queue → download
Server:            Web UI → REST API → queue.Manager → ircmanager → irc.Client → DCC
```

### Event Pipeline

```
Download event → queue.Manager → pubsub.Hub → bridge.EventBridge → sse.Hub → SSE → Web UI
IRC event      → ircmanager    → pubsub.Hub → bridge.EventBridge → sse.Hub → SSE → Web UI
Log entry      → logging.LogBroadcaster → sse.Hub → SSE → Web UI log viewer
```

**SSE event types live in two places that MUST stay in sync:**
1. Go constants: `internal/sse/events.go`
2. Frontend listeners: `web/src/lib/api.js` SSEClient `eventTypes` array

### Dual-Mode CLI

All CLI tools (`xdcc-dl`, `xdcc-search`, `xdcc-browse`) work in two modes:
- **Standalone**: Direct IRC connection, no server needed
- **Delegated**: `--command-server=http://localhost:8080` delegates to running daemon

### Store Focused Interfaces

The `store` package defines focused interfaces (`ServerStore`, `DownloadStore`, `SearchCacheStore`, etc.) that expose only needed methods. **New code must depend on focused interfaces, not the composite `Store`.**

```go
// ✅ Correct: focused interface
func NewAggregator(store DownloadStore, cacheStore SearchCacheStore)

// ❌ Wrong: depends on whole Store
func NewAggregator(store Store)
```

### Goroutine Lifecycle Pattern

Use `sync.WaitGroup` for long-lived goroutines (not `done chan`):

```go
type Worker struct {
    wg        sync.WaitGroup   // Tracks active goroutines
    runningMu sync.Mutex       // Guards isRunning
    isRunning bool             // Prevents duplicate Start()
    ctx       context.Context
    cancel    context.CancelFunc
}

func (w *Worker) Start() {
    w.runningMu.Lock()
    if w.isRunning { w.runningMu.Unlock(); return }
    w.isRunning = true
    w.runningMu.Unlock()
    w.wg.Add(1)
    go w.run()
}

func (w *Worker) Stop() {
    w.cancel()
    w.wg.Wait()  // Guaranteed completion, no race
}
```

Drain forwarding goroutines on shutdown with 100ms timeout pattern (`bridge/`, `notifier/`).

### Logging Rules (CRITICAL)

- **ALWAYS** use `*logging.Logger` — never `*log.Logger`, `log.Printf`, or `log.Println`
- The `xdccirc.Logger` interface (used by `irc.Client`) is satisfied by `*logging.Logger.Printf`
- Constructor: `logging.New(level, filePath, rotateMB)` where `level` is `LevelDebug/Info/Warn/Error`
- Levels: Debug (verbose IRC), Info (normal ops), Warn (recoverable), Error (unrecoverable)

---

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to overcomplication, and clarifying questions come before implementation rather than after mistakes.
