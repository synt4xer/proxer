# Harness Integration

Proxer fits an agent harness in two ways: as an Agent Skill invoked through the shell, or as an MCP server whose tools the model calls directly.

## Agent Skills

`skills/proxer/SKILL.md` follows the [Agent Skills specification](https://agentskills.io/specification), so any harness that discovers `.agents/skills/<name>/SKILL.md` picks Proxer up with no further configuration. Pi, Claude Code, and other Agent Skills implementations read the same layout.

`make install` already places the package in the right location, so integration is just:

1. Install into the target project (`make install DEST=...`).
2. Confirm the harness lists `proxer` among its available skills after restart, and that `/skill:proxer` or an equivalent command loads it.
3. Ask the agent to use it, for example: _"Use proxer to find where `AuthMiddleware` is defined, read that range, and rename it to `RequireSession`."_

The skill body teaches the agent the order of operations: `index` first, then `search` → `read` → `edit`. Keep `SKILL.md` in sync with the CLI flags whenever they change.

Proxer exposes no protocol server by default: the harness invokes the binary directly. If the harness speaks MCP, run it as a server instead (below).

## MCP Server

`proxer mcp` runs a stdio [MCP](https://modelcontextprotocol.io) server that exposes the four operations as tools named `index`, `search`, `read`, and `edit`. Use it when the harness speaks MCP and you want the tools in the agent's tool list rather than invoked as shell commands. The CLI and the Agent Skill keep working unchanged.

The server resolves the root the same way the CLI does (`--root`, then `PROXER_ROOT`, then the working directory), opens `<root>/.proxer.db` for its lifetime, and closes it on shutdown. It writes only JSON-RPC to stdout; errors go to stderr. Every `filepath` argument is relative to the project root, and a path that resolves outside it is rejected, never clamped or rewritten.

Register it per project. Pi reads project servers from `.pi/mcp.json` and user servers from `~/.pi/agent/mcp.json`:

```json
{
  "mcpServers": {
    "proxer": {
      "command": ".agents/skills/proxer/scripts/proxer",
      "args": ["mcp", "--root", "."],
      "cwd": ".",
      "exposure": "direct",
      "description": "Search, read, and edit this project through the proxer index"
    }
  }
}
```

`exposure: "direct"` publishes each tool as `mcp__proxer__<tool>`. Without it a harness may hide the tools behind code-mode search. The equivalent command:

```bash
pi mcp add -l proxer -- .agents/skills/proxer/scripts/proxer mcp --root .
```

| Tool | Parameters | Result |
| --- | --- | --- |
| `index` | none | `{"status":"indexed_successfully"}` |
| `search` | `keyword` | array of `{filepath, line, snippet}` |
| `read` | `filepath`, `start` (default 1), `end` (default 60) | `{filepath, content}` |
| `edit` | `filepath`, `start`, `end`, `content` | `{status, message}` |
