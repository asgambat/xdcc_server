# Plan 006: Honor the `notify_enabled` column when delivering watchlist notifications

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat d42d1e7..HEAD -- internal/searchagg/ internal/notifier/ internal/store/ internal/sse/`
> If any of those directories changed since this plan was written, compare
> the "Current state" excerpts against the live code before proceeding; on
> a mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `d42d1e7`, 2026-06-25

## Why this matters

The schema carries a per-watchlist `notify_enabled` column (added in
migration v6, `internal/store/schema.go:55-57`). The `Watchlist` Go
struct has the field (`internal/store/models.go:113`). The HTTP API
accepts `notify_enabled` in both the create and update payloads
(`internal/api/handlers_search.go:302, 393`). The notifier package
defines `EventWatchlistNewResults` and implements
`NotifyWatchlistResults` on all four provider types
(`internal/notifier/notifier.go:65, 271-273, 346-348, 414-416, 652-654`).
The SSE event type `EventWatchlistNewResults` exists
(`internal/sse/events.go:36`).

But the dispatcher in `searchagg` never reads the column. The
external-notification callback at `internal/searchagg/watchlist.go:160-162`
fires unconditionally on `wr.HasChanges && len(wr.NewPacks) > 0`, and
the SSE notification at `internal/searchagg/aggregator.go:218-219` fires
unconditionally when `wl.AutoEnqueue` is set. Users who explicitly
disable notifications for a watchlist in the UI still get spammed.

This is a one-line fix per call site: guard both calls with
`wl.NotifyEnabled`.

## Current state

Files in scope:

- `internal/searchagg/watchlist.go` — `RunWatchlist` callback
  (lines 160-162).
- `internal/searchagg/aggregator.go` — `runWatchlistSafely` SSE call
  (lines 218-219).

Current `RunWatchlist` callback (lines 160-162):

```go
if a.onWatchlistResults != nil && wr.HasChanges && len(wr.NewPacks) > 0 {
    a.onWatchlistResults(w.Name, len(wr.NewPacks), wr.Enqueued)
}
```

Current `runWatchlistSafely` SSE call (lines 217-220):

```go
// Notify via SSE about new enqueued downloads
if enqueued > 0 && wl.AutoEnqueue {
    a.notifyWatchlistResults(wl.Name, result.NewPacks)
}
```

`watchlist.go:160` does NOT check `wl.NotifyEnabled`; it should.
`aggregator.go:218` does NOT check `wl.NotifyEnabled`; it should.

`wl.NotifyEnabled` is already loaded by `GetEnabledWatchlists`
(`internal/store/sqlite.go:1043-1050` — SELECT includes
`notify_enabled`) and by `GetWatchlist` (line 967). So no extra
DB work is needed.

## Commands you will need

| Purpose          | Command                                | Expected on success |
|------------------|----------------------------------------|---------------------|
| Tests            | `task test`                            | all pass             |
| Single package   | `go test ./internal/searchagg/...`     | all pass, including new test |
| Race tests       | `go test -race ./internal/searchagg`   | no races             |
| Vet              | `task vet`                             | exit 0               |

## Scope

**In scope** (the only files you should modify):

- `internal/searchagg/watchlist.go` — add `wl.NotifyEnabled` guard to
  the external-notification callback.
- `internal/searchagg/aggregator.go` — add `wl.NotifyEnabled` guard
  to the SSE notify call.
- `internal/searchagg/watchlist_test.go` — add a test verifying
  callbacks are NOT called when `NotifyEnabled` is false.

**Out of scope** (do NOT touch):

- `internal/store/schema.go` (the column already exists).
- `internal/notifier/notifier.go` (the providers are correct; they
  already filter by `interested(events, EventWatchlistNewResults)`,
  and the dispatcher decides whether to call them at all).
- `internal/api/handlers_search.go` (the API already accepts
  `notify_enabled` and persists it).
- `internal/sse/events.go` (the event type already exists).
- The web UI (no change needed — `notify_enabled` is already plumbed
  end-to-end in the UI, it just wasn't honored).

## Git workflow

- Branch: `advisor/006-watchlist-notify-enabled`
- One commit. Message style: short, lowercase (e.g.
  `honor watchlist notify_enabled`).

## Steps

### Step 1: Guard the external-notification callback

In `internal/searchagg/watchlist.go`, replace the block at lines 160-162:

```go
if a.onWatchlistResults != nil && wr.HasChanges && len(wr.NewPacks) > 0 {
    a.onWatchlistResults(w.Name, len(wr.NewPacks), wr.Enqueued)
}
```

with:

```go
if a.onWatchlistResults != nil && wl.NotifyEnabled && wr.HasChanges && len(wr.NewPacks) > 0 {
    a.onWatchlistResults(w.Name, len(wr.NewPacks), wr.Enqueued)
}
```

**Verify**: `go build ./internal/searchagg/` → exit 0.

### Step 2: Guard the SSE notify call

In `internal/searchagg/aggregator.go`, replace the block at lines 217-220:

```go
// Notify via SSE about new enqueued downloads
if enqueued > 0 && wl.AutoEnqueue {
    a.notifyWatchlistResults(wl.Name, result.NewPacks)
}
```

with:

```go
// Notify via SSE about new enqueued downloads (respect notify_enabled).
if enqueued > 0 && wl.AutoEnqueue && wl.NotifyEnabled {
    a.notifyWatchlistResults(wl.Name, result.NewPacks)
}
```

**Verify**: `go build ./internal/searchagg/` → exit 0;
`go vet ./internal/searchagg/` → exit 0.

### Step 3: Add a regression test

In `internal/searchagg/watchlist_test.go`, find `TestWatchlistInFlight_ReentrancyGuard`
or similar `RunWatchlist`-based tests. Add a new test that confirms the
callback is NOT called when `NotifyEnabled=false`. Use the
`newTestAggregator` helper that already exists in the file (around
line 536) and a `trackedWatchlistStore` mock.

Sketch (adapt names to existing helpers):

```go
func TestRunWatchlist_NotifyDisabledSkipsCallback(t *testing.T) {
    ms := &trackedWatchlistStore{}
    agg := newTestAggregator(ms)

    var callbackCount int32
    agg.SetOnWatchlistResults(func(name string, newCount, enqueued int) {
        atomic.AddInt32(&callbackCount, 1)
    })

    wl := store.Watchlist{
        ID:            1,
        Name:          "test-wl",
        Query:         "anything",
        IntervalMinutes: 5,
        NotifyEnabled: false,  // <-- disabled
        Enabled:       true,
        AutoEnqueue:   false,  // not auto-enqueue so we can isolate callback path
    }
    ms.watchlists[wl.ID] = wl
    // Seed a fingerprint so HasChanges logic can run; the provider will
    // return no results, which is fine — we only care about callback gating.

    _, err := agg.RunWatchlist(context.Background(), wl)
    if err != nil {
        t.Fatalf("RunWatchlist: %v", err)
    }
    if atomic.LoadInt32(&callbackCount) != 0 {
        t.Errorf("callback called %d times; expected 0 (NotifyEnabled=false)", callbackCount)
    }
}

func TestRunWatchlist_NotifyEnabledFiresCallback(t *testing.T) {
    // Same as above but NotifyEnabled=true and a forced HasChanges path
    // (e.g. seed last_match_fingerprint = "" and ensure provider
    // returns at least one pack).
    // Assert callbackCount == 1.
}
```

Read `watchlist_test.go` to confirm the exact helpers (`newTestAggregator`,
`trackedWatchlistStore`, `SetOnWatchlistResults` or equivalent). If
they have different names, adapt. The goal: two tests, one for each
branch of the new guard.

**Verify**: `go test -run TestRunWatchlist_Notify ./internal/searchagg`
→ both new tests PASS.

### Step 4: Run full package test suite

**Verify**: `go test -race ./internal/searchagg/...` → all tests PASS,
no new race detector warnings.

## Test plan

- New tests (Step 3): two tests verifying the `notify_enabled` gate
  in both code paths (external notification callback + SSE notify).
- Existing tests in `internal/searchagg/...` must continue to pass —
  they use `NotifyEnabled: true` (default) implicitly or via the
  default `Watchlist{}` literal which has `false` for booleans.
- **Verify the existing test suite still passes**: if any existing
  test relied on the callback firing with a default
  `Watchlist{Enabled: true}` literal, it will start failing. Fix
  those tests by adding `NotifyEnabled: true` to their fixtures
  (this is the correct expectation: tests that exercise the
  notification path must explicitly opt in).

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `task vet` exits 0
- [ ] `go test -race ./internal/searchagg/...` exits 0, including new
      `TestRunWatchlist_Notify*` tests
- [ ] `task test` exits 0 (no regressions in other packages)
- [ ] `grep -n "wl.NotifyEnabled" internal/searchagg/watchlist.go
      internal/searchagg/aggregator.go` shows both files have at least
      one match in a guard condition
- [ ] `git status` shows modifications only inside `internal/searchagg/`
- [ ] `plans/README.md` status row updated to DONE

## STOP conditions

Stop and report back (do not improvise) if:

- The `RunWatchlist` callback or `runWatchlistSafely` SSE call don't
  match the "Current state" excerpts (drift).
- An existing test fails after the fix because it relied on the
  callback firing — fix the **test fixture** (add `NotifyEnabled: true`)
  and re-run. If more than two tests need fixture updates, STOP and
  report (the wiring may have been used differently than the excerpts
  show).
- `SetOnWatchlistResults` (or equivalent) doesn't exist on `Aggregator`
  with the signature you expect — STOP, find the real setter and adapt
  the test.

## Maintenance notes

- The check `wl.NotifyEnabled` should be evaluated before any other
  expensive work (search, dedup). The current placement is correct —
  after the work has run, but as a guard before side effects. If a
  future optimization wants to skip the run entirely when notifications
  are off, that's a separate decision and not in scope here.
- This plan does not add new columns, new API fields, or new event
  types. It only fixes a behavioral gap where the UI-supplied flag
  was being ignored. The DB, API, UI, and notifier layers were all
  already correct in isolation — only the dispatcher was wrong.
- Watch the `runWatchlistSafely` SSE notify: it currently gates on
  `AutoEnqueue` AND `NotifyEnabled`. The product intent is unclear:
  should SSE fire for *manual* enqueues? That's a UX decision; this
  plan preserves current behavior (SSE only fires for auto-enqueue).
  A follow-up could split SSE and external notification gates if
  product wants finer control.
