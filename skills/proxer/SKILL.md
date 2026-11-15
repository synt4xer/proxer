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
- `mcp__proxer__search` — `{"keyword": "..."}`.
- `mcp__proxer__read` — `{"filepath": "...", "start": 40, "end": 70}`; `start` defaults to 1 and `end` to 60. Returns the file `hash`.
- `mcp__proxer__edit` — `{"filepath": "...", "start": 52, "end": 58, "content": "...", "hash": "..."}`; `hash` is the value from the read, and a mismatch rejects the edit.

The MCP server fixes the root when it starts, so its tools take no `--root` and every `filepath` is relative to that root. Every `search` and `read` result includes that absolute root, so when several `proxer` servers are connected the paths stay attributable. Everything below applies to both interfaces: the same matching, the same path containment, and the same auto-reindex after `edit`.

Use the shell binary when no MCP server is connected, when you need `--root` for another project, or when the harness only exposes the skill.

## Workflow

1. Run `index` once before the first search in a session.
2. `search` to find where something lives.
3. `read` that line range for full context.
4. `edit` the range with the `hash` from the read; the index updates itself.

## index

```bash
./.agents/skills/proxer/scripts/proxer index
```

Rebuilds `.proxer.db` from the project tree. It skips `node_modules`, `dist`, `.next`, `.agents`, and other dependency or build directories. A project can add to that list with a `.proxer.json` at its root, e.g. `{"ignored_dirs": ["third_party", "generated"]}`: names match whole directory names at any depth and add to the defaults, and a malformed file aborts the command. Indexed files are known source and document extensions (`.go`, `.ts`, `.py`, `.rs`, `.md`, `.json`, `.yaml`, `.sql`, `.sh`, and similar), plus exact names such as `Dockerfile`, `Makefile`, and `go.mod`, up to 1 MB each.

The index is a snapshot. Re-run `index` after `git checkout`, `git pull`, or any change made outside of `proxer edit`.

## search

```bash
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware"
```

Returns a JSON array of at most 5 matches, each with `root` (the absolute path of the indexed project), `filepath`, `line` (`"start-end"`), and `snippet` with `<b>` around the matched text.

How matching behaves:

- Terms match as **token prefixes**, so `-k "auth"` finds `AuthMiddleware`.
- CamelCase is not split, so `-k "middleware"` will **not** find `AuthMiddleware`. Start from the beginning of an identifier.
- Punctuation and FTS5 operators (`AND`, `OR`, `*`) are stripped, so natural language is safe but never errors.
- Multiple words are OR-ed and ranked by relevance. Prefer one distinctive identifier over a sentence.

Results come from 60-line chunks with a 15-line overlap, so `line` is the chunk boundary, not the exact hit. Use the snippet to see where inside the chunk the match sits.

## read

```bash
./.agents/skills/proxer/scripts/proxer read -f "internal/auth.go" -s 40 -e 70
```

Returns `{"root": "...", "filepath": "...", "content": "...", "hash": "..."}` for the inclusive range `-s` .. `-e`. Line numbers start at 1 and match what `search` reports. The `hash` is the SHA-256 of the whole file, not just the range.

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
- Files outside the indexed extensions are not searchable. Editing one works, but the change will not appear in `search`.
- Directories on the project skip list (`.proxer.json`) are not indexed, and `edit` on a file inside one will not re-add it to the index.
- `.proxer.db` belongs in the target project's `.gitignore`.
- Creating and deleting files is out of scope: `edit` only rewrites an existing line range.
- Concurrent writers queue instead of failing, but two processes editing the same file still leave only the last write. `edit` never leaves a half-written file; it does not merge concurrent changes. Pass the `hash` from `read` to catch a writer that changed the file and refuse the edit.
- `search` never uses regular expressions or file globs. Use `read` on a known path when you need exact text.
