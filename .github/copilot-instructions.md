# GitHub Copilot Instructions

> **See [`agent.md`](../agent.md) for general AI and coding guidelines.**  
> This file contains **GitHub Copilot-specific** instructions only.

---

## Copilot Chat Preferences

### Code Suggestions

When generating code:
- Prefer concise explanations (avoid verbose commentary unless asked)
- Follow existing patterns in the codebase (check similar files first)
- Use existing types and functions from `internal/` packages rather than recreating logic
- When multiple approaches exist, briefly mention trade-offs

### Code Completion Behavior

When completing function implementations:
- Infer error handling from surrounding code
- Match logging style (use `logger.Info/Error/Debug` with key-value pairs)
- Use existing helper functions (e.g., `entities.ParseXDCCMessage()` for parsing)

### Test Generation

When generating tests:
- Use table-driven format (see `agent.md` for template)
- Include both success and error cases
- Add `t.Parallel()` for independent tests
- Use descriptive test names (`TestParseXDCCMessage_InvalidFormat`)

---

## Quick Reference

**Run tests only if allowed by user:**
```bash
do not run tests if not asked permission to run them
```

**Frontend + backend rebuild:**
```bash
cd web && npm run build && cd .. && go build ./cmd/xdcc-server
```

**Check if frontend needs rebuild:**
- If changes touch `web/src/`, always suggest `npm run build`
- If changes touch `internal/api/`, frontend rebuild not needed

---

## Context-Aware Suggestions

### When editing `internal/api/handlers_*.go`
- Use `api.writeError(w, status, code, msg)` for errors
- Extract request body with `json.NewDecoder(r.Body).Decode(&req)`
- Return JSON with `json.NewEncoder(w).Encode(resp)`
- Add handler registration to `router.go`

### When editing `internal/store/*.go`
- Follow naming: `Insert*`, `Update*`, `Get*ByID`, `List*`
- Use prepared statements for queries
- Return `sql.ErrNoRows` as-is (caller checks with `errors.Is`)

### When editing `cmd/*/main.go`
- Use cobra for CLI flag definitions
- Load config with `config.Load(configPath, flagOverrides)`
- Setup logging early in `main()`

### When editing `internal/irc/*.go`
- IRC operations are async; use channels + timeouts
- Don't block girc event handlers (spawn goroutines if needed)
- Wrap network errors with context

---

## Common Pitfalls to Avoid

❌ **Don't suggest `log.Println`** → Use `logger.Info/Error/Debug`  
❌ **Don't suggest CGO dependencies** → Must stay `CGO_ENABLED=0`  
❌ **Don't suggest naked returns in error paths** → Always wrap: `fmt.Errorf("context: %w", err)`  
❌ **Don't suggest global state in tests** → Use function params or test-specific instances  
❌ **Don't suggest database access outside `store/`** → Always go through `SQLiteStore` methods

---

## Workflow-Specific Hints

### When asked to "add a new API endpoint"
1. Create handler in `internal/api/handlers_*.go`
2. Register route in `internal/api/router.go`
3. Suggest updating web UI if user-facing
4. Suggest adding test in `api_integration_test.go`

### When asked to "add a new search engine"
1. Create `internal/search/<name>.go` implementing `search.Engine`
2. Add to provider list in `cmd/xdcc-server/main.go` (searchagg.New)
3. Add test in `internal/search/<name>_test.go`

### When asked to "modify the database schema"
1. Update `store/schema.go` table definitions
2. Add migration logic in `store/sqlite.go`
3. Suggest testing with existing database files

---

## Response Style

✅ **Preferred:**
- "I'll add the handler to `handlers_download.go` and register it in `router.go`."
- "This requires updating the SQLite schema. I'll add migration logic in `store/sqlite.go`."

❌ **Avoid:**
- Long explanations of basic Go concepts
- Suggesting alternative architectures without being asked
- Explaining what the existing code does (focus on the change)

---

For all other guidelines (architecture, error handling, testing, etc.), refer to [`agent.md`](../agent.md).
