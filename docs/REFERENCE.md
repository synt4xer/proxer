# CLI & Configuration Reference

This document covers Proxer's architecture, installation, CLI commands, JSON schemas, configuration, and limitations. For Agent Skill and MCP server setup, see [INTEGRATION.md](INTEGRATION.md).

## Local-First Architecture & Root Resolution

Proxer is designed to be installed **locally inside the target project** (e.g., `.agents/skills/proxer`) rather than globally. This avoids naming collisions, allows per-project version pinning, and isolates each project's `.proxer.db`.

Every invocation resolves the **project root** in this order:

```
--root <path>  →  PROXER_ROOT  →  working directory
```

The SQLite FTS5 database lives at `<root>/.proxer.db`, and every stored path is relative to `<root>`. Harnesses can spawn `proxer` from any directory or set `PROXER_ROOT` without the index drifting. Relative `-f` paths resolve against `<root>` as well.

## Compilation & Installation

### Build & Test

```bash
make test
make build
```

`make build` compiles a CGO-free static binary powered by `modernc.org/sqlite`.

### Install into a Target Project

Point `DEST` to the root of the project where your agent works:

```bash
make install DEST=/path/to/my-awesome-project
```

This installs the skill package as a single unit and appends `.proxer.db*` to `$(DEST)/.gitignore` if not already present:

```
/path/to/my-awesome-project/.agents/skills/proxer/
├── SKILL.md
└── scripts/proxer
```

For a global skill shared across multiple projects, run `make install DEST=$HOME` and pass `--root` or set `PROXER_ROOT`.

## Command Reference

All commands output JSON on stdout and write errors to stderr with a non-zero exit code. Every command accepts `--root <path>`.

| Command | Purpose |
| --- | --- |
| `index` | Bring `.proxer.db` in line with the project tree (new, changed, and deleted files; skips minified/long-line blobs). |
| `search -k <keyword> [-n <limit>] [--max-per-file <count>] [--kind code\|docs\|all] [--no-refresh]` | Refresh the index, then return up to `limit` distinct files (default `5`, max `50`) with up to `max-per-file` non-overlapping chunks per file (default `1`, max `5`). |
| `read -f <file> -s <start> -e <end>` | Return `root`, `filepath`, the inclusive line range (lines >4,096 chars truncated with ` [truncated]`), and file `hash`. |
| `edit -f <file> -s <start> -e <end> [--hash <hash>]` | Replace the line range with stdin content, then reindex only that file. |
| `mcp` | Serve `index`, `search`, `read`, and `edit` as MCP tools over stdio. |

### `index`

```bash
./.agents/skills/proxer/scripts/proxer index
```

The build is incremental: files whose size and modification time match the manifest are skipped, unchanged byte hashes are only re-stamped, and deleted paths are removed. Files dominated by lines longer than 4,096 characters (such as minified bundles) are skipped and counted in `skipped`, while multi-line source files with isolated long lines are indexed with those lines truncated.

You do not need to run `index` manually before `search`, because `search` refreshes automatically. Run `index` upfront when preparing a large repo or after a big `git pull`.

### `search`

```bash
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware"
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware" --root ~/code/my-awesome-project
```

Example response:

```json
{
  "root": "/home/dev/code/my-awesome-project",
  "indexed_at": "2026-11-16T03:38:29.543151Z",
  "refreshed": {"added": 1, "changed": 2, "removed": 0, "skipped": 0},
  "total": 12,
  "truncated": true,
  "results": [
    {
      "root": "/home/dev/code/my-awesome-project",
      "filepath": "internal/auth.go",
      "line": "40-99",
      "snippet": "...<b>AuthMiddleware</b>...",
      "match": "content"
    }
  ]
}
```

Field notes:
- `root`: Absolute path of the indexed project.
- `indexed_at`: Timestamp when the index last changed (not when it was last checked).
- `refreshed`: Counts of files added, changed, removed, and skipped during the pre-query refresh (omitted when `--no-refresh` or `PROXER_NO_REFRESH=1` is active).
- `total` / `truncated`: Total matching chunks and whether results were capped by `limit`. `results` is `[]` (never `null`) when nothing matches.
- `match`: `path`, `content`, or `both`. Path-only hits match the filename/path even if the chunk text does not contain the term.
- `line`: The 60-line chunk boundary (15-line overlap), not the exact hit line.
- `-n/--limit`: Maximum results returned (`1..50`, default `5`).
- `--max-per-file`: Maximum non-overlapping chunks returned from any single file (`1..5`, default `1`).
- `--kind`: `all` (default: production code, tests, docs, assets), `code`, or `docs`.
- `--no-refresh` / `PROXER_NO_REFRESH=1`: Query the existing index without walking the filesystem (useful for read-only mounts or pinned snapshots).

Query details:
- Every term is matched as a token prefix (`auth` matches `AuthMiddleware`).
- On code and config files, `camelCase` and `PascalCase` identifiers are also sub-tokenized into trailing segments of at least 3 characters (`middleware` matches `AuthMiddleware`).
- Multi-term queries run `AND`-first and fall back to `OR` when `AND` yields no results.
- Free-form punctuation and FTS5 operators are sanitized automatically so natural language never triggers a syntax error.

### `read`

```bash
./.agents/skills/proxer/scripts/proxer read -f "internal/auth.go" -s 40 -e 70
```

Returns `{"root": "...", "filepath": "...", "content": "...", "hash": "..."}`.

`read` has no per-line buffer ceiling. Lines exceeding 4,096 characters are truncated with ` [truncated]` to keep payloads bounded, while `hash` always covers the SHA-256 of the complete file on disk.

### `edit`

```bash
./.agents/skills/proxer/scripts/proxer read -f "internal/auth.go" -s 52 -e 58
# -> {"root": "...", "filepath": "internal/auth.go", "content": "...", "hash": "1f0c..."}

./.agents/skills/proxer/scripts/proxer edit -f "internal/auth.go" -s 52 -e 58 --hash "1f0c..." <<'EOF'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
EOF
```

`edit` replaces exact inclusive line numbers (`-s` to `-e`) from stdin and **automatically reindexes only the edited file**. Passing `--hash` from the preceding `read` rejects the write with a non-zero exit if the file changed in between.

## Project Configuration (`.proxer.json`)

Add a `.proxer.json` file in the project root to extend the built-in skip list:

```json
{
  "ignored_dirs": ["third_party", "generated", "mocks"]
}
```

- Directory names match at any depth and are merged with the built-in defaults (`.git`, `node_modules`, `dist`, `.next`, `.agents`, etc. in `internal/config/config.go`).
- A malformed `.proxer.json` aborts the command with a non-zero exit code instead of silently indexing excluded directories.
- Changes take effect on the next `search` refresh or `index`. `edit` will not re-add a file under an ignored directory.

## Repository Layout

```
cmd/proxer/          CLI entry point (Cobra)
database/            SQLite store: connection, schema, chunk rows, file manifest
internal/domain/     Result models
internal/config/     Indexing limits and allowlists
internal/filesystem/ Indexability predicates
internal/indexer/    Delta build, chunk windows, and single-file reindex
internal/mcpserver/  MCP stdio server exposing the operations as tools
internal/tools/      Search, read, and edit operations
skills/proxer/       Agent Skill package (installed into the target project)
docs/                Documentation and integration guides
```

## Limitations

- **Project scope:** `read` and `edit` reject `../` and paths outside `<root>`. Use the harness's built-in file tools for external files.
- **Stat-gated refresh:** `search` refreshes by checking file size and modification time; a same-size write within the same timestamp tick is skipped until something else changes.
- **Refresh walk cost:** Every `search` stats the tree (~36 ms at 8,000 files) unless `--no-refresh` or `PROXER_NO_REFRESH=1` is used.
- **No regex or glob matching:** `search` uses FTS5 token-prefix matching plus `camelCase`/`PascalCase` sub-token tails (3+ chars on code/config files), not arbitrary substring, regex, or glob patterns.
- **Capped results:** `search` defaults to 5 distinct files (up to 50 with `-n/--limit`); check `total` and `truncated` when completeness matters.
- **Indexed extensions only:** Files outside the known source/document allowlists or exact names (`Dockerfile`, `Makefile`, `go.mod`, etc., up to 1 MB) are not searchable, though `read` and `edit` still work on them.
- **Line-range edits only:** Creating or deleting files is out of scope; `edit` replaces existing line ranges.
- **Concurrent writers:** Simultaneous edits do not merge; always pass `--hash` from `read` to prevent silent overwrites.
- **Not a semantic analyzer:** Proxer locates and edits text; pair it with a language server (`gopls`, `tsserver`, `rust-analyzer`) for type resolution and call-graph navigation.
