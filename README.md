# Proxer

[![codecov](https://codecov.io/gh/synt4xer/proxer/graph/badge.svg)](https://codecov.io/gh/synt4xer/proxer)

**Bounded-payload code search, targeted read, and hash-guarded edit for AI coding agents.**

Single static binary (CGO-free SQLite FTS5) · CLI, [Agent Skill](https://agentskills.io/specification), and MCP server.

---

## Why Proxer?

Shell tools like `rg`, `grep`, `sed`, and ad-hoc `python3` scripts are built for human terminals, not LLM prompt budgets. In an agent loop, they either flood the context window with megabytes of unranked lines or return `0 hits` on natural multi-word and `camelCase` queries.

Measured `[CLI end-to-end]` on the reproducible **8,000-file / 72,000-chunk Go fixture** (`2026-11-24`, commit `7303375`, `macOS arm64 (Apple M1)`, `CGO_ENABLED=0`, `modernc.org/sqlite v1.62.0`, median of 7 runs; mixed-tree payload comparisons from [docs/BENCHMARKS.md](docs/BENCHMARKS.md)):

| Agent Operation (`[CLI end-to-end]`) | `proxer` | `rg` / `grep -rn` | `sed` / `python3` script |
| --- | --- | --- | --- |
| **Rare symbol** (`verifySignatureToken7722`, 1 chunk / ≤50, 1 occ.) | **479–539 B** · **10.3 / 50.3 ms** (`--no-refresh` / refreshed) | **71–74 B** (`rg` 133 ms, `grep` 792 ms) | **74 B** (`python3` walk: 659 ms) |
| **Common term** (`func`, 72k chunks · 656k occ., 9.1/chunk) | **1.0–1.5 KB** (top 5 code files) · **100.9 / 141.1 ms** | **18.4–29.2 MB** / ~241k lines (`rg` 220 ms, `grep` 2.7 s) | — |
| **Common term** (`if`, 72k chunks · 656k occ., 9.1/chunk) | **1.0–1.5 KB** (top 5 code files) · **101.4 / 141.1 ms** | Unbounded line flood | — |
| **Common multi-term** (`context value`, 72k chunks · 1.32M + 2.62M occ., 18.3 + 36.4/chunk) | **1.5–1.8 KB** (`AND`-first) · **361.1 / 402.3 ms** | **0 hits** (literal phrase) | — |
| **Multi-term / `camelCase`** (`AuthMiddleware session`, `middleware` → `AuthMiddleware`) | **1.5–1.6 KB** (sub-token tails, skips minified bundles) | **0 hits** (default) or **256 KB** (`-i` dumps minified JS) | — |
| **Read slice** on a 144 KB single-line file (lines 1–5) | **4.4 KB** in **11.8 ms** (4,096-char line cap + 64 KB total cap + SHA-256 `hash`) | — | **144.1 KB** (`sed` 4 ms, `python3` 28 ms; no hash) |
| **Edit line range** (`--hash` guard + index update) | **15–19 ms** (verifies SHA-256 + reindexes edited file) | — | **5 ms** (`sed -i`, fragile escaping) / **28 ms** (`python3`, blind overwrite) |

### Proxer vs. Built-in Harness Tools (`Grep` / `Glob` / `Read`)

Modern coding harnesses ship their own `Grep`, `Glob`, and `Read` tools with output truncation. Proxer is complementary—use each where it wins:

| Scenario | Built-in `Grep` / `Glob` / `Read` | Proxer (`search` / `read` / `edit`) | Winner |
| --- | --- | --- | --- |
| **Cold single query on an unindexed repo** | Immediate scan (~130 ms at 8k files); zero index build | Pays initial index build (~4.25 s on 72k dense Go chunks) | **Built-in** |
| **Full-regex or punctuation-heavy sweep** | Native regex engine across all text | FTS5 token-prefix + `camelCase` tails only (no regex) | **Built-in** |
| **Small rare-symbol hit (<5 matches)** | Compact `file:line` list (~70–400 B) | ~500 B JSON envelope with 60-line chunk context | **Built-in** |
| **Warm multi-term / `camelCase` ranked discovery** | Literal/regex only; unranked filesystem order | `AND`-first BM25 + symbol/path/kind ranking (`code` → `test` → `docs`) | **Proxer** |
| **Token-bounded chunk discovery & capped read** | Arbitrary truncation or full-file dumps | 60-line chunks, 5 files default, 4,096-char line cap & 64 KB read cap with `total`/`truncated`/`next_start` | **Proxer** |
| **Concurrent-safe targeted edit** | No index update; varies on stale-file guards | Atomic write + whole-file SHA-256 `--hash` guard + instant single-file reindex | **Proxer** |

> Pair Proxer with a language server (`gopls`, `tsserver`, `rust-analyzer`) for type resolution and call graphs. Full fixture recipe, SQL breakdown, and methodology in [docs/BENCHMARKS.md](docs/BENCHMARKS.md).

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

`make install` installs the binary and `SKILL.md` into `<DEST>/.agents/skills/proxer/` without modifying `.gitignore` or project config files (add `.proxer.db*` to `.gitignore` or `.git/info/exclude` yourself if desired).

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
