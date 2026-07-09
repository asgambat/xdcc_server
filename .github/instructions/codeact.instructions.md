---
applyTo: "**"
---

## CodeAct — sandboxed Python (backend: monty)

Use codeact **instead of** chained tool calls when a task reads ≥8 files,
cross-references file sets, or needs ≥5 sequential tool calls.

### Invoke

```bash
C:\Users\asgambat\.copilot\installed-plugins\copilot-skills\codeact/scripts/codeact --auto --raw --workspace . --code '<python>'
```

### Critical rules

1. **One bash call, one program.** Write a single Python program that globs,
   reads, analyzes, and prints results. Do NOT scout with view/glob before
   invoking codeact. Do NOT retry with extra tool calls if the first run
   has a minor issue — fix the program instead.
2. **Return types** — getting these wrong causes retries:
   - `glob` → **list of strings** like `["src/app.py", ...]`
   - `view` → **string** (file content)
   - `mcp_call` → **string**
   - `bash` → **dict** with stdout/stderr/returncode
3. **No `os.path`, no `os.walk`** — use `glob` to find files, `view` to read them.
4. **`glob` auto-excludes** `.venv`, `node_modules`, `__pycache__`, `.git`, `target`,
   `dist`, `build`, and similar directories. If you need files from those dirs,
   pass `exclude_dirs=[]` to disable filtering.
5. **Wrap file reads in try/except.** Print partial results as you go.
6. **Use double quotes in Python code** to avoid shell quoting conflicts with
   `--code '...'`. Write `"string"` not `'string'` inside codeact programs.
7. Skip codeact for ≤5 files or single grep→view→done workflows.
8. **Keep output minimal.** Only print what the caller actually needs —
   summaries, counts, and key findings. Do NOT dump raw file contents or
   hundreds of unfiltered lines. If a list could be long, truncate or
   aggregate inside the program. The output must fit comfortably in a
   single tool response (~5 KB) to avoid extra read round-trips.

Tools are called as regular Python functions: `view(path="f.py")`, `glob(pattern="**/*.py")`, etc.

### Monty limitations (avoid retries)

Monty runs a Python subset. These **will error**:
- `f"{x:<10}"` or any f-string format spec → use `+` with manual padding
- `"{:<10}".format(x)` → no `str.format()`
- `class Foo:` → no classes
- `match x:` → no match/case
- `str.startswith()` with tuple → use `or`
- Set comprehensions → use `list` + `in`
- `os.path`, `os.walk` → use `glob()` and `view()` instead

**MCP from inside sandbox:** Use `mcp_call(server="name", tool="tool", ...)` to call MCP servers. Both `mcp_call()` and `web_fetch()` work inside the sandbox.

### Sandbox tools: view, create, edit, glob, bash, sql, grep, web_fetch, mcp_call

### Sandbox tools (call as Python functions, keyword args only)  - `view(path=..., view_range='...')` ù Read file contents or list a directory - `create(path=..., file_text=...)` ù Create a new file with the given content - `edit(path=..., old_str=..., new_str=...)` ù Replace exactly one occurrence of old_str with new_str - `glob(pattern=..., paths='.', exclude_dirs='...')` ù Find files matching a glob pattern - `bash(command=..., timeout=30)` ù Execute a shell command - `sql(query=..., db_path=':memory:')` ù Execute a SQL query against a SQLite database - `grep(pattern=..., paths='.', glob='...', context_lines=0)` ù Search file contents with ripgrep - `web_fetch(url=..., method='GET', headers='...', data='...', max_length=20000)` ù Fetch a URL - `mcp_call(server=..., tool=..., **kwargs)` ù Call an MCP server tool (available when .mcp.json is configured) 

