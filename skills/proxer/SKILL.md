---
name: proxer
description: Search, read, and edit a local codebase via a SQLite FTS5 index. Use when locating code by keyword instead of dumping grep output, reading bounded line ranges, or making hash-guarded edits without rewriting whole files. Available as a CLI binary and as an MCP stdio server exposing index, search, read, edit, and status.
---

# Proxer

Proxer indexes a local codebase in SQLite FTS5 and returns bounded JSON payloads. Every command writes JSON to stdout; errors write to stderr and exit non-zero.

## Choosing MCP vs CLI

Prefer the `proxer` MCP server when connected — it avoids shell quoting, passes `edit` content as a JSON argument instead of stdin, and fixes the project root at startup:

- `mcp__proxer__index` — `{}`
- `mcp__proxer__search` — `{"keyword": "...", "limit": 5, "kind": "all", "max_per_file": 1}` (`limit` 1..50, `kind` `all|code|docs`, `max_per_file` 1..5; 2s refresh cooldown)
- `mcp__proxer__read` — `{"filepath": "...", "start": 1, "end": 60, "numbered": false}`
- `mcp__proxer__edit` — `{"filepath": "...", "content": "...", "hash": "..."}` with either `"start"`/`"end"` (line-range mode) or `"old_content"` + optional `"occurrence"` (anchor mode)
- `mcp__proxer__status` — `{"vacuum": false}`

Use the CLI binary when no MCP server is connected or when targeting another root:

```bash
./.agents/skills/proxer/scripts/proxer <command>
```

Global flags:

- `--root <path>` — project root (precedence: `--root` → `PROXER_ROOT` → cwd). Relative `-f` paths resolve against this root.
- `--db <path>` — explicit SQLite index path (precedence: `--db` → `PROXER_DB` → project `.proxer.db` or harness `<skillDir>/database/<repo>-<hash12>.db`).

## When to use Proxer

- **Use `search`** when the file is unknown or a query may match many files (returns bounded, ranked chunks instead of dumping every line).
- **Use `read` + `edit`** for targeted line-range reads and hash-guarded atomic edits.
- **Use `grep` / direct read** when the file path is already known or you need regex or file-glob matching.

## Core workflow

1. **`search`** to locate relevant files and chunks (auto-refreshes the index first).
2. **`read`** the target line range (add `-n`/`numbered: true` when you need exact line numbers) and note the full-file SHA-256 `hash`.
3. **`edit`** the range or anchor block with `--hash` / `hash`; Proxer writes atomically and reindexes only that file.

## Commands

### `index`

```bash
./.agents/skills/proxer/scripts/proxer index
```

Incrementally updates the index and reports `status`, `refreshed` (`added`, `changed`, `removed`, `skipped`, plus optional `ignored_dirs`, `skipped_by_reason`), and `db_size_bytes`. Optional because `search` runs the same incremental refresh before querying.

Skips `.gitignore` matches, built-in ignored directories (`node_modules`, `.git`, `.next`, `.agents`, `vendor`, `.venv`, etc.), extra `ignored_dirs` in `<root>/.proxer.json`, sensitive files (unless `index_sensitive: true` in `.proxer.json`), files over 1 MB, and minified/long-line blobs.

### `search`

```bash
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware" [-n 5] [--kind all] [--no-refresh]
```

- `-k, --keyword` (required): query terms. Matches token prefixes against both file paths and content, splits `camelCase`/`PascalCase` tail segments (3+ chars) on code/config files, runs `AND`-first with `OR` fallback, and strips punctuation and FTS5 operators. No regex or globs.
- `-n, --limit`: max results, 1..50 (default `5`).
- `--kind`: `all` (default: production code → tests → docs → generated assets), `code` (production code → tests), or `docs`.
- `--no-refresh`: skip pre-search refresh (also `PROXER_NO_REFRESH=1`).

Returns `root`, `indexed_at`, `refreshed` (omitted when refresh is skipped), `total`, `truncated`, optional `stale`/`stale_reason`, and `results`:

```json
{
  "root": "/abs/path",
  "indexed_at": "<rfc3339>",
  "refreshed": {"added": 0, "changed": 0, "removed": 0, "skipped": 0},
  "total": 12,
  "truncated": true,
  "results": [
    {"root": "/abs/path", "filepath": "internal/auth.go", "chunk": "40-99", "line": 52, "snippet": "...<b>hit</b>...", "match": "content", "hash": "<sha256>"}
  ]
}
```

Chunks are 60 lines with 15-line overlap. `chunk` is the chunk range; `line` is the 1-based matched line when available; `match` is `path`, `content`, or `both`.

### `read`

```bash
./.agents/skills/proxer/scripts/proxer read -f "internal/auth.go" -s 40 -e 70 [-n]
```

- `-f, --filepath` (required), `-s, --start` (default `1`), `-e, --end` (default `60`), `-n, --numbered` (prefix each line with `N\t`).
- Returns `root`, `filepath`, `start_line`, `end_line`, `total_lines`, `truncated`, `next_start` (when more lines remain), `content`, and full-file SHA-256 `hash`.
- Lines over 4,096 characters are truncated with ` [truncated]`. Responses cap at 64 KB (`truncated: true`, continue from `next_start`).

### `edit`

Line-range mode (CLI reads replacement from stdin):

```bash
./.agents/skills/proxer/scripts/proxer edit -f "internal/auth.go" -s 52 -e 58 --hash "<sha256>" <<'EOF'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
EOF
```

Anchor mode (mutually exclusive with `-s`/`-e`; replaces `--old` verbatim):

```bash
printf '%s' 'return requireSession(next)' | ./.agents/skills/proxer/scripts/proxer edit -f "internal/auth.go" --old "return oldHandler(next)" --hash "<sha256>"
```

- Pass `--occurrence N` (1-based) when `--old` matches multiple times; default `0` requires a unique match.
- Always pass `--hash` (from `read` or `search`) so edits fail instead of overwriting concurrent changes.
- Line-range mode trims trailing `\r\n` from the replacement (empty stdin leaves one blank line; to insert, include the replaced line plus new text). Anchor mode splices stdin/`content` verbatim.
- Refuses edits targeting lines over 4,096 characters or containing ` [truncated]`.
- Returns `status`, `message`, and updated `hash` (plus `reindex_failed`/`reindex_error` if post-write reindex failed).

### `status` (alias `doctor`)

```bash
./.agents/skills/proxer/scripts/proxer status [--vacuum] [--prune]
```

Inspects database path, size, permissions/mode, schema version, indexed counts, ignore sources, and orphaned harness DBs without walking the workspace. `--vacuum` runs FTS5 optimize, `VACUUM`, and WAL checkpoint; `--prune` (CLI only) removes orphaned harness DBs.

## Limits & Rules

- **Root confinement**: `read` and `edit` reject paths resolving outside the project root (including symlinks).
- **Existing files only**: `edit` cannot create or delete files.
- **Unindexed files**: files outside the extension/exact-name allowlist or inside ignored directories are not searchable; `edit` modifies them without adding them to the index.
- **Strict config**: malformed `<root>/.proxer.json` aborts the command instead of falling back.
- **Freshness**: delta refresh checks file size and mtime, then verifies content hash before re-chunking.
