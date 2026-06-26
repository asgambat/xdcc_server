# Plan 005: Complete `.env.example` with all configuration variables

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat d42d1e7..HEAD -- .env.example internal/config/config.go config.yaml`
> If `.env.example` or the upstream config changed since this plan was
> written, compare the "Current state" excerpts against the live code
> before proceeding; on a mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: dx
- **Planned at**: commit `d42d1e7`, 2026-06-25

## Why this matters

`.env.example` at the repository root documents only 5 environment
variables (`UID`, `GID`, `XDCC_HTTP_PORT`, `XDCC_HTTP_BIND_ADDRESS`,
`XDCC_LOGGING_LEVEL`, `XDCC_SECURITY_ADMIN_TOKEN`). The codebase
defines ~25 environment-overridable settings — every field in
`internal/config/config.go` with an `env:` struct tag (lines
120-271). Self-hosters following the README's Docker Compose path
(which references `docker-compose.yml` and the `.env` file) discover
search, IRC rate-limiting, download, storage, UI, and profiling
settings only by reading `config.yaml` line-by-line. This plan
generates a complete `.env.example` matching the actual struct tags,
so operators can `cp .env.example .env` and have every available
knob documented with sensible defaults.

## Current state

Files in scope:

- `.env.example` (at repo root) — currently 17 lines.
- `internal/config/config.go` — all `env:"..."` struct tags (lines
  120-271) are the source of truth.

Current `.env.example` (verified, repo root):

```
# ── Non-root user ────────────────────────────────────────────
# UID=1000
# GID=1000
# ── HTTP server ──────────────────────────────────────────────
# XDCC_HTTP_PORT=8080
# XDCC_HTTP_BIND_ADDRESS=0.0.0.0
# ── Logging ──────────────────────────────────────────────────
# XDCC_LOGGING_LEVEL=info
# ── Security ─────────────────────────────────────────────────
# XDCC_SECURITY_ADMIN_TOKEN=
```

Env-mapped variables in `internal/config/config.go` (the source of
truth):

| Env var                                | Section     | Type   | Default (from `config.yaml`) |
|----------------------------------------|-------------|--------|------------------------------|
| `XDCC_IRC_NICKNAME`                    | irc         | string | `xdcc-user` |
| `XDCC_IRC_GREETINGS`                   | irc         | []str  | `[]` |
| `XDCC_IRC_ENABLE_MESSAGE_SEND`         | irc         | bool   | `false` |
| `XDCC_IRC_MESSAGE_RATE_LIMIT`          | irc         | int    | `5` |
| `XDCC_IRC_MESSAGE_RATE_WINDOW_SEC`     | irc         | int    | `60` |
| `XDCC_IRC_LOG_PRIVATE_MESSAGES`        | irc         | bool   | `false` |
| `XDCC_HTTP_PORT`                       | http        | int    | `8080` |
| `XDCC_HTTP_BIND_ADDRESS`               | http        | string | `127.0.0.1` |
| `XDCC_HTTP_TRUST_PROXY`                | http        | bool   | `false` |
| `XDCC_DOWNLOAD_TEMP_DIR`               | download    | string | `./downloads/tmp` |
| `XDCC_DOWNLOAD_DEST_DIR`               | download    | string | `./downloads/complete` |
| `XDCC_DOWNLOAD_CONFLICT_POLICY`        | download    | string | `skip` |
| `XDCC_DOWNLOAD_FAIL_FALLBACK`          | download    | string | `suggest_only` |
| `XDCC_DOWNLOAD_MAX_PARALLEL`           | download    | int    | `5` |
| `XDCC_DOWNLOAD_MAX_RATE_BPS`           | download    | int64  | `0` (unlimited) |
| `XDCC_DOWNLOAD_MIN_DISK_SPACE`         | download    | int64  | `1073741824` |
| `XDCC_DOWNLOAD_MAX_RETRY`              | download    | int    | `3` |
| `XDCC_DOWNLOAD_STARTUP_DELAY_MINUTES`  | download    | int    | `0` |
| `XDCC_DOWNLOAD_CHANNEL_JOIN_DELAY`     | download    | int    | `-1` |
| `XDCC_SEARCH_PROVIDER_TIMEOUT`         | search      | int    | `5` |
| `XDCC_SEARCH_PAGE_SIZE`                | search      | int    | `50` |
| `XDCC_SEARCH_CACHE_ENABLED`            | search      | bool   | `true` |
| `XDCC_STORAGE_DB_PATH`                 | storage     | string | `./db` |
| `XDCC_STORAGE_DOWNLOADS_RETENTION`     | storage     | string | `30d` |
| `XDCC_STORAGE_CLEANUP_INTERVAL`        | storage     | string | `12h` |
| `XDCC_STORAGE_BUSY_TIMEOUT_MS`         | storage     | int    | `2000` |
| `XDCC_LOGGING_LEVEL`                   | logging     | string | `info` |
| `XDCC_LOGGING_FILE_PATH`               | logging     | string | `""` (stderr) |
| `XDCC_UI_SETUP_COMPLETED`              | ui          | bool   | `false` |
| `XDCC_UI_THEME`                        | ui          | string | `dark` |
| `XDCC_SECURITY_ADMIN_TOKEN`            | security    | string | auto-generated if empty |
| `XDCC_SECURITY_TOKEN_TTL_MINUTES`      | security    | int    | `15` |
| `XDCC_PROFILING_BLOCK_RATE`            | profiling   | int    | `0` |
| `XDCC_PROFILING_MUTEX_FRACTION`        | profiling   | int    | `0` |

## Commands you will need

| Purpose            | Command                                | Expected on success |
|--------------------|----------------------------------------|---------------------|
| Tests              | `task test`                            | all pass             |
| Build              | `task build:server`                    | exit 0               |

(`.env.example` itself is not consumed by any test or build — it is
documentation. The verification is "does the file exist with all
variables present, one per line?".)

## Scope

**In scope** (the only files you should modify):

- `.env.example` (at repo root) — overwrite with the comprehensive
  version.

**Out of scope** (do NOT touch):

- `config.yaml` — the canonical reference; `.env.example` is a
  companion.
- `internal/config/config.go` — adding/removing struct tags is a
  separate concern.
- `README.md` — references `.env.example` correctly already (Docker
  section); no change needed.
- `docker-compose.yml` — also correct already.
- `.env` (if it exists locally) — never committed; do not create it.

## Git workflow

- Branch: `advisor/005-env-example`
- One commit. Message style: short, lowercase (e.g.
  `expand .env.example with all config vars`).

## Steps

### Step 1: Rewrite `.env.example`

Replace the existing 17-line `.env.example` with the comprehensive
version below. All entries are commented out so the file is inert by
default — operators uncomment to set.

```bash
# ============================================================
# xdcc_server — environment variables
# ============================================================
# Copia questo file in .env e modifica a piacere:
#   cp .env.example .env
#
# Tutte le variabili sono opzionali. Se non impostate, vengono
# usati i default di config.yaml.
# ============================================================

# ── Non-root user (Docker) ────────────────────────────────────
# UID=1000
# GID=1000

# ============================================================
# IRC
# ============================================================
# Base nickname (viene aggiunto un suffisso random).
# XDCC_IRC_NICKNAME=xdcc-user
# Greeting list (separata da virgola). Vuoto = default "hello everybody".
# XDCC_IRC_GREETINGS=
# Abilita invio messaggi via web UI (true | false).
# XDCC_IRC_ENABLE_MESSAGE_SEND=false
# Rate limit: massimo N messaggi per finestra.
# XDCC_IRC_MESSAGE_RATE_LIMIT=5
# XDCC_IRC_MESSAGE_RATE_WINDOW_SEC=60
# Logga i messaggi privati ricevuti dal bot.
# XDCC_IRC_LOG_PRIVATE_MESSAGES=false

# ============================================================
# HTTP server
# ============================================================
# XDCC_HTTP_PORT=8080
# XDCC_HTTP_BIND_ADDRESS=127.0.0.1
# ATTENZIONE: trust_proxy=true abilita IP spoofing via X-Forwarded-For.
# Abilitare SOLO dietro reverse proxy fidato che riscrive l'header.
# XDCC_HTTP_TRUST_PROXY=false

# ============================================================
# Download
# ============================================================
# XDCC_DOWNLOAD_TEMP_DIR=./downloads/tmp
# XDCC_DOWNLOAD_DEST_DIR=./downloads/complete
# XDCC_DOWNLOAD_CONFLICT_POLICY=skip
# XDCC_DOWNLOAD_FAIL_FALLBACK=suggest_only
# XDCC_DOWNLOAD_MAX_PARALLEL=5
# 0 = illimitato.
# XDCC_DOWNLOAD_MAX_RATE_BPS=0
# Byte. Default: 1 GiB.
# XDCC_DOWNLOAD_MIN_DISK_SPACE=1073741824
# XDCC_DOWNLOAD_MAX_RETRY=3
# XDCC_DOWNLOAD_STARTUP_DELAY_MINUTES=0
# -1 = random 5-10s, 0 = nessun delay, >0 = secondi fissi.
# XDCC_DOWNLOAD_CHANNEL_JOIN_DELAY=-1

# ============================================================
# Search
# ============================================================
# XDCC_SEARCH_PROVIDER_TIMEOUT=5
# XDCC_SEARCH_PAGE_SIZE=50
# XDCC_SEARCH_CACHE_ENABLED=true

# ============================================================
# Storage (SQLite)
# ============================================================
# XDCC_STORAGE_DB_PATH=./db
# XDCC_STORAGE_DOWNLOADS_RETENTION=30d
# XDCC_STORAGE_CLEANUP_INTERVAL=12h
# XDCC_STORAGE_BUSY_TIMEOUT_MS=2000

# ============================================================
# Logging
# ============================================================
# debug | info | warn | error
# XDCC_LOGGING_LEVEL=info
# Vuoto = stderr.
# XDCC_LOGGING_FILE_PATH=

# ============================================================
# UI
# ============================================================
# XDCC_UI_SETUP_COMPLETED=false
# dark | light
# XDCC_UI_THEME=dark

# ============================================================
# Security
# ============================================================
# Se vuoto, viene generato random all'avvio. Genera con:
#   openssl rand -hex 32
# XDCC_SECURITY_ADMIN_TOKEN=
# XDCC_SECURITY_TOKEN_TTL_MINUTES=15

# ============================================================
# Profiling
# ============================================================
# 0 = disabilitato. Vedi docs/pprof-guide.md.
# XDCC_PROFILING_BLOCK_RATE=0
# XDCC_PROFILING_MUTEX_FRACTION=0
```

**Verify**: `wc -l .env.example` → at least 80 lines (the version
above is ~95 lines). `grep -c "^# XDCC_" .env.example` ≥ 30.

### Step 2: Sanity check no env var was missed

Run the following one-liner to list every env tag defined in
`config.go` and confirm each appears in `.env.example`:

```sh
grep -oE 'env:"XDCC_[A-Z_]+"' internal/config/config.go \
  | sort -u \
  | sed 's/env:"//; s/"$//' \
  | while read v; do
      if ! grep -q "^# ${v}=" .env.example; then
        echo "MISSING: $v"
      fi
    done
```

Expected output: no `MISSING:` lines.

If any var is reported missing, add it to the appropriate section in
`.env.example` and re-run. **Do not** modify `config.go`.

### Step 3: Run the build/test pipeline

**Verify**: `task vet` and `task build:server` both exit 0 (sanity
check that nothing in the toolchain consumed `.env.example` and broke).

## Test plan

No automated tests for documentation files. The verification in
Step 2 (script to grep all `env:` tags and check each is present in
`.env.example`) is the test.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `wc -l .env.example` reports ≥ 80 lines
- [ ] `grep -c "^# XDCC_" .env.example` reports ≥ 30 distinct
      env-tagged entries
- [ ] The Step 2 one-liner reports zero `MISSING:` lines
- [ ] `task vet` exits 0
- [ ] `task build:server` exits 0
- [ ] `git status` shows only `.env.example` modified
- [ ] `plans/README.md` status row updated to DONE

## STOP conditions

Stop and report back (do not improvise) if:

- The current `.env.example` has been substantively modified since
  this plan was written (drift).
- The Step 2 script reports a `MISSING:` line for a var that the plan
  did not list — that means `config.go` has a new tag; add it and
  re-run.
- A config var type is unclear (e.g. `[]string` with no obvious
  delimiter) and a maintainer decision is needed — STOP and ask.

## Maintenance notes

- `.env.example` should be re-synced whenever a new `env:` tag is
  added to `internal/config/config.go`. Consider adding a CI check
  that runs the Step 2 script (out of scope for this plan, but a
  clean follow-up).
- This plan does NOT add the auto-generation tooling (e.g. a
  `task gen:env-example` task). If that's wanted, it's a separate
  plan and should use the `env:` tags as the source of truth.
- The file is in Italian-flavored comments to match the existing
  style (`config.yaml`, `README.md`). Keep that consistent in future
  edits.
