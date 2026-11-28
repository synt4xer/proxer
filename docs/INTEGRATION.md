# Installation & Harness Integration

Proxer integrates with coding agent harnesses in two ways:
1. **CLI / Agent Skill** — invoked via shell as `proxer` (on `PATH`) or from `.agents/skills/proxer/scripts/proxer` (`proxer.exe` on Windows).
2. **MCP Server (`proxer mcp`)** — exposes `index`, `search`, `read`, `edit`, and `status` over stdio JSON-RPC.

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

#### macOS & Linux

**Standalone binary (on `PATH`):**

```bash
VERSION=0.7.0
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
ARCHIVE="proxer_${VERSION}_${OS}_${ARCH}.tar.gz"

curl -sSLO "https://github.com/synt4xer/proxer/releases/download/v${VERSION}/${ARCHIVE}"
curl -sSLO "https://github.com/synt4xer/proxer/releases/download/v${VERSION}/checksums.txt"

# Verify SHA-256 checksum (macOS: shasum -a 256, Linux: sha256sum)
grep " ${ARCHIVE}$" checksums.txt | (command -v sha256sum >/dev/null && sha256sum -c - || shasum -a 256 -c -)

tar -xzf "${ARCHIVE}" proxer
mkdir -p "$HOME/.local/bin"
mv proxer "$HOME/.local/bin/proxer"
rm -f "${ARCHIVE}" checksums.txt
```

On macOS, if Gatekeeper blocks an unsigned downloaded binary, clear the quarantine attribute once:

```bash
xattr -d com.apple.quarantine "$HOME/.local/bin/proxer"
```

**Agent Skill install from a release archive (no Go toolchain required):**

```bash
# Set DEST to a project root, or $HOME for a user-level install shared across repos
DEST=/path/to/your-project
VERSION=0.7.0
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')

tmp=$(mktemp -d)
curl -sSL "https://github.com/synt4xer/proxer/releases/download/v${VERSION}/proxer_${VERSION}_${OS}_${ARCH}.tar.gz" | tar -xz -C "$tmp"
mkdir -p "$DEST/.agents/skills/proxer/scripts"
cp "$tmp/skills/proxer/SKILL.md" "$DEST/.agents/skills/proxer/SKILL.md"
cp "$tmp/proxer" "$DEST/.agents/skills/proxer/scripts/proxer"
chmod +x "$DEST/.agents/skills/proxer/scripts/proxer"
rm -rf "$tmp"
```

#### Windows (PowerShell)

**Standalone binary (on `PATH`):**

```powershell
$Version = "0.7.0"
$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$Archive = "proxer_${Version}_windows_${Arch}.zip"
$BaseUrl = "https://github.com/synt4xer/proxer/releases/download/v$Version"

Invoke-WebRequest -Uri "$BaseUrl/$Archive" -OutFile $Archive
Invoke-WebRequest -Uri "$BaseUrl/checksums.txt" -OutFile checksums.txt

# Verify SHA-256 checksum
$Expected = (Select-String -Path checksums.txt -Pattern " $Archive$").Line.Split(" ", [System.StringSplitOptions]::RemoveEmptyEntries)[0]
$Actual = (Get-FileHash -Algorithm SHA256 $Archive).Hash.ToLower()
if ($Actual -ne $Expected) { throw "Checksum mismatch: expected $Expected, got $Actual" }

$InstallDir = "$HOME\.local\bin"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Expand-Archive -Path $Archive -DestinationPath "$env:TEMP\proxer-extract" -Force
Copy-Item "$env:TEMP\proxer-extract\proxer.exe" "$InstallDir\proxer.exe" -Force
Remove-Item -Recurse -Force "$env:TEMP\proxer-extract", $Archive, checksums.txt
```

Add `$HOME\.local\bin` to your User `PATH` (or run `"$HOME\.local\bin\proxer.exe"` directly), then verify with `proxer.exe --version`.

**Agent Skill install from a release archive:**

```powershell
# Set $Dest to a project root, or $HOME for a user-level install shared across repos
$Dest = "C:\path\to\your-project"
$Version = "0.7.0"
$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$Archive = "proxer_${Version}_windows_${Arch}.zip"

Invoke-WebRequest -Uri "https://github.com/synt4xer/proxer/releases/download/v$Version/$Archive" -OutFile $Archive
Expand-Archive -Path $Archive -DestinationPath "$env:TEMP\proxer-extract" -Force

$SkillDir = Join-Path $Dest ".agents\skills\proxer"
New-Item -ItemType Directory -Force -Path (Join-Path $SkillDir "scripts") | Out-Null
Copy-Item "$env:TEMP\proxer-extract\skills\proxer\SKILL.md" (Join-Path $SkillDir "SKILL.md") -Force
Copy-Item "$env:TEMP\proxer-extract\proxer.exe" (Join-Path $SkillDir "scripts\proxer.exe") -Force
Remove-Item -Recurse -Force "$env:TEMP\proxer-extract", $Archive
```

### Build from Source

Requires Go 1.27+ (and `make` for the Agent Skill helper target):

```bash
# Install standalone binary onto GOPATH/bin
CGO_ENABLED=0 go install github.com/synt4xer/proxer/cmd/proxer@latest

# Or build locally and install the Agent Skill into a project (or DEST=$HOME)
make build
make install DEST=/path/to/your-project
```

`make install` copies `SKILL.md` and the binary into `<DEST>/.agents/skills/proxer/` without modifying `.gitignore` or project configs.

---

## 2. How to Use (CLI & Agent Skill)

`skills/proxer/SKILL.md` follows the [Agent Skills specification](https://agentskills.io/specification). Any harness that discovers `.agents/skills/<name>/SKILL.md` (such as Claude Code or Pi) picks it up automatically.

All commands write JSON to stdout and errors to stderr with a non-zero exit code.

### Common Commands

**macOS & Linux:**

```bash
# Search (auto-refreshes index before querying)
proxer search -k "authMiddleware" -n 5 --kind code

# Read a bounded line range with line numbers and full-file SHA-256 hash
proxer read -f "internal/auth.go" -s 40 -e 70 --numbered

# Line-range edit with --hash guard
proxer edit -f "internal/auth.go" -s 52 -e 58 --hash "<sha256>" <<'EOF'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
EOF

# Anchor-mode edit (replaces exact --old block)
printf '%s' 'return requireSession(next)' | proxer edit -f "internal/auth.go" --old "return oldHandler(next)" --hash "<sha256>"

# Inspect index health, size, and DB routing
proxer status
```

**Windows (PowerShell):**

```powershell
# Search
proxer.exe search -k "authMiddleware" -n 5 --kind code

# Read
proxer.exe read -f "internal/auth.go" -s 40 -e 70 --numbered

# Line-range edit with --hash guard (line-range mode trims trailing CRLF)
@'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
'@ | proxer.exe edit -f "internal/auth.go" -s 52 -e 58 --hash "<sha256>"

# Inspect index health
proxer.exe status
```

> **Windows tip:** PowerShell pipelines append a trailing newline when piping strings to native commands. Line-range mode (`-s`/`-e`) trims trailing `\r\n` automatically; for verbatim `--old` anchor edits on Windows, prefer the MCP `edit` tool (which passes `content` as a JSON field).

If you installed Proxer as a project-local Agent Skill rather than onto `PATH`, replace `proxer` with `./.agents/skills/proxer/scripts/proxer` (macOS/Linux) or `.\.agents\skills\proxer\scripts\proxer.exe` (Windows). Use `--root <path>` (or `PROXER_ROOT`) to target a repository outside the current directory.

---

## 3. MCP Server Setup

Register `proxer mcp` in your harness MCP config (e.g., `.pi/mcp.json`, Claude, or Cursor).

**When `proxer` is on `PATH` (macOS, Linux, and Windows):**

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

**When installed as a project-local Agent Skill:**

- macOS / Linux: set `"command": ".agents/skills/proxer/scripts/proxer"`
- Windows: set `"command": ".agents/skills/proxer/scripts/proxer.exe"`

Or via CLI in harnesses that support it:

```bash
# Pi (with proxer on PATH)
pi mcp add -l proxer -- proxer mcp --root .

# Pi (project-local Agent Skill on macOS/Linux)
pi mcp add -l proxer -- .agents/skills/proxer/scripts/proxer mcp --root .
```

### Server Behavior

- **Root & DB routing:** Resolves root via `--root` → `PROXER_ROOT` → working directory. Uses `<root>/.proxer.db` for project-local installs, or `<skillDir>/database/<name>-<hash12>.db` for harness-level installs (override with `--db` or `PROXER_DB`).
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
| Agent Skill (`SKILL.md`) | Project-local and user-level (`$HOME`) install via release archive or `make install` |
| Release Artifacts | CGO-free static cross-builds for `linux`, `darwin`, and `windows` (`amd64` + `arm64`) via GoReleaser |
