# Plan 002: Restore `build:all` to build all four binaries

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat d42d1e7..HEAD -- Taskfile.yml`
> If `Taskfile.yml` changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: docs / build
- **Planned at**: commit `d42d1e7`, 2026-06-25

## Why this matters

`Taskfile.yml` declares four binaries (`xdcc-server`, `xdcc-dl`,
`xdcc-search`, `xdcc-browse`) in its `BINARIES` var (line 11) and
defines all four `build:*` tasks. The top-level `build:all` task (line
45-48) however only depends on `build:server`:

```yaml
build:all:
  desc: Build all binaries
  #deps: [build:server, build:dl, build:search, build:browse]
  deps: [build:server]
```

The intended four-binary line is right there, commented out. The result:
`task all`, `task build:all`, `task ci`, and `task release` all build
only `xdcc-server`. The `release-binaries.yml` workflow and `install`
task (which copies four binaries to `/usr/local/bin/`) can both produce
inconsistent outputs. `README.md` and `CLAUDE.md` both advertise "all
binaries" via `task all`, which is currently a lie. This is a 30-second
fix that prevents real release-pipeline drift.

## Current state

Files in scope:

- `Taskfile.yml` — the build system. Lines 45-48 hold the bug.

Current excerpt (lines 44-48):

```yaml
  build:all:
    desc: Build all binaries
    #deps: [build:server, build:dl, build:search, build:browse]
    deps: [build:server]
```

Surrounding context (for confirmation that the four `build:*` tasks
exist):

```yaml
  BINARIES: xdcc-server xdcc-dl xdcc-search xdcc-browse    # line 11

  build:server:  # line 50 — builds xdcc-server
  build:dl:      # line 63 — builds xdcc-dl
  build:search:  # line 74 — builds xdcc-search
  build:browse:  # line 85 — builds xdcc-browse
```

The four tasks are correctly defined; only the `build:all` aggregator is
broken.

## Commands you will need

| Purpose            | Command                       | Expected on success |
|--------------------|-------------------------------|---------------------|
| List Taskfile tasks| `task --list-all \| grep -E "^build"` | shows `build:all`, `build:server`, `build:dl`, `build:search`, `build:browse` |
| Build all          | `task build:all`              | exit 0, all 4 binaries in `bin/` |
| Confirm outputs    | `ls -1 bin/`                  | 4 entries: `xdcc-server`, `xdcc-dl`, `xdcc-search`, `xdcc-browse` |
| Validate YAML      | `task --list-all >/dev/null`  | exit 0 (catches YAML syntax errors) |

## Scope

**In scope** (the only files you should modify):

- `Taskfile.yml` — `build:all` task, lines 45-48.

**Out of scope** (do NOT touch):

- `release-binaries.yml` (`.github/workflows/`) — may build binaries
  separately; leave its workflow definition alone.
- `install` task in `Taskfile.yml` — already references all four
  binaries correctly; no change needed.
- `README.md`, `CLAUDE.md` — both already say "all binaries"; once
  the fix lands they become accurate.

## Git workflow

- Branch: `advisor/002-build-all`
- One commit is enough. Message style: short, lowercase, matching
  `git log --oneline -10` (e.g. `fix build:all deps`).

## Steps

### Step 1: Restore the four-binary deps on `build:all`

In `Taskfile.yml`, replace lines 45-48:

```yaml
build:all:
  desc: Build all binaries
  deps: [build:server, build:dl, build:search, build:browse]
```

(Remove the commented-out line.)

**Verify**: `task --list-all >/dev/null` → exit 0 (Taskfile.yml parses).

### Step 2: Run `task build:all` and confirm all four binaries are produced

**Verify**: `task build:all` → exit 0; `ls -1 bin/` shows four files
(`xdcc-server`, `xdcc-dl`, `xdcc-search`, `xdcc-browse`). Note:
`build:server` requires `frontend:build` and `frontend:check` deps, which
in turn run `cd web && npm install` if `node_modules` is missing. If you
hit a frontend error, that's pre-existing — re-check the worktree has
`web/node_modules` and `web/dist/`; if those exist locally the command
is fast.

If `task build:all` fails for reasons unrelated to the `deps:` change
(e.g. frontend `npm install` issues, missing toolchain), STOP and
report — those are pre-existing problems, not in scope.

### Step 3: Confirm `task all` (the user-facing alias) builds all four

The `all` task (line 38-40) chains to `build:all`, so once Step 2
passes, this follows.

**Verify**: `task all` → exit 0; same four binaries in `bin/`.

## Test plan

No new tests required — this is build-pipeline configuration. The
verification command (`task build:all` → 4 binaries) is the test.

If you want a sanity script, this one-liner is sufficient and may be
added to the project later (do NOT add it as part of this plan — out of
scope):

```sh
test "$(ls -1 bin/ | wc -l)" = "4"
```

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `task build:all` exits 0
- [ ] `ls -1 bin/` contains exactly `xdcc-server`, `xdcc-dl`,
      `xdcc-search`, `xdcc-browse`
- [ ] `grep -n "deps:" Taskfile.yml` shows `build:all` with four entries
      on a single line (no commented-out line above it)
- [ ] `git status` shows `Taskfile.yml` as the only modified file
- [ ] `plans/README.md` status row updated to DONE

## STOP conditions

Stop and report back (do not improvise) if:

- The `Taskfile.yml` `build:all` block does not match the "Current
  state" excerpt (the codebase has drifted since this plan was written).
- `task build:all` fails with a frontend (npm/svelte-check) error and
  you don't have `web/node_modules` — this is pre-existing and out of
  scope; report and stop.
- `task build:all` fails because of a Go compile error — STOP, that is
  not what this plan addresses.
- You find that the four `build:*` tasks are not all defined — STOP,
  this plan assumed `build:dl`, `build:search`, `build:browse` exist
  (they do at HEAD `d42d1e7`, lines 63-95).

## Maintenance notes

- If a fifth binary is added later (e.g. a new `cmd/<tool>/main.go`),
  the pattern is: (1) add `build:<tool>` task, (2) append it to the
  `BINARIES` var, (3) append it to `build:all` deps. There's no
  automation — keep this convention.
- The `release-binaries.yml` workflow may build binaries individually
  with its own matrix. That is correct as-is; do not unify it with
  `build:all` (different purposes, different cross-compilation needs).
