# Proxer (Project Indexer)

**Proxer** is a code search, read, and edit tool for AI coding agents. It keeps a SQLite FTS5 index of a project in `.proxer.db`, so an agent can find code by keyword, read the exact line range it needs, and replace that range without rewriting the whole file.

## Why Proxer

`grep` and `ripgrep` scan the tree on every call. Proxer answers from an index instead, so search latency stays roughly flat as the project grows, and each hit arrives as a chunk the agent can pass straight to `read` and `edit`. The measured cost, on an Apple-silicon Mac with a warm cache over synthetic Go trees:

| Workload | 2,000 files (160k lines) | 8,000 files (640k lines) |
| --- | --- | --- |
| `index` (once per session) | 278 ms | 1.39 s |
| `edit` (reindex the edited file) | 20 ms | 37 ms |
| `search` for a rare token | 7.9 ms | 9.4 ms |
| `rg` for the same token | 22 ms | 70 ms |
| `search` payload for a common word | 846 B | 831 B |
| `rg` payload for the same word | 10.8 MB / 156k lines | 43.9 MB / 624k lines |

Method: `index` fills the database once, `edit` reindexes only the file it changed, and `search` returns at most five 60-line chunks. `rg` is ripgrep 15.2.0 given an explicit path. The index cost is one-time and proportional to the tree; the edit cost tracks the edited file, not the project.

The payload column is the reason to prefer Proxer over a raw scan in an agent loop: the same query that dumps 43.9 MB of grep output returns 831 bytes of `{root, filepath, line, snippet}`, and each entry can be fed to `read` and then `edit` with the `hash` guard.

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
- **Autonomous edit and targeted reindex:** When an agent edits a file, Proxer saves the change and reindexes **only that file**. The rest of the index stays valid, so the edit cost tracks the file rather than the project size (see [Why Proxer](#why-proxer)).
- **Explicit project root:** `--root`, `PROXER_ROOT`, or the working directory, in that order. The index never depends on where the process happened to start.
- **Prefix search:** Every search term is matched as a token prefix, so `auth` reaches `AuthMiddleware` without the agent guessing full identifiers.
- **Query sanitization:** Free-form input, including punctuation and FTS5 operators, is quoted before it reaches the parser. Natural language never triggers a syntax error.
- **Smart defaults:** Out of the box, it skips dependency, build, and VCS directories (`.git`, `node_modules`, `dist`, `.next`, `.agents`, …) and indexes known source and document extensions plus exact names such as `Dockerfile`, `Makefile`, and `go.mod`, up to 1 MB per file. A project extends the skip list with a `.proxer.json` at its root, and the defaults always stay in force. The built-in lists live in `internal/config/config.go`.
- **Self-contained:** A single CGO-free static binary built on `modernc.org/sqlite`. No runtime dependencies, no service to run.

## Repository Layout

```
cmd/proxer/          CLI entry point (Cobra)
internal/domain/     Result models
internal/config/     Indexing limits and allowlists
internal/filesystem/ Indexability predicates
internal/indexer/    SQLite FTS5 store and chunking
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

## Command Reference

Results are printed as JSON on stdout; failures go to stderr with a non-zero exit code. Every command accepts the global `--root <path>` flag described above; without it, the working directory is the project root.

| Command | Purpose |
| --- | --- |
| `index` | Rebuild `.proxer.db` from the project tree. |
| `search -k <keyword>` | Return up to 5 matching chunks with `root`, `filepath`, `line`, and `snippet`. |
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

Names match whole directory names at any depth and are added to the built-in defaults, which stay in force. A malformed file aborts the command with a non-zero exit code instead of silently indexing directories the project excluded. Changing the list takes effect on the next `index`; `edit` will not re-add a file under an ignored directory to the index.

### Initialize

```bash
./.agents/skills/proxer/scripts/proxer index
```

The index is a snapshot. Re-run `index` after `git checkout`, `git pull`, or any change made outside of `proxer edit`. Run it once per session; `edit` keeps itself up to date afterwards.

### Search

```bash
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware"
```

From outside the project, name the root instead of changing directory:

```bash
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware" --root ~/code/my-awesome-project
```

```
[{"root":"/home/dev/code/my-awesome-project","filepath":"internal/auth.go","line":"40-99","snippet":"...<b>AuthMiddleware</b>..."}]
```

`root` is the absolute path of the indexed project, so results stay unambiguous when one harness drives several projects. `line` is the chunk boundary, not the exact hit. Use the snippet to locate the match inside the chunk.

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
- **MCP server.** `proxer mcp` publishes the four tools as `mcp__proxer__<tool>`. Tool arguments avoid shell quoting, and `edit` takes the replacement as an argument instead of stdin. Choose this when the harness speaks MCP and you want the tools in the model's tool list.

Start with the project-local skill. Add the MCP server when the harness supports it, and reach for the global skill only when per-project copies become a burden.

Setup for both, including a worked project-scoped MCP config, lives in [docs/INTEGRATION.md](docs/INTEGRATION.md).

## Limitations

- **Scope.** Every path must stay inside the root. `read` and `edit` reject `../` and absolute paths that point at another project, because no index entry exists outside the root. Use the harness `read` or `bash` for those files.
- **Snapshot index.** The index reflects the tree at the last `index` (`edit` refreshes the file it touches). Changes from `git checkout`, another tool, or another process are invisible until the next `index`.
- **Text matching only.** Search matches token prefixes. CamelCase is not split, so `middleware` will not find `AuthMiddleware` while `auth` will. There are no regular expressions or globs.
- **Indexed extensions only.** Files outside the known source and document extensions are not searchable. Editing one works, but the change will not appear in `search`.
- **Ignored directories.** Directories on the skip list are not indexed, and `edit` inside one will not re-add it to the index.
- **File edits only.** Creating and deleting files is out of scope; `edit` rewrites an existing line range.
- **Concurrent writers.** Two processes editing the same file leave only the last write. `edit` never writes a half-finished file, but it does not merge concurrent changes. Pass the `hash` from `read` so a change in between is refused.
- **Database hygiene.** `.proxer.db` belongs in the target project's `.gitignore`.

## License

Proxer is released under the [GNU General Public License, version 3](LICENSE). You may use, study, share, and modify it. GPL-3.0 is a copyleft license: if you distribute a modified version, you must release it under the same license and make the source available. The software comes with no warranty.
