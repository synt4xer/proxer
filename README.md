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

## Installation

Pre-built static binaries for **macOS**, **Linux**, and **Windows** (`amd64` and `arm64`) are published on [GitHub Releases](https://github.com/synt4xer/proxer/releases). Each archive includes the `proxer` binary and `skills/proxer/SKILL.md` for Agent Skill installs.

### macOS & Linux

```bash
# Set VERSION to any release tag (without the leading "v")
VERSION=0.7.0
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')

curl -sSL "https://github.com/synt4xer/proxer/releases/download/v${VERSION}/proxer_${VERSION}_${OS}_${ARCH}.tar.gz" -o proxer.tar.gz
tar -xzf proxer.tar.gz
mkdir -p "$HOME/.local/bin"
mv proxer "$HOME/.local/bin/proxer"
rm -f proxer.tar.gz

# Ensure $HOME/.local/bin is on your PATH, then verify:
proxer --version
```

### Windows (PowerShell)

```powershell
$Version = "0.7.0"
$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$Url = "https://github.com/synt4xer/proxer/releases/download/v$Version/proxer_${Version}_windows_${Arch}.zip"

Invoke-WebRequest -Uri $Url -OutFile proxer.zip
New-Item -ItemType Directory -Force -Path "$HOME\.local\bin" | Out-Null
Expand-Archive -Path proxer.zip -DestinationPath "$HOME\.local\bin" -Force
Remove-Item proxer.zip

# Add "$HOME\.local\bin" to your User PATH, then verify:
& "$HOME\.local\bin\proxer.exe" --version
```

### Build from Source (Go 1.27+)

```bash
# Install directly onto GOPATH/bin
CGO_ENABLED=0 go install github.com/synt4xer/proxer/cmd/proxer@latest

# Or clone and install binary + Agent Skill into a target project
make build
make install DEST=/path/to/your-project
```

For Agent Skill directory installs (from a release archive or source) and checksum verification, see [docs/INTEGRATION.md](docs/INTEGRATION.md).

---

## How to Use

### The 3-Step Agent Loop

1. **Search** — Locate up to 5 distinct files (`-n` up to 50) ranked production code first, with auto-refresh and `total` / `truncated` metadata.
2. **Read** — Fetch exact line ranges (`-s`..`-e`, optional `--numbered`) with whole-file SHA-256 `hash` and safe truncation caps.
3. **Edit** — Replace a line range (`-s`..`-e`) or exact anchor block (`--old`) from stdin with `--hash` protection; Proxer reindexes **only that file**.

**macOS & Linux (`bash`/`zsh`):**

```bash
# 1. Search (auto-refreshes the index)
proxer search -k "authMiddleware"

# 2. Read target lines and capture file hash
proxer read -f "internal/auth.go" -s 52 -e 58 --numbered

# 3. Edit with hash guard + automatic single-file reindex
proxer edit -f "internal/auth.go" -s 52 -e 58 --hash "1f0c..." <<'EOF'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
EOF
```

**Windows (PowerShell):**

```powershell
# 1. Search
proxer.exe search -k "authMiddleware"

# 2. Read target lines and capture file hash
proxer.exe read -f "internal/auth.go" -s 52 -e 58 --numbered

# 3. Edit with hash guard (pipe replacement content via stdin)
@'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
'@ | proxer.exe edit -f "internal/auth.go" -s 52 -e 58 --hash "1f0c..."
```

> Using a project-local Agent Skill install instead of a global `PATH` binary? Invoke `./.agents/skills/proxer/scripts/proxer` (macOS/Linux) or `.\.agents\skills\proxer\scripts\proxer.exe` (Windows).

### Enable MCP Server

Add Proxer to your harness MCP config (e.g., `.pi/mcp.json`, Claude, or Cursor):

```json
{
  "mcpServers": {
    "proxer": {
      "command": "proxer",
      "args": ["mcp", "--root", "."],
      "cwd": "."
    }
  }
}
```

Exposes `index`, `search`, `read`, `edit`, and `status` over stdio. For project-local skill paths and full harness setup across macOS, Linux, and Windows, see [docs/INTEGRATION.md](docs/INTEGRATION.md).

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
