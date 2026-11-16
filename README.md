# Proxer (Project Indexer)

**Proxer** is a code search, read, and edit tool for AI coding agents. It keeps a SQLite FTS5 index of a project in `.proxer.db`, so an agent can find code by keyword, read the exact line range it needs, and replace that range without rewriting the whole file.

Current release: [`v0.4.0`](https://github.com/synt4xer/proxer/releases/tag/v0.4.0). The upgrade notes for a project that already ran an older Proxer are in [Upgrading from v0.3.0](#upgrading-from-v030).

## Why Proxer

An agent's context window is the scarce resource, and a raw scan spends it on every matching line. On the benchmark below, `rg` printed 7.3 MB over 60,000 lines for the token `func` on a 2,000-file tree, and 29.2 MB over 240,000 lines on an 8,000-file tree. The same query through Proxer returned about 1 KB at either size.

Proxer returns at most five 60-line chunks, each with a 15-line overlap, so a hit arrives with enough surrounding code to act on and can be passed straight to `read` and `edit`. The index keeps search fast as the tree grows, but the bounded payload is the reason to use it in an agent loop.

| Workload | 2,000 files (17 MB) | 8,000 files (68 MB) |
| --- | --- | --- |
| `search` payload for `func` | 1.0 KB | 1.0 KB |
| `rg` payload for `func` | 7.3 MB / 60k lines | 29.2 MB / 240k lines |
| `search` for a token that never appears, refreshed | 16.8 ms | 38.6 ms |
| `search` for the same token, `--no-refresh` | 8.7 ms | 8.9 ms |
| `search` for `func`, refreshed | 27.8 ms | 81.9 ms |
| `search` for `func`, `--no-refresh` | 20.8 ms | 55.2 ms |
| `index`, first build | 433 ms | 2.00 s |
| `index`, nothing changed | 16.6 ms | 35.6 ms |
| `index`, 1,000 files changed | 544 ms | 717 ms |
| `edit` (reindex the edited file) | 15 ms | 15 ms |

Method: an Apple-silicon Mac with a warm cache over generated Go trees (2,000 files / 17 MB and 8,000 files / 68 MB, which chunk into about 8,000 and 32,000 rows). Each number is the median of seven `proxer` runs, process start included, and each changed-file run writes its files immediately before the timed build so the diff it claims to measure is really there. `rg` is ripgrep 15.2.0 given an explicit path and default output.

`index` is a delta build. The first build reads and hashes every file and writes the chunk map; a build with nothing to do is a stat-only walk; a changed file costs about 0.7 ms to re-read and re-chunk at 32,000 chunks, so the 1,000-file pull finishes in roughly two thirds of a second. `search` refreshes the index before it queries unless `--no-refresh` is passed, which is why the refreshed and unrefreshed rows differ. `edit` reindexes only the file it changed.

`search` returns an envelope: `{root, indexed_at, refreshed, results}`, where each entry of `results` is `{root, filepath, line, snippet}`. The agent feeds the line range to `read`, gets back a `hash`, and passes it to `edit` so a change in between refuses the overwrite.

Proxer is text search, not semantic analysis. It does not resolve types, definitions, or references; that is what a language server (`gopls`, `tsserver`, `rust-analyzer`) is for. Use Proxer to locate and change text, and the language server to reason about the program. They complement each other.

## Architectural Paradigm: Local-First Tools

**Proxer** is designed to be installed **locally within the target project's workspace** (e.g., `.agents/skills/proxer`) rather than globally (`/usr/local/bin`). This prevents naming collisions, allows version pinning per project, and keeps the SQLite database (`.proxer.db`) isolated per project.

Each `proxer` invocation operates on a **project root**, and the root is resolved explicitly:

```
--root <path>  →  PROXER_ROOT  →  working directory
```

The database lives at `<root>/.proxer.db`, and every path in it is keyed relative to that root. A harness can therefore spawn the binary from any directory, or set `PROXER_ROOT` in its server configuration, without the index drifting to the wrong place. Relative `-f` paths resolve against the root as well.

## Key Features

- **FTS5 smart chunking:** Proxer splits code into 60-line chunks with a 15-line overlap, so a match keeps its surrounding context without spending the agent's token budget on whole files.
- **Fresh by default:** `search` walks the tree before it queries, re-reads only the files whose size or timestamp moved, re-chunks only the ones whose content actually changed, and drops the rows of files that are gone. Changes written by `git pull` or another tool are found without a manual `index`. A refresh with nothing to do opens no write transaction and costs a 36 ms walk on 8,000 files.
- **Autonomous edit and targeted reindex:** When an agent edits a file, Proxer saves the change and reindexes **only that file**. The rest of the index stays valid, so the edit cost tracks the file rather than the project size (see [Why Proxer](#why-proxer)).
- **Explicit project root:** `--root`, `PROXER_ROOT`, or the working directory, in that order. The index never depends on where the process happened to start.
- **Prefix search:** Every search term is matched as a token prefix, so `auth` reaches `AuthMiddleware` without the agent guessing full identifiers.
- **Query sanitization:** Free-form input, including punctuation and FTS5 operators, is quoted before it reaches the parser. Natural language never triggers a syntax error.
- **Smart defaults:** Out of the box, it skips dependency, build, and VCS directories (`.git`, `node_modules`, `dist`, `.next`, `.agents`, …) and indexes known source and document extensions plus exact names such as `Dockerfile`, `Makefile`, and `go.mod`, up to 1 MB per file. A project extends the skip list with a `.proxer.json` at its root, and the defaults always stay in force. The built-in lists live in `internal/config/config.go`.
- **Self-contained:** A single CGO-free static binary built on `modernc.org/sqlite`. No runtime dependencies, no service to run.

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
docs/                Harness integration guide
```

## Compilation and Installation

The `Makefile` compiles Proxer without CGO and installs the binary into a target project's agent skills folder.

### 1. Build and Run Tests

```bash
make test
make build
```

### 2. Install to a Target Project

Point the `DEST` variable to the root directory of the project where your LLM will be working.

```bash
make install DEST=/path/to/my-awesome-project
```

The skill is installed as a single unit:

```
/path/to/my-awesome-project/.agents/skills/proxer/
├── SKILL.md
└── scripts/proxer
```

Add the index to the target project's `.gitignore`:

```gitignore
.proxer.db
.proxer.db-shm
.proxer.db-wal
```

### Upgrading from v0.3.0

Two things change under an existing project, and both are handled without manual steps:

- **The database is rebuilt once.** v0.4.0 stamps the schema version, the chunk window, and a per-file manifest into `.proxer.db`; a database without that stamp is cleared and reindexed on the next command instead of being extended with rows it cannot account for. The first `search` after the upgrade pays for the full build (about 2 s at 8,000 files), and every later one is a delta.
- **`search` output is an envelope.** A script that parsed the top-level array must read `results` instead:

```json
{
  "root": "/home/dev/code/my-awesome-project",
  "indexed_at": "2026-11-16T04:55:12.917786Z",
  "refreshed": {"added": 0, "changed": 0, "removed": 0},
  "results": [{"root": "...", "filepath": "...", "line": "136-181", "snippet": "...<b>hit</b>..."}]
}
```

`index` still prints `{"status": "indexed_successfully", "refreshed": {...}}`, and `read` and `edit` are unchanged.

## Command Reference

Results are printed as JSON on stdout; failures go to stderr with a non-zero exit code. Every command accepts the global `--root <path>` flag described above; without it, the working directory is the project root.

| Command | Purpose |
| --- | --- |
| `index` | Bring `.proxer.db` in line with the project tree, touching only new, changed, and deleted files. |
| `search -k <keyword> [--no-refresh]` | Refresh the index, then return up to 5 matching chunks inside `{root, indexed_at, refreshed, results}`. |
| `read -f <file> -s <start> -e <end>` | Return `root`, `filepath`, the inclusive line range, and the file `hash` as JSON. |
| `edit -f <file> -s <start> -e <end> [--hash <hash>]` | Replace the range with stdin content, then reindex that file. |
| `mcp` | Serve all four operations as MCP tools over stdio. |

### Project Configuration

Drop a `.proxer.json` in the project root to add directories to the skip list:

```json
{
  "ignored_dirs": ["third_party", "generated", "mocks"]
}
```

Names match whole directory names at any depth and are added to the built-in defaults, which stay in force. A malformed file aborts the command with a non-zero exit code instead of silently indexing directories the project excluded. The skip list is read on every command, so a change takes effect on the next `search` refresh or `index`; `edit` will not re-add a file under an ignored directory to the index.

### Initialize

```bash
./.agents/skills/proxer/scripts/proxer index
```

The build is incremental, so running `index` again is cheap: files whose size and modification time still match the manifest are skipped, files whose bytes hash the same are only re-stamped, and rows for deleted paths are dropped. Nothing needs to be remembered between sessions, because `search` does the same work before every query. Run `index` only when you want to pay that cost up front, for example before a timed query or after a large `git pull`.

### Search

```bash
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware"
```

From outside the project, name the root instead of changing directory:

```bash
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware" --root ~/code/my-awesome-project
```

```
{
  "root": "/home/dev/code/my-awesome-project",
  "indexed_at": "2026-11-16T03:38:29.543151Z",
  "refreshed": {"added": 1, "changed": 2, "removed": 0},
  "results": [
    {"root": "/home/dev/code/my-awesome-project", "filepath": "internal/auth.go", "line": "40-99", "snippet": "...<b>AuthMiddleware</b>..."}
  ]
}
```

`root` is the absolute path of the indexed project, so results stay unambiguous when one harness drives several projects. `indexed_at` is when the index last changed, not when it was last checked. `refreshed` counts the files this call added, changed, and removed; all zero means the index was already current, and the field is absent when `--no-refresh` skipped the check. `results` is empty, not missing, when nothing matches.

`line` is the chunk boundary, not the exact hit. Use the snippet to locate the match inside the chunk.

Pass `--no-refresh` (or set `PROXER_NO_REFRESH=1`) to query the index exactly as it stands. That is the right call when the database is mounted read-only or when a pinned snapshot is the point; it is the wrong call when you expect the answer to describe the tree.

### Read

```bash
./.agents/skills/proxer/scripts/proxer read -f "internal/auth.go" -s 40 -e 70
```

The result is `{"root": "...", "filepath": "...", "content": "...", "hash": "..."}`; `root` is the absolute path of the indexed project, `filepath` is echoed as given, and `hash` is the SHA-256 of the whole file at read time.

Pass that `hash` to the next `edit` of the same file. If anything changed in between, the edit is refused instead of overwriting code the agent has not seen.

### Edit

The replacement targets exact line numbers, so it does not depend on matching indentation or surrounding whitespace. **Proxer reindexes the edited file automatically after this command completes**; no other file is touched.

Line numbers are exact boundaries, so read the range immediately before editing it. Any change that shifts lines in between makes the range stale.

Pass the `hash` from that read as `--hash` to make the check explicit: if the file changed after the read, `edit` exits non-zero and leaves the file untouched instead of overwriting code the agent has not seen. Without `--hash` the edit proceeds as before.

```bash
./.agents/skills/proxer/scripts/proxer read -f "internal/auth.go" -s 52 -e 58
# -> {"root": "...", "filepath": "internal/auth.go", "content": "...", "hash": "1f0c..."}

./.agents/skills/proxer/scripts/proxer edit -f "internal/auth.go" -s 52 -e 58 --hash "1f0c..." <<'EOF'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
EOF
```

## Integration

Proxer plugs into an agent harness in two ways:

- **Agent Skill:** `skills/proxer/SKILL.md` follows the [Agent Skills specification](https://agentskills.io/specification), so any harness that discovers `.agents/skills/<name>/SKILL.md` loads it. `make install` places the package correctly.
- **MCP server:** `proxer mcp` serves `index`, `search`, `read`, and `edit` as MCP tools over stdio.

Which one to pick:

- **Project-local skill (default).** `make install DEST=/path/to/project` puts the package inside the project, with its own binary and its own `.proxer.db`. Choose this when an agent works on one project at a time and you want the version pinned there.
- **Global skill.** `make install DEST=$HOME` installs the same package at `~/.agents/skills/proxer`, so several projects share one binary. The database is still per project, so pass `--root` or set `PROXER_ROOT`. Choose this when you drive many repositories and do not want a copy in each.
- **MCP server.** `proxer mcp` publishes the four tools as `mcp__proxer__<tool>`. Tool arguments avoid shell quoting, and `edit` takes the replacement as an argument instead of stdin. The server's `search` refreshes the index just like the CLI; set `PROXER_NO_REFRESH=1` in its environment to make every search a pure read. Choose this when the harness speaks MCP and you want the tools in the model's tool list.

Start with the project-local skill. Add the MCP server when the harness supports it, and reach for the global skill only when per-project copies become a burden.

Setup for both, including a worked project-scoped MCP config, lives in [docs/INTEGRATION.md](docs/INTEGRATION.md).

## Limitations

- **Scope.** Every path must stay inside the root. `read` and `edit` reject `../` and absolute paths that point at another project, because no index entry exists outside the root. Use the harness `read` or `bash` for those files.
- **Fresh to a gate, not to a proof.** `search` refreshes by comparing size and modification time, and only reads a file whose stat moved. A same-size write inside the same timestamp tick is therefore skipped until something else changes the file. The hash catches every case where the gate opens; it is not computed on every walk.
- **Refresh costs a walk.** Every `search` stats the tree, about 36 ms at 8,000 files, and takes the write lock only when something changed. `--no-refresh` avoids both.
- **Text matching only.** Search matches token prefixes. CamelCase is not split, so `middleware` will not find `AuthMiddleware` while `auth` will. There are no regular expressions or globs.
- **Capped result set.** `search` returns at most five chunks ordered by rank, so a broad query is truncated rather than complete. Treat a search as a starting point.
- **Indexed extensions only.** Files outside the known source and document extensions are not searchable. Editing one works, but the change will not appear in `search`.
- **Ignored directories.** Directories on the skip list are not indexed, and `edit` inside one will not re-add it to the index.
- **File edits only.** Creating and deleting files is out of scope; `edit` rewrites an existing line range.
- **Concurrent writers.** Two processes editing the same file leave only the last write. `edit` never writes a half-finished file, but it does not merge concurrent changes. Pass the `hash` from `read` so a change in between is refused.
- **Database hygiene.** `.proxer.db` belongs in the target project's `.gitignore`.

## License

Proxer is released under the [GNU General Public License, version 3](LICENSE). You may use, study, share, and modify it. GPL-3.0 is a copyleft license: if you distribute a modified version, you must release it under the same license and make the source available. The software comes with no warranty.
