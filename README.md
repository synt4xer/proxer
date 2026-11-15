# Proxer (Project Indexer)

**Proxer** is a high-performance, autonomous sub-process CLI tool designed to act as _the eyes and hands_ for AI Agents (LLMs) in your local development environment.

Instead of relying on `grep` for code search or fragile Python scripts for code modification, Proxer manages everything through SQLite FTS5 and pure-Go file operations, enabling highly precise codebase exploration and seamless code editing.

## Architectural Paradigm: Local-First Tools

**Proxer** is designed to be installed **locally within the target project's workspace** (e.g., `.agents/skills/proxer`) rather than globally (`/usr/local/bin`). This prevents naming collisions, allows version pinning per project, and ensures the SQLite database (`.proxer.db`) remains perfectly isolated to the project's root.

## Key Features

- **FTS5 Smart Chunking:** Code is chunked into 60-line segments (with a 15-line sliding window) to preserve context retention and prevent wasting the agent's token limits.
- **Autonomous Edit & Auto-Reindex:** When an agent edits a file, Proxer immediately saves the changes and silently updates the SQLite index. The agent's memory stays perfectly synchronized with the disk.
- **Smart Defaults:** Out of the box, it ignores build/dependency folders (`node_modules`, `dist`, `.next`) and strictly indexes relevant files (`.go`, `.ts`, `.md`).
- **Zero Dependencies:** Built with a CGO-free driver (`modernc.org/sqlite`). Highly portable and fast to compile.

## Compilation and Installation

We use a `Makefile` to compile Proxer (without CGO) and inject the binary directly into your target project's agent skills folder.

### 1. Build and Run Tests

```bash
make test
make build
```

### 2. Install to a Target Project

Point the DEST variable to the root directory of the project where your LLM will be working.

```bash
make install DEST=/path/to/my-awesome-project
```

This will create the directory and place the binary at: `/path/to/my-awesome-project/.agents/skills/proxer`.

## Agent Usage & Harness Integration

**Proxer** interacts with your AI Harness via stdout (for successful JSON responses) and stderr (for error messages requiring agent intervention). All commands below assume the agent is executing them from the root of the target project.

### 1. Initialize

The agent (or you) runs this once to build the `.proxer.db` in the project root.

```bash
./.agents/skills/proxer index
```

### 2. Search

Used by the agent to find functions, lines, or error messages.

```bash
./.agents/skills/proxer search -k "authMiddleware"
```

Output: JSON array containing `filepath`, `line ranges`, and `highlighted snippets`.

### 3. Read

Used by the agent to read full line ranges without hallucinating.

```bash
./.agents/skills/proxer read -f "internal/auth.go" -s 40 -e 70
```

### 4. Edit

Used by the agent to perform precise line replacements. This method is far more robust than Python string replacement because it is unaffected by indentation parsing errors. **Proxer automatically triggers an FTS5 re-index after this command completes.**

```bash
./.agents/skills/proxer edit -f "PLAN.md" -s 74 -e 86 << 'EOF'
  - [x] **1.2 Dialect-Specific DDL Migrations**
     - [x] Port `000001_init.up.sql` to PostgreSQL dialect
       - Native types (`TIMESTAMPTZ`, `BOOLEAN`, `TEXT`, `REAL`).
EOF
```
