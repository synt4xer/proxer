# Proxer

**Bounded-payload code search, targeted read, and hash-guarded edit for AI coding agents.**

Current release: [`v0.5.0`](https://github.com/synt4xer/proxer/releases/tag/v0.5.0) · Single static binary (CGO-free SQLite FTS5) · CLI, [Agent Skill](https://agentskills.io/specification), and MCP server.

---

## Why Proxer?

Raw `grep` / `rg` dumps every matching line into the model's context window. On an 8,000-file Go tree, searching `func` with `rg` prints **29.2 MB** across 240,000 lines; Proxer returns **1.0 KB**—up to five distinct files, ranked production-code-first, with 60-line overlapping chunks ready to read and edit.

| Operation | 2,000 files (17 MB) | 8,000 files (68 MB) |
| --- | --- | --- |
| `proxer search` payload (`func`) | **1.0 KB** | **1.0 KB** |
| `rg` payload (`func`) | 7.3 MB / 60k lines | 29.2 MB / 240k lines |
| `proxer search` (refreshed / `--no-refresh`) | 27.8 ms / 20.8 ms | 81.9 ms / 55.2 ms |
| `proxer edit` (with single-file reindex) | **15 ms** | **15 ms** |

> Proxer locates and edits text fast; pair it with a language server (`gopls`, `tsserver`, `rust-analyzer`) for type resolution and call graphs. See [docs/BENCHMARKS.md](docs/BENCHMARKS.md) for full numbers and methodology.

## The 3-Step Agent Loop

1. **Search** — Find up to 5 distinct files (`-n` up to 50) ranked code → tests → docs → assets, with automatic pre-query refresh and honest `total` / `truncated` counts.
2. **Read** — Fetch exact line ranges (`-s` to `-e`) with a whole-file SHA-256 `hash` (long lines >4,096 chars are safely truncated).
3. **Edit** — Replace exact line ranges using `--hash` to prevent stale overwrites, and Proxer reindexes **only that file** automatically.

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

- **Bounded by design:** 60-line chunks (15-line overlap), max 5 distinct files by default, and safe 4,096-char line caps on `read` and indexing.
- **Fresh by default:** `search` performs a fast stat-only walk (~36 ms on 8,000 files) and re-chunks only changed files—no manual `index` step needed.
- **Path + prefix search:** Token-prefix matching (`auth` → `AuthMiddleware`) across both file paths and content, with FTS5 query sanitization.
- **Local-first & explicit root:** Resolves project root via `--root` → `PROXER_ROOT` → working directory, keeping `.proxer.db` isolated per repo.
- **Zero runtime dependencies:** Single CGO-free static Go binary.

## Documentation

- [CLI & Configuration Reference](docs/REFERENCE.md) — full flags, JSON schemas, `.proxer.json`, repo layout, and limitations
- [Harness & MCP Integration](docs/INTEGRATION.md) — Agent Skills and MCP stdio server setup
- [Benchmarks & Performance](docs/BENCHMARKS.md) — scale tests, `v0.4.0` vs `v0.5.0` deltas, and methodology
- [Agent Skill](skills/proxer/SKILL.md) — the prompt/skill specification bundled for coding agents

## License

Released under the [GNU General Public License v3.0](LICENSE).
