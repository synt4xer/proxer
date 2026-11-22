# Proxer

**Bounded-payload code search, targeted read, and hash-guarded edit for AI coding agents.**

Single static binary (CGO-free SQLite FTS5) · CLI, [Agent Skill](https://agentskills.io/specification), and MCP server.

---

## Why Proxer?

Shell tools like `rg`, `grep`, `sed`, and ad-hoc `python3` scripts are built for human terminals, not LLM prompt budgets. In an agent loop, they either flood the context window with megabytes of unranked lines or return `0 hits` on natural multi-word and `camelCase` queries.

Measured on an **8,000-file (68 MB)** mixed repository (Go source, tests, Markdown docs, and a 144 KB single-line minified JS bundle):

| Agent Operation | `proxer` | `rg` / `grep -rn` | `sed` / `python3` script |
| --- | --- | --- | --- |
| **Broad search** (`func`) | **1.0–1.5 KB** (top 5 code files, 55–82 ms) | **18.4–29.2 MB** / ~241k lines (`rg` 220 ms, `grep` 2.7 s) | — |
| **Multi-term query** (`AuthMiddleware session`) | **1.6 KB** (`AND`-first → ranks target #1, 20–95 ms) | **0 hits** (treats query as literal adjacent phrase) | — |
| **`camelCase` sub-token** (`middleware` → `AuthMiddleware`) | **1.5 KB** (matches sub-token tails, skips bundle, 15–100 ms) | **0 hits** (default) or **256 KB** (`-i` dumps minified JS line) | — |
| **Rare symbol** (`verifySignatureToken7722`, 1 hit) | **479–539 B** (60-line chunk context, 14–72 ms) | **71–74 B** (1 line only; `rg` 133 ms, `grep` 792 ms) | **74 B** (`python3` walk: 659 ms) |
| **Read slice** on a 144 KB single-line file (lines 1–5) | **4.4 KB** in **12 ms** (4,096-char cap + SHA-256 `hash`) | — | **144.1 KB** (`sed` 4 ms, `python3` 28 ms; no hash) |
| **Edit line range** (`--hash` guard + index update) | **15–19 ms** (verifies SHA-256 + reindexes edited file) | — | **5 ms** (`sed -i`, fragile escaping) / **28 ms** (`python3`, blind overwrite) |

> **When raw CLI tools win:** If you already know a rare symbol has <5 hits and only want `file:line`, `rg` or `grep -n` outputs fewer bytes (~71 B vs ~500 B). And for full-regex sweeps across a cold repo, use `rg`. Pair Proxer with a language server (`gopls`, `tsserver`, `rust-analyzer`) for type resolution and call graphs. Full methodology in [docs/BENCHMARKS.md](docs/BENCHMARKS.md).

## The 3-Step Agent Loop

1. **Search** — Find up to 5 distinct files (`-n` up to 50, `--max-per-file` up to 5) ranked production code → tests → docs → assets, with automatic pre-query refresh and honest `total` / `truncated` counts.
2. **Read** — Fetch exact line ranges (`-s` to `-e`) with a whole-file SHA-256 `hash` (lines >4,096 chars are safely capped with ` [truncated]`).
3. **Edit** — Replace exact line ranges from stdin using `--hash` to prevent stale overwrites, and Proxer reindexes **only that file** automatically.

```bash
# 1. Search (auto-refreshes .proxer.db before querying)
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware"

# 2. Read the target range and capture its hash
./.agents/skills/proxer/scripts/proxer read -f "internal/auth.go" -s 52 -e 58

# 3. Edit with concurrency protection + instant single-file reindex
./.agents/skills/proxer/scripts/proxer edit -f "internal/auth.go" -s 52 -e 58 --hash "1f0c..." <<'EOF'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
EOF
```

## Quick Start

### 1. Build & Install into Your Project

```bash
make build
make install DEST=/path/to/your-project
```

`make install` installs the binary and `SKILL.md` into `<DEST>/.agents/skills/proxer/` and adds `.proxer.db*` to `<DEST>/.gitignore`.

### 2. Optional: Enable MCP Server

Add Proxer to your harness MCP config (e.g., `.pi/mcp.json` or Claude/Cursor MCP settings):

```json
{
  "mcpServers": {
    "proxer": {
      "command": ".agents/skills/proxer/scripts/proxer",
      "args": ["mcp", "--root", "."],
      "cwd": "."
    }
  }
}
```

## Highlights

- **Bounded by design:** 60-line chunks (15-line overlap), max 5 distinct files by default, automatic skipping of single-line minified blobs during `index`, and 4,096-char line caps on `read`.
- **Fresh by default:** `search` performs a fast stat-only walk (~36 ms on 8,000 files) and re-chunks only changed files—no manual `index` step needed.
- **Smart code matching:** Token-prefix matching (`auth` → `AuthMiddleware`), `camelCase`/`PascalCase` tail indexing (`middleware` → `AuthMiddleware`), `AND`-first multi-term queries with `OR` fallback, and 5x path-weight boost.
- **Local-first & explicit root:** Resolves project root via `--root` → `PROXER_ROOT` → working directory, keeping `.proxer.db` isolated per repo.
- **Zero runtime dependencies:** Single CGO-free static Go binary.

## Documentation

- [CLI & Configuration Reference](docs/REFERENCE.md) — full flags, JSON schemas, `.proxer.json`, repo layout, and limitations
- [Harness & MCP Integration](docs/INTEGRATION.md) — Agent Skills and MCP stdio server setup
- [Benchmarks & Performance](docs/BENCHMARKS.md) — comparisons against `rg`, `grep`, `sed`, and `python3`, ranking ablations, and methodology
- [Agent Skill](skills/proxer/SKILL.md) — the prompt/skill specification bundled for coding agents

## License

Released under the [GNU General Public License v3.0](LICENSE).
