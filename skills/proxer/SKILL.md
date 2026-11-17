---
name: proxer
description: Search, read, and edit a local codebase through a SQLite FTS5 index at the project root. Use when locating code by keyword instead of grepping, reading an exact line range instead of a whole file, or replacing a block of lines without rewriting the file. Available as a CLI and as an MCP stdio server whose tools are named index, search, read, and edit.
---

# Proxer

Proxer keeps a SQLite FTS5 index of the current project in `.proxer.db`. It replaces `grep` for discovery and replaces whole-file rewrites for edits.

The binary sits next to this file, at `.agents/skills/proxer/scripts/proxer`:

```bash
./.agents/skills/proxer/scripts/proxer <command>
```

Every command takes a global `--root <path>` flag. The index belongs to that root, not to the working directory, so pass it whenever you are not running from the project root:

```bash
./.agents/skills/proxer/scripts/proxer search -k "auth" --root /path/to/project
```

`--root` beats `PROXER_ROOT`, which beats the working directory. Relative `-f` paths resolve against the root, so `-f internal/auth.go` means the same file regardless of where the process started.

Each command prints JSON on stdout. Failures print to stderr and exit non-zero, so check the exit code before parsing the output.

## Two ways to drive it

If the harness has a `proxer` MCP server connected, prefer its tools over the shell binary. They return the same JSON without shell quoting, and `edit` takes the replacement as an argument instead of stdin. The tools are named `mcp__proxer__<tool>`:

- `mcp__proxer__index` — no arguments.
- `mcp__proxer__search` — `{"keyword": "..."}`, optionally `"limit"` (1..50, default 5) and `"kind"` (`code`, `docs`, or `all`). Refreshes the index first, like the CLI.
- `mcp__proxer__read` — `{"filepath": "...", "start": 40, "end": 70}`; `start` defaults to 1 and `end` to 60. Returns the file `hash`.
- `mcp__proxer__edit` — `{"filepath": "...", "start": 52, "end": 58, "content": "...", "hash": "..."}`; `hash` is the value from the read, and a mismatch rejects the edit.

The MCP server fixes the root when it starts, so its tools take no `--root` and every `filepath` is relative to that root. Every `search` and `read` result includes that absolute root, so when several `proxer` servers are connected the paths stay attributable. Everything below applies to both interfaces: the same matching, the same path containment, and the same auto-reindex after `edit`.

Use the shell binary when no MCP server is connected, when you need `--root` for another project, or when the harness only exposes the skill.

## Workflow

1. `search` to find where something lives. It refreshes the index first, so a database that does not exist yet is built and files written by other tools are found.
2. `read` that line range for full context.
3. `edit` the range with the `hash` from the read; the index updates itself.

## index

```bash
./.agents/skills/proxer/scripts/proxer index
```

Brings `.proxer.db` in line with the project tree and prints the counts it touched: `{"status": "indexed_successfully", "refreshed": {"added": 0, "changed": 0, "removed": 0, "skipped": 0}}`. The build is incremental, so a run with nothing to do is a stat-only walk that prints all zeros. `search` does the same work before every query, so this command is optional.

It skips `node_modules`, `dist`, `.next`, `.agents`, and other dependency or build directories. A project can add to that list with a `.proxer.json` at its root, e.g. `{"ignored_dirs": ["third_party", "generated"]}`: names match whole directory names at any depth and add to the defaults, and a malformed file aborts the command. Indexed files are known source and document extensions (`.go`, `.ts`, `.py`, `.rs`, `.md`, `.json`, `.yaml`, `.sql`, `.sh`, and similar), plus exact names such as `Dockerfile`, `Makefile`, and `go.mod`, up to 1 MB each. Files containing lines longer than 4,096 characters (such as minified JS bundles) are skipped at index time to prevent them from entering search results, and are reported in `skipped`.

## search

```bash
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware"
```

Returns an envelope:

```json
{
  "root": "...",
  "indexed_at": "2026-11-16T03:38:29.543151Z",
  "refreshed": {"added": 1, "changed": 0, "removed": 0},
  "total": 12,
  "truncated": true,
  "results": [{"root": "...", "filepath": "...", "line": "40-99", "snippet": "...<b>hit</b>...", "match": "content"}]
}
```

`results` holds at most `--limit` files, one per file: `root` is the absolute path of the indexed project, `line` is `"start-end"`, `snippet` wraps the matched text in `<b>`, and `match` is `path`, `content`, or `both`. `indexed_at` is when the index last changed. `refreshed` counts what this call added, changed, and removed; all zero means the index was already current. `total` counts every matching chunk and `truncated` says whether more matched than `results` holds, so a broad query is never silently cut.

`-n/--limit` asks for more files (default 5, clamped to 1..50). `--kind code|docs|all` filters by class: `all` is the default and returns code first, then documentation (`.md`, `.mdx`, `.txt`, `.rst`), then generated assets (`*.min.*`, `*.map`, lockfiles). Use `--kind code` when a docs-heavy tree answers a code question with prose.

Add `--no-refresh` (or set `PROXER_NO_REFRESH=1`) to query the index as it stands. Then `refreshed` is absent and a file changed since the last build stays invisible, which is only what you want for a pinned snapshot or a read-only mount.

How matching behaves:

- Terms match as **token prefixes**, so `-k "auth"` finds `AuthMiddleware`.
- Terms match the **file path as well as the body**, and a path that carries the term outranks a chunk that merely mentions it, so `-k "000001"` finds `db/migrations/mysql/000001_init.up.sql`.
- CamelCase is not split, so `-k "middleware"` will **not** find `AuthMiddleware`. Start from the beginning of an identifier.
- Punctuation and FTS5 operators (`AND`, `OR`, `*`) are stripped, so natural language is safe but never errors.
- Multiple words are OR-ed and ranked by relevance, then demoted by class. Prefer one distinctive identifier over a sentence.

Results come from 60-line chunks with a 15-line overlap, so `line` is the chunk boundary, not the exact hit. Use the snippet to see where inside the chunk the match sits. Five slots describe five different files, so one file that matched five times does not fill the answer.

## read

```bash
./.agents/skills/proxer/scripts/proxer read -f "internal/auth.go" -s 40 -e 70
```

Returns `{"root": "...", "filepath": "...", "content": "...", "hash": "..."}` for the inclusive range `-s` .. `-e`. Line numbers start at 1 and match what `search` reports. Lines exceeding 4,096 characters are truncated past that width with an explicit ` [truncated]` marker to keep payloads bounded. The `hash` is the SHA-256 of the whole file, not just the range or truncated content, so it reliably guards subsequent edits.

Read the range before editing it: `edit` needs exact boundaries. Carry the `hash` into the edit so a change in between is caught.

## edit

```bash
./.agents/skills/proxer/scripts/proxer edit -f "internal/auth.go" -s 52 -e 58 --hash "1f0c..." <<'EOF'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
EOF
```

Replaces lines `-s` .. `-e` (inclusive) with the content read from stdin, then re-indexes **only that file**. Other files keep their existing index entries, so the command stays fast no matter how large the project is. Trailing newlines in the replacement are trimmed, so do not add a blank line at the end of the block.

Pass the `hash` from the read as `--hash` (or the `hash` argument over MCP). If the file changed after that read, `edit` exits non-zero and writes nothing, so a stale range can never overwrite code you have not seen. Omitting `--hash` keeps the old behavior.

The replacement does not need to match the length of the range. The whole range is always consumed:

- To insert, replace a single line with that line plus the new text.
- An empty stdin leaves one blank line in place of the range, it does not remove the line count entirely.

## Limits

- Every path must stay inside the root. `read` and `edit` reject `../` and absolute paths that point at another project, because no index entry exists outside the root. Use the harness `read` or `bash` for those files.
- Lines exceeding 4,096 characters are truncated during `read` with an explicit ` [truncated]` marker to keep payloads bounded, and files containing lines past that width are skipped at index time.
- Files outside the indexed extensions are not searchable. Editing one works, but the change will not appear in `search`.
- Directories on the project skip list (`.proxer.json`) are not indexed, and `edit` on a file inside one will not re-add it to the index.
- `.proxer.db` belongs in the target project's `.gitignore`.
- Freshness is gated on size and modification time: a same-size write inside the same timestamp tick is not noticed until the file changes again. Everything the gate opens is confirmed by a content hash before it is re-chunked.
- Creating and deleting files is out of scope: `edit` only rewrites an existing line range.
- Concurrent writers queue instead of failing, but two processes editing the same file still leave only the last write. `edit` never leaves a half-written file; it does not merge concurrent changes. Pass the `hash` from `read` to catch a writer that changed the file and refuse the edit.
- `search` never uses regular expressions or file globs. Use `read` on a known path when you need exact text.
