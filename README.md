# Proxer (Project Indexer)

**Proxer** is a high-performance, autonomous sub-process CLI tool designed to act as _the eyes and hands_ for AI Agents (LLMs) in your local development environment.

Instead of relying on `grep` for code search or fragile Python scripts for code modification, Proxer manages everything through SQLite FTS5 and pure-Go file operations, enabling highly precise codebase exploration and seamless code editing.

## Architectural Paradigm: Local-First Tools

**Proxer** is designed to be installed **locally within the target project's workspace** (e.g., `.agents/skills/proxer`) rather than globally (`/usr/local/bin`). This prevents naming collisions, allows version pinning per project, and keeps the SQLite database (`.proxer.db`) isolated per project.

Each `proxer` invocation operates on a **project root**, and the root is resolved explicitly:

```
--root <path>  →  PROXER_ROOT  →  working directory
```

The database lives at `<root>/.proxer.db`, and every path in it is keyed relative to that root. A harness can therefore spawn the binary from any directory — or set `PROXER_ROOT` in its server configuration — without the index drifting to the wrong place. Relative `-f` paths resolve against the root as well.

## Key Features

- **FTS5 Smart Chunking:** Code is chunked into 60-line segments (with a 15-line sliding window) to preserve context retention and prevent wasting the agent's token limits.
- **Autonomous Edit & Targeted Reindex:** When an agent edits a file, Proxer saves the change and reindexes **only that file**. The rest of the index stays valid, so the cost of an edit is constant instead of proportional to the project size. On a 2000-file tree, an edit drops from 162 ms to 11 ms.
- **Explicit Project Root:** `--root`, `PROXER_ROOT`, or the working directory, in that order. The index never depends on where the process happened to start.
- **Prefix Search:** Every search term is matched as a token prefix, so `auth` reaches `AuthMiddleware` without the agent guessing full identifiers.
- **Query Sanitization:** Free-form input, including punctuation and FTS5 operators, is quoted before it reaches the parser. Natural language never triggers a syntax error.
- **Smart Defaults:** Out of the box, it skips dependency, build, and VCS directories (`.git`, `node_modules`, `dist`, `.next`, `.agents`, …) and indexes known source and document extensions plus exact names such as `Dockerfile`, `Makefile`, and `go.mod`, up to 1 MB per file. The lists live in `internal/config/config.go`.
- **Self-Contained:** A single CGO-free static binary built on `modernc.org/sqlite`. No runtime dependencies, no service to run.

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

We use a `Makefile` to compile Proxer (without CGO) and inject the binary directly into your target project's agent skills folder.

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
| `search -k <keyword>` | Return up to 5 matching chunks with `filepath`, `line`, and `snippet`. |
| `read -f <file> -s <start> -e <end>` | Return the inclusive line range as JSON. |
| `edit -f <file> -s <start> -e <end>` | Replace the range with stdin content, then reindex that file. |
| `mcp` | Serve all four operations as MCP tools over stdio. |

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
[{"filepath":"internal/auth.go","line":"40-99","snippet":"...<b>AuthMiddleware</b>..."}]
```

`line` is the chunk boundary, not the exact hit. Use the snippet to locate the match inside the chunk.

### Read

```bash
./.agents/skills/proxer/scripts/proxer read -f "internal/auth.go" -s 40 -e 70
```

### Edit

Line-based replacement is more robust than Python string replacement because it is unaffected by indentation parsing errors. **Proxer reindexes the edited file automatically after this command completes**; no other file is touched.

Line numbers are exact boundaries, so read the range immediately before editing it. Any change that shifts lines in between makes the range stale.

```bash
./.agents/skills/proxer/scripts/proxer edit -f "PLAN.md" -s 74 -e 86 <<'EOF'
  - [x] **1.2 Dialect-Specific DDL Migrations**
     - [x] Port `000001_init.up.sql` to PostgreSQL dialect
       - Native types (`TIMESTAMPTZ`, `BOOLEAN`, `TEXT`, `REAL`).
EOF
```
## Integration

Proxer plugs into an agent harness in two ways:

- **Agent Skill** — `skills/proxer/SKILL.md` follows the [Agent Skills specification](https://agentskills.io/specification), so any harness that discovers `.agents/skills/<name>/SKILL.md` loads it. `make install` places the package correctly.
- **MCP server** — `proxer mcp` serves `index`, `search`, `read`, and `edit` as MCP tools over stdio.

Setup for both, including a per-project `.pi/mcp.json`, lives in [docs/integration.md](docs/integration.md).
