# Plan 001: Make `channellog.Logger.Close` idempotent under concurrent calls

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat d42d1e7..HEAD -- internal/channellog/`
> If any file under `internal/channellog/` changed since this plan was
> written, compare the "Current state" excerpts against the live code
> before proceeding; on a mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `d42d1e7`, 2026-06-25

## Why this matters

`channellog.Logger.Close()` is currently called from shutdown paths in
`cmd/xdcc-server/main.go` and from the periodic reconcile loop. If two
goroutines race into `Close()` (e.g. a SIGTERM handler plus the
context-cancellation path), the existing implementation's `select { case
<-l.stopCh: default: close(l.stopCh) }` pattern is not atomic — both
goroutines can pass the `default` branch before either calls `close()`,
and the second `close(l.stopCh)` panics with `close of closed channel`,
taking the whole daemon down. The panic surfaces as
`docs/ANALISI_BACKEND_BUG_CONCORRENZA.md §2.14` — a HIGH-severity
finding. A `sync.Once` makes the operation safe and idempotent, which is
the documented contract (`Close is safe to call multiple times`).

## Current state

Files in scope:

- `internal/channellog/channellog.go` — `Logger.Close` (lines 278-308),
  `Logger` struct (lines 45-52), `New` constructor (lines 67-80).
- `internal/channellog/channellog_test.go` — existing tests use
  `defer l.Close()` repeatedly; no concurrent-Close coverage.

Current `Logger` struct (lines 45-52):

```go
type Logger struct {
    dir string

    mu    sync.Mutex
    files map[string]*channelFile

    stopCh chan struct{} // closed by Close() to stop the sync goroutine
}
```

Current `Logger.Close` (lines 278-308) — the unsafe pattern:

```go
func (l *Logger) Close() error {
    if l == nil {
        return nil
    }
    // Stop the background sync goroutine first (idempotent via close).
    select {
    case <-l.stopCh:
        // Already closed.
    default:
        close(l.stopCh)   // <-- race window: two goroutines both reach here
    }
    // ... file close loop under l.mu ...
}
```

The bug: `select { default: }` only checks the channel state at the
instant of the select. Two concurrent `Close()` calls can both see
`default` and both execute `close(l.stopCh)`.

## Commands you will need

| Purpose          | Command                              | Expected on success |
|------------------|--------------------------------------|---------------------|
| Tests            | `task test`                          | all pass             |
| Race tests       | `task test:race`                     | all pass             |
| Single package   | `go test -race ./internal/channellog` | all pass            |
| Format           | `task fmt`                           | exit 0               |
| Vet              | `task vet`                           | exit 0               |

(All commands match `Taskfile.yml`.)

## Scope

**In scope** (the only files you should modify):

- `internal/channellog/channellog.go` — add `sync.Once`, init in `New`,
  use in `Close`.
- `internal/channellog/channellog_test.go` — add `TestClose_ConcurrentSafe`.

**Out of scope** (do NOT touch):

- `internal/channellog/channellog.go` `Log`/`LogPrivate` race vs. `Close`
  (tracked separately as `docs/ANALISI_BACKEND_BUG_CONCORRENZA.md §2.15`
  and §2.16 — a different fix and out of scope here).
- `channellog.ReconcileChannels` lifecycle (separate concern).
- Any other package. The `channellog` package is standalone.

## Git workflow

- Branch: `advisor/001-channellog-close-once`
- One commit per logical step is fine, or one combined commit at the end.
- Message style: match recent history — short, lowercase, no Conventional
  Commits prefix (see `git log --oneline -10`).

## Steps

### Step 1: Add `sync.Once` field to `Logger` struct

In `internal/channellog/channellog.go`, modify the `Logger` struct
(currently lines 45-52):

```go
type Logger struct {
    dir string

    mu    sync.Mutex
    files map[string]*channelFile

    stopCh chan struct{}
    closeOnce sync.Once // guards close(stopCh) so concurrent Close is safe
}
```

**Verify**: `go vet ./internal/channellog/` → exit 0.

### Step 2: Initialize `closeOnce` in `New`

In `internal/channellog/channellog.go`, the `New` function (around line 67-80)
currently initializes `stopCh`. `sync.Once` is zero-value ready, so no
change is strictly needed for the zero value, but for clarity add an
explicit comment to the struct field (done in Step 1) so readers know
the field must NOT be copied (it embeds a `sync.Mutex`-equivalent state).

**Verify**: `go build ./internal/channellog/` → exit 0.

### Step 3: Replace the unsafe `select` with `sync.Once.Do` in `Close`

In `internal/channellog/channellog.go`, replace the body of `Close()`
(lines 282-288). The new body:

```go
func (l *Logger) Close() error {
    if l == nil {
        return nil
    }
    // sync.Once makes Close safe under concurrent callers: the inner
    // func runs exactly once even if multiple goroutines race here
    // (e.g. SIGTERM handler + context cancellation).
    l.closeOnce.Do(func() {
        close(l.stopCh)
    })

    l.mu.Lock()
    defer l.mu.Unlock()
    // ... existing file-close loop unchanged ...
}
```

Keep the `l.mu`-guarded file-close loop as-is. Only the `stopCh` close
moves into `sync.Once.Do`.

**Verify**: `go build ./internal/channellog/` → exit 0;
`go vet ./internal/channellog/` → exit 0.

### Step 4: Add a concurrent-Close regression test

In `internal/channellog/channellog_test.go`, add a new test after the
existing `TestClose_NilSafe` (around line 166-172). Model after the
existing `TestLog_ConcurrentWrites` (line 108) for style.

```go
func TestClose_ConcurrentSafe(t *testing.T) {
    dir := t.TempDir()
    l, err := New(dir)
    if err != nil {
        t.Fatalf("New: %v", err)
    }
    // Spawn N goroutines that all call Close simultaneously.
    const N = 50
    var wg sync.WaitGroup
    wg.Add(N)
    for i := 0; i < N; i++ {
        go func() {
            defer wg.Done()
            _ = l.Close()
        }()
    }
    wg.Wait()
    // No panic means success. A second single Close must also be a no-op.
    if err := l.Close(); err != nil {
        t.Errorf("second Close: %v", err)
    }
}
```

You'll need to add `"sync"` to the test file's imports if not already
present (check the top of the file).

**Verify**: `go test -race -run TestClose_ConcurrentSafe ./internal/channellog`
→ PASS, no panic, no race detected.

### Step 5: Run full package and race tests

Run the full package test suite under the race detector.

**Verify**: `go test -race ./internal/channellog` → all existing tests
(including the new `TestClose_ConcurrentSafe`) PASS, no panic.

## Test plan

- **New test**: `TestClose_ConcurrentSafe` (Step 4) — 50 goroutines
  call `Close()` simultaneously; must not panic; second sequential
  `Close()` returns no error.
- **Existing tests**: `TestLog_CreatesFile`, `TestLog_ConcurrentWrites`,
  `TestClose_NilSafe`, `TestReconcileChannels_*` must continue to pass.
- **Pattern**: model after `TestLog_ConcurrentWrites` (line 108) which
  already uses `sync.WaitGroup` for goroutine fan-in.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `task fmt` exits 0
- [ ] `task vet` exits 0
- [ ] `go test -race ./internal/channellog` exits 0 with all tests passing
- [ ] `task test:race` exits 0 (full repo, no new races)
- [ ] `grep -n "default:" internal/channellog/channellog.go | grep -A1 "stopCh"` returns no matches in `Close`
- [ ] `git status` shows modifications only inside `internal/channellog/`
- [ ] `plans/README.md` status row updated to DONE

## STOP conditions

Stop and report back (do not improvise) if:

- The `Logger` struct or `Close` function in
  `internal/channellog/channellog.go` does not match the "Current state"
  excerpts (the codebase has drifted since this plan was written).
- A test panics for a reason unrelated to the `Close` race (e.g. a
  pre-existing test failure).
- The fix appears to require touching `ReconcileChannels` or any other
  out-of-scope function.
- `sync.Once` is already present in the file (then the fix is partial
  or already landed — report and stop).

## Maintenance notes

- `sync.Once` zero value is safe to use without initialization, but the
  field must NOT be copied (which would reset the state). Document this
  on the struct (a single-line comment is enough — already in Step 1).
- If `Close` ever needs to do more than `close(stopCh)` plus the file
  loop (e.g. flushing metrics), put it all inside `closeOnce.Do` so it
  remains atomic.
- Related open finding: `Log`/`LogPrivate` can reopen a file after
  `Close` (ANALISI §2.15-§2.16) — a separate fix. If that work is
  scheduled, it must land before any code path can call `Close`
  followed by `Log`.
