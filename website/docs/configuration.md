---
title: Configuration
sidebar_position: 4
---

# Configuration

Derbent reads one TOML file, `config.toml` in your user config directory:

- Windows: `%AppData%\derbent\config.toml`
- macOS: `~/Library/Application Support/derbent/config.toml`
- Linux: `$XDG_CONFIG_HOME/derbent/config.toml`, else `~/.config/derbent/config.toml`

Pass `--config <path>` to `derbent mcp`, `derbent gate` or `derbent config check` to read another file.

Without a config file, every call is allowed. Once the file exists, it must hold at least one
[rule](./rules.md), and the last rule must have no `agent`, `tool` or `args` condition, so every call
gets its decision from your file.

The file is decoded strictly. An unknown key is an error that names it (`unknown keys: ...`), and a
syntax error names its line. A typo never passes silently.

## A complete example

```toml
[servers.github]
command = ["github-mcp-server", "stdio"]
env     = { GITHUB_PERSONAL_ACCESS_TOKEN = "${env:GITHUB_TOKEN}" }

[servers.docs]
url = "https://docs.example.com/mcp"

[[rule]]
tool   = "memory_*"
action = "allow"

[[rule]]
agent  = "copilot"
tool   = "github__*"
action = "deny"

[[rule]]
tool   = "github__create_*"
action = "ask"

[[rule]]
tool   = "native__Bash"
args   = { command = "git push*" }
action = "ask"

[[rule]]
action = "ask"

[approvals]
timeout = "50s"

[receipts]
redact = ['(?i)bearer\s+\S+', 'ghp_[A-Za-z0-9]{36}']
```

## Keys

| Key | Value | Default | See |
|---|---|---|---|
| `[[rule]]` | a list of rules, tried in order | no file: allow every call | [Rules](./rules.md) |
| `agent` | glob over the agent name | any agent | [Rules](./rules.md) |
| `tool` | glob over the tool name | any tool | [Rules](./rules.md) |
| `args` | table of globs over top-level string arguments | no condition | [Rules](./rules.md) |
| `action` | `allow`, `deny` or `ask` | required | [Rules](./rules.md) |
| `[servers.<name>]` | one MCP server behind the gate | none | [MCP servers](./mcp-servers.md) |
| `command` | program and arguments of a stdio server | set `command` or `url` | [MCP servers](./mcp-servers.md) |
| `env` | environment for a command server; may use `${env:NAME}` | none | [MCP servers](./mcp-servers.md) |
| `url` | address of an HTTP server | set `command` or `url` | [MCP servers](./mcp-servers.md) |
| `headers` | HTTP headers for a url server; may use `${env:NAME}` | none | [MCP servers](./mcp-servers.md) |
| `[approvals] timeout` | a duration such as `"50s"` or `"2m"`, at least one second | `"50s"` | [Approvals](./approvals.md) |
| `[receipts] redact` | regular expressions (Go syntax) masked in stored arguments | none | [Receipts](./receipts.md) |

## When changes take effect

- `derbent mcp` reads the config once, when the agent CLI starts it. Start a new agent session after a
  change.
- `derbent gate` runs once per tool call, so the hook picks up a change on the next call.

## Check your config

```bash
derbent config check
```

It loads the config, starts every server once, and prints what the agents would get:

```text
config: <path>
rules: <number of rules>
server github: <n> tools
  github__<tool>
  github__<tool>  (left out: its input schema is not an object)
server docs: not running
```

A missing file is reported as `(not found, using defaults)`. A tool marked `left out` is not offered to
the agents, and why is shown next to it. If a server does not start, the command ends with
`config check failed`.
