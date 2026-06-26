# Plan 004: Require admin-token auth on the `/api/events` SSE stream

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> **Drift check (run first)**:
> `git diff --stat d42d1e7..HEAD -- internal/api/router.go internal/api/api.go internal/api/handlers_sse.go web/src/lib/api.js`
> If any of those files changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: security
- **Planned at**: commit `d42d1e7`, 2026-06-25

## Why this matters

`GET /api/events` is registered on `chi` at `internal/api/router.go:123`,
**before** the protected group that applies `RequireAdminToken`
(`router.go:130-131`). The SSE handler (`internal/api/handlers_sse.go`)
performs no auth check of its own. Anyone with network access to the
server can open a long-lived EventSource and read:

- Download progress, filenames, sizes, bot names, server addresses,
  error messages.
- IRC server status, channel joins.
- Log entries — including private IRC messages if
  `log_private_messages: true` is set in config (see
  `docs/ANALISI_BACKEND_BUG_CONCORRENZA.md` for context).
- Watchlist and search notifications.

This is an info-leak (no write capability via SSE), but real data about
user downloads, bot networks, and private messages should not be
unauthenticated. The fix is small: gate `/api/events` behind admin-token
auth, accepting the token via either the existing `X-Admin-Token` header
or a `?token=` query parameter (browsers' `EventSource` cannot set
custom headers, so the query-param form is required for the existing
web UI to keep working).

## Current state

Files in scope:

- `internal/api/router.go` — line 123 registers `/api/events` outside
  the protected group at lines 130-131.
- `internal/api/api.go` — `RequireAdminToken` middleware at lines 229-246
  (constant-time header comparison).
- `internal/api/handlers_sse.go` — `handleEvents` (lines 18-...) reads
  no token; just sets SSE headers and subscribes to the hub.
- `web/src/lib/api.js` — `SSEClient` constructs the EventSource URL.

Current router snippet (lines 121-131):

```go
// =====================================================================
// SSE events stream (Fase 7.1)
// =====================================================================
r.Get("/api/events", a.handleEvents) // GET /api/events

// =====================================================================
// Protected System/Admin routes
// =====================================================================
r.Group(func(r chi.Router) {
    r.Use(RequireAdminToken(a.Config.Security.AdminToken))
    // ...
})
```

Current `RequireAdminToken` (`api.go:229-246`):

```go
func RequireAdminToken(expected string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if expected == "" {
                writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "admin token not configured")
                return
            }
            got := r.Header.Get("X-Admin-Token")
            if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
                writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid admin token")
                return
            }
            next.ServeHTTP(w, r)
        })
    }
}
```

Current frontend SSE URL (from `web/src/lib/api.js`,
`SSEClient.open()` — verify exact line):

```js
const url = `${base}/api/events`;
this.es = new EventSource(url);
```

The frontend does NOT currently send any token.

## Commands you will need

| Purpose          | Command                                  | Expected on success |
|------------------|------------------------------------------|---------------------|
| Build            | `task build:server`                      | exit 0               |
| Tests            | `task test`                              | all pass             |
| Single package   | `go test ./internal/api/...`             | all pass             |
| Vet              | `task vet`                               | exit 0               |
| Manual smoke     | `bin/xdcc-server &` then `curl -N -H "X-Admin-Token: $TOK" http://localhost:8080/api/events` | 200 + SSE stream |
| Manual 401       | `curl -N -i http://localhost:8080/api/events` | `HTTP/1.1 401 Unauthorized` |

## Scope

**In scope** (the only files you should modify):

- `internal/api/api.go` — add `RequireAdminTokenQuery` middleware that
  accepts the token via either `X-Admin-Token` header or `?token=`
  query parameter.
- `internal/api/router.go` — move `r.Get("/api/events", …)` into the
  protected group, OR add `r.Use(RequireAdminTokenQuery(...))` on a
  sub-route just for `/api/events`.
- `web/src/lib/api.js` — update `SSEClient` to include the admin token
  in the query string when opening the EventSource.
- `internal/api/api_integration_test.go` — add tests for both 200
  (with token) and 401 (without) cases.

**Out of scope** (do NOT touch):

- The list of event types (`internal/sse/events.go`) — no change.
- The frontend store contract — no change.
- Any other SSE implementation detail (event replay, log broadcast) —
  no change.
- TLS / Origin / CORS for SSE — separate concern, see also Plan 013 if
  filed (out of scope here).

## Git workflow

- Branch: `advisor/004-sse-auth`
- One commit per logical step is fine, or one combined commit at the end.
- Message style: short, lowercase, matching `git log --oneline -10`.

## Steps

### Step 1: Add `RequireAdminTokenQuery` middleware

In `internal/api/api.go`, immediately after the existing
`RequireAdminToken` function (around line 246), add a sibling:

```go
// RequireAdminTokenQuery is like RequireAdminToken but also accepts the
// admin token via a "token" query parameter. This is required for SSE
// because browser EventSource cannot set custom HTTP headers.
//
// SECURITY: the token will appear in server access logs and browser
// history. Mitigations: (1) the token is only useful with network
// access to the daemon (treat as bearer credential); (2) advise
// operators to terminate TLS at a reverse proxy and strip the query
// string in upstream logs.
func RequireAdminTokenQuery(expected string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if expected == "" {
                writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "admin token not configured")
                return
            }
            got := r.Header.Get("X-Admin-Token")
            if got == "" {
                got = r.URL.Query().Get("token")
            }
            if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
                writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid admin token")
                return
            }
            next.ServeHTTP(w, r)
        })
    }
}
```

**Verify**: `go build ./internal/api/` → exit 0.

### Step 2: Move `/api/events` under the protected group

In `internal/api/router.go`, cut the line `r.Get("/api/events",
a.handleEvents)` (line 123) from its current public location and paste
it inside the protected `r.Group(...)` (after line 131). Change its
middleware to `RequireAdminTokenQuery`:

Find the protected group (around line 130-131):

```go
r.Group(func(r chi.Router) {
    r.Use(RequireAdminToken(a.Config.Security.AdminToken))

    // Logs
    r.Get("/api/logs", a.handleLogs)
    // ...
})
```

Add a separate SSE-specific protected group **before** (or after) the
existing one (keep changes localized; do not refactor the larger
group):

```go
// SSE — uses query-param auth because EventSource can't set headers.
r.Group(func(r chi.Router) {
    r.Use(RequireAdminTokenQuery(a.Config.Security.AdminToken))
    r.Get("/api/events", a.handleEvents)
})
```

Remove the original `r.Get("/api/events", a.handleEvents)` line that
was at line 123. Delete the now-empty section comment if it becomes
uninformative.

**Verify**: `go build ./internal/api/` → exit 0;
`grep -n "/api/events" internal/api/router.go` shows exactly one match
and it is inside an `r.Use(RequireAdminTokenQuery(...))` block.

### Step 3: Update `SSEClient` to send the token

In `web/src/lib/api.js`, find `SSEClient` and the URL construction
(grep for `EventSource` or `/api/events`). Modify so the URL includes
the stored admin token (assumed to live in `localStorage` or a
similar place — search for `admin_token` / `AdminToken` in the same
file to find the existing accessor).

If the frontend already has a function `getAdminToken()` (or
similar), use it; otherwise read it from `localStorage.getItem('admin_token')`
after verifying that's the storage convention (check
`web/src/lib/api.js` and `stores.js`).

Change the URL construction from:

```js
const url = `${base}/api/events`;
```

to:

```js
const token = getAdminToken();
const url = token
    ? `${base}/api/events?token=${encodeURIComponent(token)}`
    : `${base}/api/events`;
```

Where `getAdminToken()` is the existing accessor. If none exists, add
the simplest one (e.g. `localStorage.getItem('admin_token')`) and note
in a comment that this assumes the auth flow already populates it.

**Verify**: `cd web && npx svelte-check --tsconfig ./jsconfig.json --fail-on-warnings`
→ exit 0.

### Step 4: Add integration tests

In `internal/api/api_integration_test.go`, find the existing pattern
for auth tests (search for `RequireAdminToken` or `401` in test
names). Add three tests modeled on that pattern:

```go
func TestSSE_EventsRequiresAuth(t *testing.T) {
    // Build API, then issue GET /api/events without any token.
    // Expect: 401 UNAUTHORIZED.
}

func TestSSE_EventsAcceptsHeaderToken(t *testing.T) {
    // Set admin token in config, issue GET with X-Admin-Token header.
    // Expect: 200 + Content-Type: text/event-stream.
}

func TestSSE_EventsAcceptsQueryToken(t *testing.T) {
    // Set admin token in config, issue GET with ?token= query.
    // Expect: 200 + Content-Type: text/event-stream.
}
```

Adapt exact assertions to match the test harness used in the rest of
`api_integration_test.go`. If no such harness exists, follow the
pattern of any existing handler test in the same file.

**Verify**: `go test -run TestSSE_Events ./internal/api/...` → 3 PASS.

### Step 5: Run full test suite + race

**Verify**: `task test:race` → exit 0, all tests pass.

## Test plan

- New tests (Step 4): three integration tests covering 401 (no token),
  200 via header, 200 via query string.
- Existing tests in `internal/api/...` must continue to pass.
- Manual smoke (optional, document in commit message): start
  `xdcc-server`, run `curl -N -i http://localhost:8080/api/events`
  → expect `HTTP/1.1 401`.

## Done criteria

Machine-checkable. ALL must hold:

- [ ] `task vet` exits 0
- [ ] `go test ./internal/api/...` exits 0, including new
      `TestSSE_Events*`
- [ ] `task test:race` exits 0
- [ ] `cd web && npx svelte-check --tsconfig ./jsconfig.json --fail-on-warnings`
      exits 0
- [ ] `grep -n "/api/events" internal/api/router.go` shows exactly one
      match, inside a `RequireAdminTokenQuery` group
- [ ] `git status` shows modifications only inside `internal/api/`,
      `web/src/lib/api.js`, and (if needed) `web/src/lib/stores.js`
- [ ] `plans/README.md` status row updated to DONE

## STOP conditions

Stop and report back (do not improvise) if:

- The router registration or `RequireAdminToken` does not match the
  "Current state" excerpts (drift).
- The frontend has no admin-token accessor and the plan's assumed
  `getAdminToken()` is not the right name — find the correct one or
  ask the reviewer; do not invent a new storage convention.
- `svelte-check` fails for reasons unrelated to this change.
- The existing `SSEClient` does something more complex than the
  excerpt suggests (e.g. sends tokens via `fetch` first to obtain a
  cookie, then opens EventSource) — STOP, the auth design may
  already be partially in place and needs a different fix.

## Maintenance notes

- The query-string token will appear in access logs (nginx, journald,
  etc.). Document this in README "Security" section as a follow-up —
  out of scope here, but operators should know. The constant-time
  compare still applies; the only thing that changes is the attack
  surface for log exfiltration.
- If a future PR moves from SSE to WebSockets, the `?token=` query
  parameter approach still works for the initial upgrade request, and
  the same middleware can be reused.
- The token is also sent in `Last-Event-ID` on reconnect (browser
  EventSource behavior), but that header is set by the browser, not
  by JS — it doesn't leak the token.
- A stricter design (cookie-based auth via a `Set-Cookie` on a
  separate login endpoint) would eliminate the query-string token
  entirely; that's a larger refactor — note it as a future
  improvement, do not pursue here.
