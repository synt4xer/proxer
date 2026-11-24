# Harness Integration

Proxer integrates with coding agent harnesses in two ways:
1. **Agent Skill (CLI)** — invoked via shell from `.agents/skills/proxer/scripts/proxer`.
2. **MCP Server (`proxer mcp`)** — exposes `index`, `search`, `read`, `edit`, and `status` over stdio JSON-RPC.

Both surfaces share the same indexing engine, path confinement, and pre-search refresh behavior.

---

## 1. Agent Skill Setup

`skills/proxer/SKILL.md` follows the [Agent Skills specification](https://agentskills.io/specification). Any harness that discovers `.agents/skills/<name>/SKILL.md` (such as Claude Code or Pi) picks it up automatically.

```bash
make build
make install DEST=/path/to/your-project
```

This installs `<DEST>/.agents/skills/proxer/SKILL.md` and `<DEST>/.agents/skills/proxer/scripts/proxer` without modifying `.gitignore` or project configs. For a user-level install shared across projects, run `make install DEST=$HOME`.

---

## 2. MCP Server Setup

Register `proxer mcp` in your harness MCP config (e.g., `.pi/mcp.json`, Claude, or Cursor):

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

Or via CLI in harnesses that support it:

```bash
# Pi
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

## 3. Verification Status

| Commit | Date | Surfaces Verified | Flows |
| --- | --- | --- | --- |
| `5cff2ae` | 2026-11-24 | CLI (`proxer`), MCP stdio (`proxer mcp`), Agent Skill (`SKILL.md`) | `index`, `search`, `read` (`--numbered`, 64 KB cap), `edit` (range + `--old` anchor + `--hash`), `status`/`doctor` (`--vacuum`, `--prune`) on macOS arm64 (`CGO_ENABLED=0`, `modernc.org/sqlite v1.62.0`). |
