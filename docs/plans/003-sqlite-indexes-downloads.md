# Plan 003: Add SQLite indexes for `downloads` history filters and `FilenamesExist` dedup

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat d42d1e7..HEAD -- internal/store/`
> If any file under `internal/store/` changed since this plan was written,
> compare the "Current state" excerpts against the live code before
> proceeding; on a mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: perf
- **Planned at**: commit `d42d1e7`, 2026-06-25

## Why this matters

`internal/store/sqlite.go:GetDownloadHistory` (lines 567-637) filters the
`downloads` table on five columns — `filename LIKE`, `bot LIKE`,
`file_size >=`/`<=`, and `date(completed_at) >=`/`<=` — yet the only
indexes on `downloads` today (`internal/store/schema.go:113-115`,
plus `idx_downloads_status_channel` at line 174) cover `status`,
`channel`, `(bot, server_address)`, and `(status, channel)`. Every
filter combination that does not match those exact prefixes falls back to
a full table scan. At 10k+ history rows each Web UI history page takes
100–500ms instead of <10ms.

A second hot path, `FilenamesExist` (`sqlite.go:683`), is used by
`searchagg.RunWatchlist` to deduplicate incoming packs against history.
It applies `LOWER()` to both sides of the comparison — SQLite does not
use a plain index on `filename` for `LOWER(filename) = LOWER(?)`, so
each watchlist run also scans the full table. SQLite 3.9+ supports
**expression indexes** which solve this exactly.

Adding four indexes via a single additive migration eliminates both hot
scans with no query rewrites required.

## Current state

Files in scope:

- `internal/store/schema.go` — `migrations` slice (starts line 28),
  `currentSchemaVersion = 8` (line 18), `initialSchema` const (lines
  ~30-180).
- `internal/store/sqlite.go` — `GetDownloadHistory` (lines 567-637) and
  `FilenamesExist` (line 683) read these filters; no edits required.

Current migration list (lines 28-67) ends at version 8
(`avg_speed_bps`). The new migration will be version 9.

Current indexes on `downloads` (already in `initialSchema`):

```sql
CREATE INDEX IF NOT EXISTS idx_downloads_status ON downloads(status);
CREATE INDEX IF NOT EXISTS idx_downloads_channel ON downloads(channel);
CREATE INDEX IF NOT EXISTS idx_downloads_bot_server ON downloads(bot, server_address);
-- (later) CREATE INDEX IF NOT EXISTS idx_downloads_status_channel ON downloads(status, channel);
```

Current `GetDownloadHistory` filter clauses (lines 580-608):

```go
if filter.Filename != "" {
    whereClauses = append(whereClauses, "filename LIKE ?")
    args = append(args, "%"+filter.Filename+"%")
}
if filter.Bot != "" {
    whereClauses = append(whereClauses, "bot LIKE ?")
    args = append(args, "%"+filter.Bot+"%")
}
if filter.DateFrom != "" {
    whereClauses = append(whereClauses, "date(completed_at) >= ?")
    args = append(args, filter.DateFrom)
}
if filter.DateTo != "" {
    whereClauses = append(whereClauses, "date(completed_at) <= ?")
    args = append(args, filter.DateTo)
}
```

Current `FilenamesExist` (lines 683-720) uses
`LOWER(?) = LOWER(filename)` on both sides — needs an expression index.

## Commands you will need

| Purpose            | Command                              | Expected on success |
|--------------------|--------------------------------------|---------------------|
| Tests              | `task test`                          | all pass             |
| Single package     | `go test ./internal/store/...`       | all pass, including any new test |
| Build              | `task build:server`                  | exit 0 (confirms schema.go compiles) |
| Vet                | `task vet`                           | exit 0               |

## Scope

**In scope** (the only files you should modify):

- `internal/store/schema.go` — bump `currentSchemaVersion` to `9`,
  append a new migration entry.

**Out of scope** (do NOT touch):

- `internal/store/sqlite.go` — query patterns are fine; SQLite will
  pick the new indexes automatically.
- Any other table's indexes. Only `downloads` is targeted here.
- `recovery.go`, `backup.go` — out of scope.

## Git workflow

- Branch: `advisor/003-downloads-indexes`
- One commit. Message style: short, lowercase (e.g. `add downloads indexes`).

## Steps

### Step 1: Bump `currentSchemaVersion` to 9

In `internal/store/schema.go`, change line 18:

```go
const currentSchemaVersion = 9
```

**Verify**: `go build ./internal/store/` → exit 0.

### Step 2: Append a new migration entry

In `internal/store/schema.go`, find the `migrations` slice (starts
line 28). After the existing version 8 entry (lines 65-67), add a new
entry:

```go
{
    version:     9,
    description: "Add indexes on downloads for history filters and watchlist dedup: (filename), (bot), (completed_at DESC), expression index on LOWER(filename)",
    up: `
CREATE INDEX IF NOT EXISTS idx_downloads_filename ON downloads(filename);
CREATE INDEX IF NOT EXISTS idx_downloads_bot ON downloads(bot);
CREATE INDEX IF NOT EXISTS idx_downloads_completed_at ON downloads(completed_at DESC);
CREATE INDEX IF NOT EXISTS idx_downloads_lower_filename ON downloads(LOWER(filename));
`,
},
```

Notes on choices:

- `idx_downloads_filename` and `idx_downloads_bot` help prefix-style
  LIKE (`%foo%` still does a scan over the index, which is bounded by
  row count, not page count — much faster than the heap on large
  tables).
- `idx_downloads_completed_at DESC` matches the
  `ORDER BY completed_at DESC` in `GetDownloadHistory` (line 631).
- `idx_downloads_lower_filename` is an expression index that lets
  `LOWER(?) = LOWER(filename)` in `FilenamesExist` (line 710) hit an
  index instead of scanning.

If the `migrations` slice uses Go struct literal syntax without trailing
commas, add the comma to the version-8 entry too. Confirm the file
parses.

**Verify**: `go build ./internal/store/` → exit 0.

### Step 3: Run the package tests (which exercise migrations)

The test harness creates a template DB via `Migrate()` in `TestMain`
(`main_test.go:35`), then each test copies it. Running the existing
test suite exercises the new migration on a fresh DB.

**Verify**: `go test ./internal/store/...` → all tests pass, including
the `TestMain` migration smoke test.

### Step 4: Add a focused index-exists test (optional but recommended)

In `internal/store/sqlite_test.go`, add a small test that opens a store
and verifies the new indexes exist (uses SQLite's
`sqlite_master`/`pragma index_list`).

```go
func TestMigrations_DownloadsIndexes(t *testing.T) {
    s := newTestStore(t)
    defer s.Close()

    rows, err := s.DB().QueryContext(context.Background(),
        `SELECT name FROM sqlite_master
         WHERE type='index' AND tbl_name='downloads'
         ORDER BY name`)
    if err != nil { t.Fatalf("list indexes: %v", err) }
    defer rows.Close()

    want := map[string]bool{
        "idx_downloads_status":          false,
        "idx_downloads_channel":         false,
        "idx_downloads_bot_server":      false,
        "idx_downloads_status_channel":  false,
        "idx_downloads_filename":        false,
        "idx_downloads_bot":             false,
        "idx_downloads_completed_at":    false,
        "idx_downloads_lower_filename":  false,
    }
    for rows.Next() {
        var name string
        if err := rows.Scan(&name); err != nil { t.Fatalf("scan: %v", err) }
        delete(want, name)
    }
    for name := range want {
        t.Errorf("missing index: %s", name)
    }
}
```

Note: confirm the exact `DB()` accessor name on `*SQLiteStore` by
reading the struct definition before writing this test. If the
accessor is named differently (e.g. `db()` or there is no public
accessor), adapt accordingly — the goal is to confirm the indexes
exist after migration.

**Verify**: `go test -run TestMigrations_DownloadsIndexes ./internal/store/`
→ PASS.

If the test setup is awkward (no public DB accessor), SKIP this step
and rely on Step 3's full-suite pass — the test is nice-to-have, not
required.

## Test plan

- Existing `internal/store` tests must continue to pass (Step 3).
- The new `TestMigrations_DownloadsIndexes` (Step 4) verifies the
  indexes were created.
- After merging, run `task test:race` once to confirm no regressions.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `currentSchemaVersion == 9` (`grep -n "currentSchemaVersion" internal/store/schema.go`)
- [ ] `go build ./internal/store/` exits 0
- [ ] `go test ./internal/store/...` exits 0
- [ ] `task vet` exits 0
- [ ] `git status` shows only `internal/store/schema.go` (and
      optionally `internal/store/sqlite_test.go`) modified
- [ ] `plans/README.md` status row updated to DONE

## STOP conditions

Stop and report back (do not improvise) if:

- The `migrations` slice in `internal/store/schema.go` doesn't end at
  version 8 (drift).
- A test other than the new one fails after the migration is added —
  STOP, do not fix unrelated test failures.
- SQLite rejects one of the index statements at runtime (e.g. syntax
  error on expression index). Confirm `modernc.org/sqlite` is
  ≥ 1.34 (it ships SQLite ≥ 3.45, which supports expression
  indexes since 3.9.0). If it does reject, report and stop.
- The `DB()` accessor used in Step 4 doesn't exist and no good
  alternative is obvious — skip Step 4 and rely on Step 3.

## Maintenance notes

- Expression indexes (`LOWER(filename)`) must use the same expression
  in the query. If `FilenamesExist` ever changes its comparison
  (e.g. drops `LOWER()`), this index becomes unused — keep them in
  sync.
- `idx_downloads_completed_at DESC` mirrors the `ORDER BY` in
  `GetDownloadHistory`. If the ordering changes, the index becomes
  near-useless — revisit.
- The migration is additive (no destructive changes). Existing DBs at
  version 8 will upgrade; new DBs will run version 9 from scratch.
- Watchlist dedup (`FilenamesExist`) and download history
  (`GetDownloadHistory`) are the two confirmed hot paths. If other
  queries on `downloads` surface (e.g. by-channel stats), revisit.
