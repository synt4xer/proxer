# Proxer (Project Indexer)

**Proxer** is a high-performance, autonomous sub-process CLI tool designed to act as _the eyes and hands_ for AI Agents (LLMs) in your local development environment.

Instead of relying on `grep` for code search or fragile Python scripts for code modification, Proxer manages everything through SQLite FTS5 and pure-Go file operations, enabling highly precise codebase exploration and seamless code editing.

## Architectural Paradigm: Local-First Tools

**Proxer** is designed to be installed **locally within the target project's workspace** (e.g., `.agents/skills/proxer`) rather than globally (`/usr/local/bin`). This prevents naming collisions, allows version pinning per project, and ensures the SQLite database (`.proxer.db`) remains perfectly isolated to the project's root.

## Key Features

- **FTS5 Smart Chunking:** Code is chunked into 60-line segments (with a 15-line sliding window) to preserve context retention and prevent wasting the agent's token limits.
- **Autonomous Edit & Auto-Reindex:** When an agent edits a file, Proxer immediately saves the changes and silently updates the SQLite index. The agent's memory stays perfectly synchronized with the disk.
- **Prefix Search:** Every search term is matched as a token prefix, so `auth` reaches `AuthMiddleware` without the agent guessing full identifiers.
- **Query Sanitization:** Free-form input, including punctuation and FTS5 operators, is quoted before it reaches the parser. Natural language never triggers a syntax error.
- **Smart Defaults:** Out of the box, it skips dependency, build, and VCS directories (`.git`, `node_modules`, `dist`, `.next`, `.agents`, …) and indexes only known source and document extensions.
- **Self-Contained:** A single CGO-free static binary built on `modernc.org/sqlite`. No runtime dependencies, no service to run.

## Repository Layout

```
cmd/proxer/          CLI entry point (Cobra)
internal/domain/     Result models
internal/config/     Indexing limits and allowlists
internal/filesystem/ Indexability predicates
internal/indexer/    SQLite FTS5 store and chunking
internal/tools/      Search, read, and edit operations
skills/proxer/       Agent Skill package (installed into the target project)
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

All commands must run from the root of the target project. Results are printed as JSON on stdout; failures go to stderr with a non-zero exit code.

| Command | Purpose |
| --- | --- |
| `index` | Rebuild `.proxer.db` from the project tree. |
| `search -k <keyword>` | Return up to 5 matching chunks with `filepath`, `line`, and `snippet`. |
| `read -f <file> -s <start> -e <end>` | Return the inclusive line range as JSON. |
| `edit -f <file> -s <start> -e <end>` | Replace the range with stdin content, then re-index. |

### Initialize

```bash
./.agents/skills/proxer/scripts/proxer index
```

The index is a snapshot. Re-run `index` after `git checkout`, `git pull`, or any change made outside of `proxer edit`.

### Search

```bash
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware"
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

Line-based replacement is far more robust than Python string replacement because it is unaffected by indentation parsing errors. **Proxer automatically triggers an FTS5 re-index after this command completes.**

```bash
./.agents/skills/proxer/scripts/proxer edit -f "PLAN.md" -s 74 -e 86 <<'EOF'
  - [x] **1.2 Dialect-Specific DDL Migrations**
     - [x] Port `000001_init.up.sql` to PostgreSQL dialect
       - Native types (`TIMESTAMPTZ`, `BOOLEAN`, `TEXT`, `REAL`).
EOF
```
## Harness Integration

### Agent Skills

`skills/proxer/SKILL.md` follows the [Agent Skills specification](https://agentskills.io/specification), so any harness that discovers `.agents/skills/<name>/SKILL.md` picks Proxer up with no further configuration. Pi, Claude Code, and other Agent Skills implementations read the same layout.

`make install` already places the package in the right location, so integration is just:

1. Install into the target project (`make install DEST=...`).
2. Confirm the harness lists `proxer` among its available skills after restart, and that `/skill:proxer` or an equivalent command loads it.
3. Ask the agent to use it, for example: _"Use proxer to find where `AuthMiddleware` is defined, read that range, and rename it to `RequireSession`."_

The skill body teaches the agent the order of operations: `index` first, then `search` → `read` → `edit`. Keep `SKILL.md` in sync with the CLI flags whenever they change.

Proxer exposes no protocol server: the harness invokes the binary directly. MCP server mode is not implemented yet.
