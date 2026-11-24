# Proxer

[![codecov](https://codecov.io/gh/synt4xer/proxer/graph/badge.svg)](https://codecov.io/gh/synt4xer/proxer)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Bounded-payload code search, targeted read, and hash-guarded edit for AI coding agents.**

Single static Go binary (CGO-free SQLite FTS5) · CLI, [Agent Skill](https://agentskills.io/specification), and MCP stdio server.

---

## Why Proxer?

Shell tools (`grep`, `sed`, ad-hoc `python3`) are built for human terminals, not LLM context windows. They either dump megabytes of unranked lines or return `0 hits` on natural multi-word and `camelCase` queries.

Measured `[CLI end-to-end]` on the reproducible **8,000-file / 72,000-chunk Go fixture** + 143.6 KB minified JS file (Apple M1, `CGO_ENABLED=0`, `modernc.org/sqlite v1.62.0`, median of 7 runs):

| Operation (`[CLI end-to-end]`) | `proxer` | `grep -rn` | `sed` / `python3` |
| --- | --- | --- | --- |
| **Rare symbol** (`verifySignatureToken7722`, 1 hit) | **519–579 B** · **11.4 / 64.6 ms** (`--no-refresh` / refreshed) | **120 B** · 1,613 ms | **120 B** (`python3`: 914 ms) |
| **Sub-token `camelCase`** (`Signature`) | **519–579 B** · **11.5 / 62.8 ms** | **0 hits** (case-sensitive) | — |
| **Common term** (`func`, 72k chunks · 656k occ.) | **2.1 KB** (top 5 files) · **146.2 / 204.2 ms** | **54.88 MB** · 1,311 ms | — |
| **Common multi-term** (`context value`, 72k chunks) | **2.3 KB** (`AND`-first) · **271.1 / 327.4 ms** | **0 hits** · 1,642 ms | — |
| **Read 143.6 KB single-line file** (lines 1–5) | **4.3 KB** · **9.4 ms** (4,096-char line cap + SHA-256 `hash`) | — | **143.6 KB** (`sed` 4.2 ms, `python3` 27.6 ms) |
| **Edit 6-line function** (`--hash` + single-file reindex) | **19.6 ms** (atomic write + hash guard + reindex) | — | **4.1 ms** (`sed -i`) / **27.0 ms** (`python3`, blind overwrite) |

### When to Use Built-in Harness Tools vs. Proxer

- **Use built-in `Grep` / `Glob` / `Read`** for one-off queries on an unindexed repo (avoids the initial ~5.4 s index build on 72k chunks), regex/glob sweeps, or known <5-hit exact symbols.
- **Use Proxer** for warm multi-term / `camelCase` ranked discovery (`code` → `test` → `docs` → `assets`), strictly bounded reads (4,096-char line cap + 64 KB response cap), and hash-guarded atomic edits that keep the index fresh.

Full benchmark tables, trade-offs, and reproduction script: [docs/BENCHMARKS.md](docs/BENCHMARKS.md).

---

## The 3-Step Agent Loop

1. **Search** — Locate up to 5 distinct files (`-n` up to 50) ranked production code first, with auto-refresh and `total` / `truncated` metadata.
2. **Read** — Fetch exact line ranges (`-s`..`-e`, optional `--numbered`) with whole-file SHA-256 `hash` and safe truncation caps.
3. **Edit** — Replace a line range (`-s`..`-e`) or exact anchor block (`--old`) from stdin with `--hash` protection; Proxer reindexes **only that file**.

```bash
# 1. Search (auto-refreshes the index)
./.agents/skills/proxer/scripts/proxer search -k "authMiddleware"

# 2. Read target lines and capture file hash
./.agents/skills/proxer/scripts/proxer read -f "internal/auth.go" -s 52 -e 58 --numbered

# 3. Edit with hash guard + automatic single-file reindex
./.agents/skills/proxer/scripts/proxer edit -f "internal/auth.go" -s 52 -e 58 --hash "1f0c..." <<'EOF'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
EOF
```

---

## Quick Start

### 1. Build & Install into a Project

```bash
make build
make install DEST=/path/to/your-project
```

Installs the binary and `SKILL.md` into `<DEST>/.agents/skills/proxer/` without touching `.gitignore` or project configs.

### 2. Optional: Enable MCP Server

Add Proxer to your harness MCP config (e.g., `.pi/mcp.json`, Claude, or Cursor):

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

Exposes `index`, `search`, `read`, `edit`, and `status` over stdio.

---

## Highlights

- **Bounded payloads:** 60-line chunks (15-line overlap), 5 distinct files by default, 4,096-char line cap and 64 KB total cap on `read`, and automatic skipping of minified blobs.
- **Fresh by default:** `search` runs a stat-only delta walk (~63.5 ms on 8,000 files) and re-chunks only changed files.
- **Code-aware matching:** Token-prefix matching, `camelCase`/`PascalCase` tail indexing, `AND`-first with `OR` fallback, and 5× path weight.
- **Safe & isolated:** Strict root confinement, `.gitignore` + sensitive-file filtering, SHA-256 `--hash` guards, atomic writes, and isolated per-project DBs (`--root` → `PROXER_ROOT` → cwd).
- **Zero runtime dependencies:** Single CGO-free static Go binary.

---

## Documentation

- [CLI & Configuration Reference](docs/REFERENCE.md) — commands, flags, JSON schemas, `.proxer.json`, DB routing, and limitations
- [Harness & MCP Integration](docs/INTEGRATION.md) — Agent Skill and MCP stdio server setup
- [Benchmarks & Performance](docs/BENCHMARKS.md) — latest measurements, trade-offs, and reproduction script
- [Agent Skill](skills/proxer/SKILL.md) — bundled prompt specification for coding agents

## License

Released under the [MIT License](LICENSE).
