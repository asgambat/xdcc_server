# Config Update Pattern: `SnapshotAndApply` + `ApplyPartial`

**Recommended pattern for atomic, race-free partial config updates in `xdcc-server`.**

## Overview

Runtime config updates (theme, message rate limit, channels, etc.) must:

1. **Atomically swap the in-memory config** so concurrent readers (`ircmanager`, `queue` workers, the rate limiter, the channel-log logger) see a consistent state.
2. **Persist only the changed fields** to disk, preserving comments, formatting, and field ordering in `config.yaml`.

Both goals are achieved by combining two helpers on `*config.Config`:

- **`SnapshotAndApply(fn func(snap *Config)) *Config`** — atomic snapshot + apply (closes the TOCTOU window — see below)
- **`ApplyPartial(path string, oldCfg *Config) error`** — surgical YAML diff/persist (preserves comments)

## The pattern

```go
// Atomic in-memory update: capture the pre-state under RLock, apply the
// user's mutation to a deep copy, and install it via Replace — all under
// the same lock. The returned *Config is the pre-snapshot, used as the
// "old" side of the diff below.
oldCfg := a.Config.SnapshotAndApply(func(snap *config.Config) {
    snap.UI.Theme = body.Theme
})

// Persist only the changed fields to disk, preserving YAML comments
// and formatting. Falls back to a full Save() if any change involves
// a non-scalar type.
if err := a.Config.ApplyPartial(a.ConfigPath, oldCfg); err != nil {
    writeError(w, http.StatusInternalServerError, "SAVE_ERROR", err.Error())
    return
}
```

### What `SnapshotAndApply` does

1. Takes a single `RLock` on the live config.
2. Deep-clones the live state **twice** under the same lock:
   - one clone becomes the pre-snapshot returned to the caller (`oldCfg`),
   - the other is handed to the closure for mutation.
3. Releases the lock.
4. Runs the closure, which may read fields from the clone (e.g. to preserve the live admin token) and mutate others (e.g. set the new theme).
5. Calls `Replace` to atomically install the mutated clone.
6. Returns the pre-snapshot.

### What `ApplyPartial` does

1. Computes a reflection-based diff between `oldCfg` and the live config (now post-mutation) under RLock.
2. Surgically updates only the changed **scalar** fields in the YAML node tree, preserving comments, ordering, and the user's existing formatting.
3. Falls back to a full `Save()` if any change involves a non-scalar type (slice, array, struct) — those can't be safely updated in-place.

## Why this matters: closing the TOCTOU window

The naive **two-`Clone()`** pattern opens a TOCTOU (time-of-check / time-of-use) race:

```go
// ❌ DO NOT USE — has a TOCTOU race between the two Clones
oldCfg := a.Config.Clone()  // T1: snapshot for the diff
newCfg := a.Config.Clone()  // T2: snapshot for the new value
newCfg.UI.Theme = "dark"
a.Config.Replace(newCfg)
a.Config.ApplyPartial(a.ConfigPath, oldCfg)
```

If a concurrent `Replace` lands between the two `Clone()` calls, `oldCfg` is stale and reflects a *different* state than the post-mutation live config. The diff computed by `ApplyPartial` then includes the unrelated concurrent changes, silently widening what gets written to disk:

```
T0:   live = {theme: "light", rate_limit: 5}
T1:   oldCfg = Clone(live)        // {theme: "light", rate_limit: 5}
T2:   concurrent handler changes rate_limit to 10
      live = {theme: "light", rate_limit: 10}
T3:   newCfg = Clone(live)        // {theme: "light", rate_limit: 10}  ← stale relative to T1
T4:   newCfg.UI.Theme = "dark"
T5:   Replace(newCfg)             // live = {theme: "dark", rate_limit: 10}
T6:   ApplyPartial(path, oldCfg)  // diff({light,5}, {dark,10}) persists BOTH fields
```

`SnapshotAndApply` closes this window by taking **both** clones under a single `RLock`. The pre-snapshot returned to the caller and the clone handed to the closure are guaranteed to share the same pre-mutation state, so the diff is always between (pre, post) of one atomic snapshot.

## When to use what

| Scenario | Recommended call |
|---|---|
| Single-field update (theme, token TTL, etc.) | `SnapshotAndApply` + `ApplyPartial` |
| Full structured config save (JSON API) | `SnapshotAndApply` + `ApplyPartial` (preserves YAML comments) |
| Raw YAML save from the Advanced editor | `SnapshotAndApply` (return value unused) + `SaveRaw` |
| Full rewrite (no formatting preservation needed) | `Save` |
| Bootstrap / first-time setup | `Save` |

### Why `SaveRaw` doesn't need the pre-snapshot

The Advanced YAML editor sends the **raw bytes** of the file. `SaveRaw` writes those bytes verbatim, so no diff is needed. `SnapshotAndApply` is still used (instead of plain `Replace`) so the in-memory swap and the read of the live admin token happen under a single lock — closing the same TOCTOU window in the in-memory path.

### Why `Save` is the right choice for bootstrap

Bootstrap (`handleSetupBootstrap`) is a one-shot, server-startup-only path. It runs before any concurrent reader is active, so it can use `Save` directly without `SnapshotAndApply`.

## Contract: don't call back into `c` from the closure

The closure passed to `SnapshotAndApply` receives a `*Config` (a deep copy with zero-value mutexes). It must:

- **Mutate the passed `*Config` only.**
- **Not call back into `c`** (e.g. `c.Clone`, `c.Replace`, `c.Get*`).

Calling back into `c` is safe (the helper's `RLock` is released before the closure runs, so no deadlock), but it defeats the purpose of having a single atomic snapshot — a concurrent `Replace` could land between the closure's read of `c.X` and the helper's `Replace(snap)`, and the install could clobber that change.

The helper also tolerates a `nil` `fn` (it snapshots, installs the unmodified clone, and returns the pre-snapshot) — useful for tests and idempotent no-op updates.

## Reference usage in the codebase

| File:line | Call site | What it does |
|---|---|---|
| `internal/api/handlers_system.go` :: `handleUpdateTheme` | `PATCH /api/config/theme` | Single-field update (theme only) |
| `internal/api/handlers_system.go` :: `handleUpdateConfig` (YAML branch) | `PUT /api/config` (text/yaml) | Raw editor save + admin token preservation |
| `internal/api/handlers_system.go` :: `handleUpdateConfig` (JSON branch) | `PUT /api/config` (application/json) | Structured partial save with admin token preservation |
| `internal/ircmanager/manager.go` :: `DownloadPack` | per-download setup | One-shot `Clone()` snapshot for the download worker (read-only, no install — does not need `SnapshotAndApply`) |
| `internal/api/handlers_system.go` :: `handleSetupBootstrap` | `POST /api/setup/bootstrap` | One-shot, pre-concurrency setup path. Uses `Save` directly (no `SnapshotAndApply`) because no concurrent reader is active yet |

## See also

- `internal/config/config.go` — definitions of `SnapshotAndApply`, `ApplyPartial`, `Replace`, `Clone`, `cloneLocked`
- `docs/ANALISI_BACKEND_BUG_CONCORRENZA.md` § 2.26 — the original analysis of `Config` thread-safety that motivated the mutex-protected struct and these helpers
