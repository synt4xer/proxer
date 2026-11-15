---
name: proxer
description: Search, read, and edit a local codebase through a SQLite FTS5 index at the project root. Use when locating code by keyword instead of grepping, reading an exact line range instead of a whole file, or replacing a block of lines without rewriting the file.
---

# Proxer

Proxer keeps a SQLite FTS5 index of the current project in `.proxer.db`. It replaces `grep` for discovery and replaces whole-file rewrites for edits.

Run every command from the project root. The binary sits next to this file, at `.agents/skills/proxer/scripts/proxer`:

```bash
./.agents/skills/proxer/scripts/proxer <command>
```

Each command prints JSON on stdout. Failures print to stderr and exit non-zero, so check the exit code before parsing the output.

## Workflow

1. Run `index` once before the first search in a session.
2. `search` to find where something lives.
3. `read` that line range for full context.
4. `edit` the range; the index updates itself.

## index

```bash
./.agents/skills/proxer/scripts/proxer index
```

Rebuilds `.proxer.db` from the project tree. It skips `node_modules`, `dist`, `.next`, and other dependency or build directories, and indexes only `.go`, `.ts`, and `.md` files.

The index is a snapshot. Re-run `index` after `git checkout`, `git pull`, or any change made outside of `proxer edit`.

## search

```bash
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware"
```

Returns a JSON array of at most 5 matches, each with `filepath`, `line` (`"start-end"`), and `snippet` with `<b>` around the matched text.

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

Returns `{"filepath": "...", "content": "..."}` for the inclusive range `-s` .. `-e`. Line numbers start at 1 and match what `search` reports.

Read the range before editing it: `edit` needs exact boundaries.

## edit

```bash
./.agents/skills/proxer/scripts/proxer edit -f "internal/auth.go" -s 52 -e 58 <<'EOF'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
EOF
```

Replaces lines `-s` .. `-e` (inclusive) with the content read from stdin, then re-indexes the project automatically. Trailing newlines in the replacement are trimmed, so do not add a blank line at the end of the block.

The replacement does not need to match the length of the range. The whole range is always consumed:

- To insert, replace a single line with that line plus the new text.
- An empty stdin leaves one blank line in place of the range, it does not remove the line count entirely.

## Limits

- Only `.go`, `.ts`, and `.md` files are indexed. Editing any other file works, but the change will not appear in `search`.
- `.proxer.db` belongs in the target project's `.gitignore`.
- `search` never uses regular expressions or file globs. Use `read` on a known path when you need exact text.
