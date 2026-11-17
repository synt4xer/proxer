# Harness Integration

Proxer fits an agent harness in two ways: as an Agent Skill invoked through the shell, or as an MCP server whose tools the model calls directly. This guide describes `v0.5.0`; the tool result shapes below are the ones that release returns.

Whatever the interface, `search` brings the index up to date before it queries, so a harness never has to sequence `index` before `search`. The database carries a schema stamp and a per-file manifest, so a project that already ran an older Proxer is reindexed once on the first command rather than answered from rows the new binary cannot account for.

## Agent Skills

`skills/proxer/SKILL.md` follows the [Agent Skills specification](https://agentskills.io/specification), so any harness that discovers `.agents/skills/<name>/SKILL.md` picks Proxer up with no further configuration. Agent Skills implementations such as Claude Code and Pi read the same layout.

`make install` already places the package in the right location, so integration is just:

1. Install into the target project (`make install DEST=...`).
2. Confirm the harness lists `proxer` among its available skills after restart, and that `/skill:proxer` or an equivalent command loads it.
3. Ask the agent to use it, for example: _"Use proxer to find where `AuthMiddleware` is defined, read that range, and rename it to `RequireSession`."_

The skill body teaches the agent the order of operations: `search` → `read` → `edit`, with `index` as an optional cost paid up front. `search` refreshes the index itself, so a missing database is built on the first query. Keep `SKILL.md` in sync with the CLI flags whenever they change.

The skill is a thin instruction file over the same binary the MCP server runs, so the matching rules, the path containment, and the reindex-after-edit behaviour described below hold for both. The one asymmetry is the per-call refresh opt-out: `search --no-refresh` exists only on the CLI, because the environment variable covers the server case.

Proxer exposes no protocol server by default: the harness invokes the binary directly. If the harness speaks MCP, run it as a server instead (below).

## MCP Server

`proxer mcp` runs a stdio [MCP](https://modelcontextprotocol.io) server that exposes the four operations as tools named `index`, `search`, `read`, and `edit`. Use it when the harness speaks MCP and you want the tools in the agent's tool list rather than invoked as shell commands. The CLI and the Agent Skill keep working unchanged.

The server resolves the root the same way the CLI does (`--root`, then `PROXER_ROOT`, then the working directory), opens `<root>/.proxer.db` for its lifetime, and closes it on shutdown. It reports itself to the client as `proxer` version `0.5.0`. It writes only JSON-RPC to stdout; errors go to stderr. Every `filepath` argument is relative to the project root, and a path that resolves outside it is rejected, never clamped or rewritten. Every `search` and `read` result carries the absolute `root` it came from, so a harness talking to several projects can tell which index a path belongs to. `search` also reports `indexed_at`, the counts its pre-query refresh touched, `total`, and whether the answer was `truncated`, and `PROXER_NO_REFRESH=1` in the server environment turns that refresh off for callers that must not write. `read` also returns a `hash` of the whole file: pass it to `edit` and a change in between rejects the edit instead of overwriting code the model has not seen.

Register it per project, using the config file your harness reads. The shape below is common to MCP clients; extra keys vary by harness.

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

Pi is one example. It reads project servers from `.pi/mcp.json` and user servers from `~/.pi/agent/mcp.json`, and its `exposure: "direct"` key publishes each tool as `mcp__proxer__<tool>` instead of hiding it behind code-mode search. A harness that offers a command can register the same server from a shell:

```bash
# Pi
pi mcp add -l proxer -- .agents/skills/proxer/scripts/proxer mcp --root .
```

| Tool | Parameters | Result |
| --- | --- | --- |
| `index` | none | `{status, refreshed}` — incremental; `refreshed` counts added, changed, and removed files, and three zeros mean the index was already current |
| `search` | `keyword`, `limit` (default 5, clamped 1..50), `kind` (`code`, `docs`, or `all`) | `{root, indexed_at, refreshed, total, truncated, results[]}`, each result `{root, filepath, line, snippet, match}`; `refreshed` is absent when `PROXER_NO_REFRESH=1`, and `results` is an empty array, not null, when nothing matches |
| `read` | `filepath`, `start` (default 1), `end` (default 60) | `{root, filepath, content, hash}` |
| `edit` | `filepath`, `start`, `end`, `content`, `hash` (optional) | `{status, message}` |

`indexed_at` is when the index last changed, not when this call checked it, so a model can tell a fresh answer from a replayed one. `line` is the chunk boundary (`"136-181"`), not the exact hit; the snippet wraps the matches in `<b>` tags for the model to locate them inside the chunk. `match` says whether the terms were found in the path, the content, or both, so a path-only hit does not send the model reading a chunk that has nothing to do with it. `total` counts every matching chunk and `truncated` says whether the result list is smaller than the match, which is what makes `limit` a decision the model can make rather than a wall. A client written before v0.4.0 that read `search`'s top-level array must read `results` instead.

Two behaviours are worth knowing when you write the tool descriptions in a harness prompt:

- `search` returns at most `limit` files (five by default), ordered code first, then docs, then generated assets, so a broad keyword is truncated rather than complete. `truncated` and `total` say so, and the model should treat a hit as a starting point and follow up with `read`.
- `edit` rejects a `hash` that no longer matches the file, and the rejection is an error, not a silent no-op. Carry the hash from `read` so a concurrent write is refused instead of overwritten.
