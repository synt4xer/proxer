# Harness Integration

Proxer fits an agent harness in two ways: as an Agent Skill invoked through the shell, or as an MCP server whose tools the model calls directly.

## Agent Skills

`skills/proxer/SKILL.md` follows the [Agent Skills specification](https://agentskills.io/specification), so any harness that discovers `.agents/skills/<name>/SKILL.md` picks Proxer up with no further configuration. Agent Skills implementations such as Claude Code and Pi read the same layout.

`make install` already places the package in the right location, so integration is just:

1. Install into the target project (`make install DEST=...`).
2. Confirm the harness lists `proxer` among its available skills after restart, and that `/skill:proxer` or an equivalent command loads it.
3. Ask the agent to use it, for example: _"Use proxer to find where `AuthMiddleware` is defined, read that range, and rename it to `RequireSession`."_

The skill body teaches the agent the order of operations: `search` → `read` → `edit`, with `index` as an optional cost paid up front. `search` refreshes the index itself, so a missing database is built on the first query. Keep `SKILL.md` in sync with the CLI flags whenever they change.

Proxer exposes no protocol server by default: the harness invokes the binary directly. If the harness speaks MCP, run it as a server instead (below).

## MCP Server

`proxer mcp` runs a stdio [MCP](https://modelcontextprotocol.io) server that exposes the four operations as tools named `index`, `search`, `read`, and `edit`. Use it when the harness speaks MCP and you want the tools in the agent's tool list rather than invoked as shell commands. The CLI and the Agent Skill keep working unchanged.

The server resolves the root the same way the CLI does (`--root`, then `PROXER_ROOT`, then the working directory), opens `<root>/.proxer.db` for its lifetime, and closes it on shutdown. It writes only JSON-RPC to stdout; errors go to stderr. Every `filepath` argument is relative to the project root, and a path that resolves outside it is rejected, never clamped or rewritten. Every `search` and `read` result carries the absolute `root` it came from, so a harness talking to several projects can tell which index a path belongs to. `search` also reports `indexed_at` and the counts its pre-query refresh touched, and `PROXER_NO_REFRESH=1` in the server environment turns that refresh off for callers that must not write. `read` also returns a `hash` of the whole file: pass it to `edit` and a change in between rejects the edit instead of overwriting code the model has not seen.

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
| `index` | none | `{status, refreshed}` — incremental; `refreshed` counts added, changed, and removed files |
| `search` | `keyword` | `{root, indexed_at, refreshed, results[]}`, each result `{root, filepath, line, snippet}` |
| `read` | `filepath`, `start` (default 1), `end` (default 60) | `{root, filepath, content, hash}` |
| `edit` | `filepath`, `start`, `end`, `content`, `hash` (optional) | `{status, message}` |
