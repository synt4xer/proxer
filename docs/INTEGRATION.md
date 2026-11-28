# Installation & Harness Integration

Proxer integrates with coding agent harnesses in two ways:
1. **Agent Skill (CLI)** — installed into a **project** skill directory (`.agents/skills/proxer/`) or a **harness** skill directory (`~/.agents/skills/proxer/`, `~/.claude/skills/proxer/`, `~/.cursor/skills/proxer/`, etc.), with `SKILL.md` at the skill root and the binary at `scripts/proxer` (`scripts/proxer.exe` on Windows).
2. **MCP Server (`proxer mcp`)** — exposes `index`, `search`, `read`, `edit`, and `status` over stdio JSON-RPC, running either the installed project/harness skill binary or a binary placed in your harness's MCP directory.

Both surfaces share the same indexing engine, path confinement, and pre-search refresh behavior.

---

## 1. Installation

### Pre-built Releases (Linux, macOS, Windows)

Every tagged release (`v*`) publishes CGO-free static binaries via GoReleaser on [GitHub Releases](https://github.com/synt4xer/proxer/releases):

| OS | Architectures | Archive Format | Binary |
| --- | --- | --- | --- |
| **Linux** | `amd64`, `arm64` | `proxer_<version>_linux_<arch>.tar.gz` | `proxer` |
| **macOS** | `amd64`, `arm64` | `proxer_<version>_darwin_<arch>.tar.gz` | `proxer` |
| **Windows** | `amd64`, `arm64` | `proxer_<version>_windows_<arch>.zip` | `proxer.exe` |

Each archive contains:
- `proxer` (or `proxer.exe` on Windows)
- `skills/proxer/SKILL.md` (Agent Skill specification)
- `README.md` and `LICENSE`

SHA-256 digests for all archives are published alongside each release in `checksums.txt`.

### Choosing an Install Directory

Proxer detects its install location automatically to decide where to store the SQLite index (`--db` or `PROXER_DB` always overrides this):

| Install Target | Layout | Automatic DB Routing |
| --- | --- | --- |
| **Project** | `<project>/.agents/skills/proxer/{SKILL.md,scripts/proxer[.exe]}` | `<project>/.proxer.db` (`install_mode: "project"`) |
| **Harness (user-level)** | `<home-hidden-or-config>/.../skills/proxer/{SKILL.md,scripts/proxer[.exe]}` (e.g. `~/.agents/skills/proxer`, `~/.claude/skills/proxer`, `~/.cursor/skills/proxer`, or `PROXER_SKILL_DIR`) | `<skill-dir>/database/<repo>-<hash12>.db` (`install_mode: "harness"`) |
| **MCP-only directory** | `<mcp-dir>/proxer[.exe]` (e.g. `~/.mcp/proxer/proxer[.exe]`) | `<project>/.proxer.db` if the project has one, otherwise `~/.agents/skills/proxer/database/<repo>-<hash12>.db` |

> Installing the binary inside a `skills/proxer` directory under your home/config directory is what enables automatic per-repo DB isolation in harness mode without creating `.proxer.db` inside every repository.

---

### Option A: Project or Harness Skill Install (Recommended)

#### macOS & Linux

```bash
VERSION=0.7.2
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
ARCHIVE="proxer_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/synt4xer/proxer/releases/download/v${VERSION}"

# Pick ONE target:
#   Project: SKILL_DIR="/path/to/your-project/.agents/skills/proxer"
#   Harness: SKILL_DIR="$HOME/.agents/skills/proxer"   # or $HOME/.claude/skills/proxer, $HOME/.cursor/skills/proxer
SKILL_DIR="/path/to/your-project/.agents/skills/proxer"

tmp=$(mktemp -d)
curl -sSL "${BASE_URL}/${ARCHIVE}" -o "$tmp/${ARCHIVE}"
curl -sSL "${BASE_URL}/checksums.txt" -o "$tmp/checksums.txt"

# Verify SHA-256 checksum (Linux: sha256sum, macOS: shasum -a 256)
(cd "$tmp" && grep " ${ARCHIVE}$" checksums.txt | (command -v sha256sum >/dev/null && sha256sum -c - || shasum -a 256 -c -))

tar -xzf "$tmp/${ARCHIVE}" -C "$tmp"
mkdir -p "$SKILL_DIR/scripts"
cp "$tmp/skills/proxer/SKILL.md" "$SKILL_DIR/SKILL.md"
cp "$tmp/proxer" "$SKILL_DIR/scripts/proxer"
chmod +x "$SKILL_DIR/scripts/proxer"
rm -rf "$tmp"

"$SKILL_DIR/scripts/proxer" --version
```

On macOS, if Gatekeeper blocks an unsigned downloaded binary, clear the quarantine attribute once:

```bash
xattr -d com.apple.quarantine "$SKILL_DIR/scripts/proxer"
```

#### Windows (PowerShell)

```powershell
$Version = "0.7.2"
$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$Archive = "proxer_${Version}_windows_${Arch}.zip"
$BaseUrl = "https://github.com/synt4xer/proxer/releases/download/v$Version"

# Pick ONE target:
#   Project: $SkillDir = "C:\path\to\your-project\.agents\skills\proxer"
#   Harness: $SkillDir = "$HOME\.agents\skills\proxer"   # or $HOME\.claude\skills\proxer, $HOME\.cursor\skills\proxer
$SkillDir = "C:\path\to\your-project\.agents\skills\proxer"

$Tmp = Join-Path $env:TEMP "proxer-install"
New-Item -ItemType Directory -Force -Path $Tmp | Out-Null
Invoke-WebRequest -Uri "$BaseUrl/$Archive" -OutFile (Join-Path $Tmp $Archive)
Invoke-WebRequest -Uri "$BaseUrl/checksums.txt" -OutFile (Join-Path $Tmp "checksums.txt")

# Verify SHA-256 checksum
$Expected = (Select-String -Path (Join-Path $Tmp "checksums.txt") -Pattern " $Archive$").Line.Split(" ", [System.StringSplitOptions]::RemoveEmptyEntries)[0]
$Actual = (Get-FileHash -Algorithm SHA256 (Join-Path $Tmp $Archive)).Hash.ToLower()
if ($Actual -ne $Expected) { throw "Checksum mismatch: expected $Expected, got $Actual" }

Expand-Archive -Path (Join-Path $Tmp $Archive) -DestinationPath $Tmp -Force
New-Item -ItemType Directory -Force -Path (Join-Path $SkillDir "scripts") | Out-Null
Copy-Item (Join-Path $Tmp "skills\proxer\SKILL.md") (Join-Path $SkillDir "SKILL.md") -Force
Copy-Item (Join-Path $Tmp "proxer.exe") (Join-Path $SkillDir "scripts\proxer.exe") -Force
Remove-Item -Recurse -Force $Tmp

& (Join-Path $SkillDir "scripts\proxer.exe") --version
```

---

### Option B: MCP-Only Directory Install

If you already installed the Agent Skill above, you do **not** need a second binary — point your MCP config directly at `<SKILL_DIR>/scripts/proxer` (or `proxer.exe`).

If you only want the MCP server binary in a dedicated MCP directory (without `SKILL.md`):

**macOS & Linux:**

```bash
VERSION=0.7.2
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
MCP_DIR="$HOME/.mcp/proxer" # or your harness's MCP servers directory

mkdir -p "$MCP_DIR"
curl -sSL "https://github.com/synt4xer/proxer/releases/download/v${VERSION}/proxer_${VERSION}_${OS}_${ARCH}.tar.gz" | tar -xz -C "$MCP_DIR" proxer
chmod +x "$MCP_DIR/proxer"
```

**Windows (PowerShell):**

```powershell
$Version = "0.7.2"
$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$Archive = "proxer_${Version}_windows_${Arch}.zip"
$McpDir = "$HOME\.mcp\proxer" # or your harness's MCP servers directory

New-Item -ItemType Directory -Force -Path $McpDir | Out-Null
Invoke-WebRequest -Uri "https://github.com/synt4xer/proxer/releases/download/v$Version/$Archive" -OutFile "$env:TEMP\$Archive"
Expand-Archive -Path "$env:TEMP\$Archive" -DestinationPath "$env:TEMP\proxer-mcp" -Force
Copy-Item "$env:TEMP\proxer-mcp\proxer.exe" (Join-Path $McpDir "proxer.exe") -Force
Remove-Item -Recurse -Force "$env:TEMP\proxer-mcp", "$env:TEMP\$Archive"
```

---

### Option C: Build from Source

Requires Go 1.27+ and `make`:

```bash
make build
# Project install:
make install DEST=/path/to/your-project

# Or user-level harness install ($HOME/.agents/skills/proxer/):
make install DEST="$HOME"
```

`make install` copies `SKILL.md` and the binary into `<DEST>/.agents/skills/proxer/` without modifying `.gitignore` or project configs.

---

## 2. How to Use (CLI & Agent Skill)

`skills/proxer/SKILL.md` follows the [Agent Skills specification](https://agentskills.io/specification). Any harness that discovers `.agents/skills/<name>/SKILL.md` (or its own `<harness>/skills/<name>/SKILL.md`, such as Claude Code, Cursor, or Pi) picks it up automatically.

All commands write JSON to stdout and errors to stderr with a non-zero exit code.

### Common Commands

The examples below assume a **project-local install**. If you installed into a **harness directory** (e.g., `$HOME/.agents/skills/proxer`), substitute `$HOME/.agents/skills/proxer/scripts/proxer` (or `$HOME\.agents\skills\proxer\scripts\proxer.exe`) and run from the target repo or pass `--root <path>` / `PROXER_ROOT`.

**macOS & Linux:**

```bash
PROXER=./.agents/skills/proxer/scripts/proxer

# Search (auto-refreshes index before querying)
"$PROXER" search -k "authMiddleware" -n 5 --kind code

# Read a bounded line range with line numbers and full-file SHA-256 hash
"$PROXER" read -f "internal/auth.go" -s 40 -e 70 --numbered

# Line-range edit with --hash guard
"$PROXER" edit -f "internal/auth.go" -s 52 -e 58 --hash "<sha256>" <<'EOF'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
EOF

# Anchor-mode edit (replaces exact --old block)
printf '%s' 'return requireSession(next)' | "$PROXER" edit -f "internal/auth.go" --old "return oldHandler(next)" --hash "<sha256>"

# Inspect index health, size, and DB routing
"$PROXER" status
```

**Windows (PowerShell):**

```powershell
$Proxer = ".\.agents\skills\proxer\scripts\proxer.exe"

# Search
& $Proxer search -k "authMiddleware" -n 5 --kind code

# Read
& $Proxer read -f "internal/auth.go" -s 40 -e 70 --numbered

# Line-range edit with --hash guard (line-range mode trims trailing CRLF)
@'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
'@ | & $Proxer edit -f "internal/auth.go" -s 52 -e 58 --hash "<sha256>"

# Inspect index health
& $Proxer status
```

> **Windows tip:** PowerShell pipelines append a trailing newline when piping strings to native commands. Line-range mode (`-s`/`-e`) trims trailing `\r\n` automatically; for verbatim `--old` anchor edits on Windows, prefer the MCP `edit` tool (which passes `content` as a JSON field).

---

## 3. MCP Server Setup

Register `proxer mcp` in your harness MCP config (e.g., `.pi/mcp.json`, `.mcp.json`, Claude, or Cursor).

**Project-local install:**

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

On Windows, set `"command": ".agents/skills/proxer/scripts/proxer.exe"`.

**Harness-level or dedicated MCP directory install:**

JSON MCP configs generally do not expand `~` or `$HOME`, so use the full binary path:

- **macOS / Linux (harness):** `"/Users/<you>/.agents/skills/proxer/scripts/proxer"` (or `"/home/<you>/..."`)
- **macOS / Linux (MCP dir):** `"/Users/<you>/.mcp/proxer/proxer"`
- **Windows (harness):** `"C:\\Users\\<you>\\.agents\\skills\\proxer\\scripts\\proxer.exe"`
- **Windows (MCP dir):** `"C:\\Users\\<you>\\.mcp\\proxer\\proxer.exe"`

Or register via CLI in harnesses that support it:

```bash
# Pi (project-local install on macOS/Linux)
pi mcp add -l proxer -- .agents/skills/proxer/scripts/proxer mcp --root .

# Pi (harness-level install on macOS/Linux)
pi mcp add -l proxer -- "$HOME/.agents/skills/proxer/scripts/proxer" mcp --root .
```

### Server Behavior

- **Root & DB routing:** Resolves root via `--root` → `PROXER_ROOT` → working directory. Uses `<root>/.proxer.db` for project-local installs, or `<skillDir>/database/<name>-<hash12>.db` for harness-level and MCP-only installs (override with `--db` or `PROXER_DB`).
- **Refresh cooldown:** `search` auto-refreshes before querying with a 2-second cooldown (invalidated immediately after `edit`). Set `PROXER_NO_REFRESH=1` in the server environment to force read-only searches.
- **Read-only fallback:** If the DB is read-only on disk, `proxer mcp` opens it in read-only mode so `search` and `status` still work (marking `search` responses with `"stale": true`).
- **Stdio isolation:** Writes only JSON-RPC to stdout; errors go to stderr.

### Exposed MCP Tools

| Tool | Parameters | Key Result Fields |
| --- | --- | --- |
| `index` | none | `status`, `refreshed`, `db_size_bytes`, `index_sensitive`, `purge_pending`, `warning` |
| `search` | `keyword` (required), `limit` (1..50, default 5), `max_per_file` (1..5, default 1), `kind` (`all`\|`code`\|`docs`) | `root`, `indexed_at`, `stale`, `refreshed`, `total`, `truncated`, `results[]` (`root`, `filepath`, `chunk`, `line`, `snippet`, `match`, `hash`) |
| `read` | `filepath` (required), `start` (default 1), `end` (default 60), `numbered` (default false) | `root`, `filepath`, `start_line`, `end_line`, `total_lines`, `truncated`, `next_start`, `content`, `hash` |
| `edit` | `filepath` (required), `content` (required), `start`/`end` **or** `old_content` (+ optional `occurrence`), `hash` (optional) | `status`, `message`, `hash`, `reindex_failed`, `reindex_error` |
| `status` | `vacuum` (default false) | `root`, `db_path`, `db_size_bytes`, `db_mode`, `schema_version`, `indexed_at`, `files_indexed`, `chunks_indexed`, `install_mode`, `ignore_sources` |

### Prompting Tips for Harnesses

- Treat `search` results as bounded entry points (`total` and `truncated` tell the model when more matches exist).
- Always pass the `hash` from `read` (or `search`) into `edit` so concurrent writes fail safely instead of overwriting unseen changes.
- Use `read` with `numbered: true` before line-range edits, or use `edit`'s `old_content` anchor mode when exact line numbers aren't needed.

---

## 4. Verification Scope

| Surface | Verified Flows |
| --- | --- |
| CLI (`proxer`) | `index`, `search`, `read` (`--numbered`, 64 KB cap), `edit` (range + `--old` anchor + `--hash`), `status`/`doctor` (`--vacuum`, `--prune`) |
| MCP stdio (`proxer mcp`) | `index`, `search`, `read`, `edit`, `status`, 2s refresh cooldown, read-only fallback |
| Agent Skill (`SKILL.md`) | Project-local (`.agents/skills/proxer/`) and harness-level (`~/.agents/skills/proxer/`, `~/.claude/skills/proxer/`, etc.) installs via release archive or `make install` |
| Release Artifacts | CGO-free static cross-builds for `linux`, `darwin`, and `windows` (`amd64` + `arm64`) via GoReleaser |
