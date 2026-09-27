---
title: Connect your agents
sidebar_position: 3
---

# Connect your agents

Each agent CLI starts Derbent as a stdio MCP server with `derbent mcp --agent <name>`. Name the MCP entry
`derbent` in every CLI: the [pre-tool hook](./built-in-tools.md) recognises Derbent's own tools by that
name.

## Claude Code

```bash
claude mcp add derbent -- derbent mcp --agent claude
```

## Codex

```bash
codex mcp add derbent -- derbent mcp --agent codex
```

Codex starts MCP servers with only a few environment variables. If your Derbent config uses
`${env:NAME}` (see [MCP servers](./mcp-servers.md)), add `env_vars` to Derbent's entry in
`~/.codex/config.toml`:

```toml
[mcp_servers.derbent]
# keep the command and args that `codex mcp add` wrote, and add:
env_vars = ["GITHUB_TOKEN"]
```

Without it the gate does not start, and its error names the missing variable.

## GitHub Copilot CLI and Antigravity CLI

Add a stdio MCP server named `derbent` in the CLI's MCP settings, with this command:

```bash
derbent mcp --agent copilot
derbent mcp --agent antigravity
```

## The agent name

The value of `--agent` is the name your [rules](./rules.md) match.

- It is 1 to 32 lower-case letters, digits, dashes or underscores. Anything else stops the gate with
  `--agent must be 1 to 32 lower-case letters, digits, dashes or underscores`.
- Give the CLI's pre-tool hook the same name (`derbent gate --agent claude`), so one rule covers both.
- It is a label, not authentication. Any process running as you could start a gate under any name.

## Check that it works

Each agent now has three extra tools: `memory_write`, `memory_search` and `memory_read`. Ask one agent to
save a note, then ask another agent, working in the same repository, to search for it. Every call is
recorded:

```bash
derbent receipts
derbent verify
```

`derbent mcp` also takes `--project`, `--config` and `--db`. See the [CLI reference](./cli.md).
