---
title: CLI reference
sidebar_position: 11
---

# CLI reference

Where a command takes `--config` or `--db`, the defaults are `config.toml` in your user config directory and
`derbent.db` in your user state directory. See [Installation](./installation.md#where-derbent-keeps-its-files).

## derbent

```bash
derbent [--db <path>]
```

Opens the terminal UI: waiting calls, the agents seen in the last hour and a live feed of receipts. See
[Approvals](./approvals.md#the-terminal-ui) for the keys.

| Flag | Meaning |
|---|---|
| `--db` | database file. Given explicitly, a path that does not exist is refused instead of created. |

## derbent mcp

```bash
derbent mcp --agent <name> [--project <dir>] [--config <path>] [--db <path>]
```

Serves one agent session as a stdio MCP server until the agent disconnects. Agent CLIs start it; you don't
run it by hand. See [Connect your agents](./connect-agents.md).

| Flag | Meaning |
|---|---|
| `--agent` | name of the agent this gate serves, such as `claude` or `codex`. Required: 1 to 32 lower-case letters, digits, dashes or underscores. |
| `--project` | project directory. Default: the git root of the working directory. |
| `--config` | config file |
| `--db` | database file |

## derbent gate

```bash
derbent gate --agent <name> [--cli <protocol>] [--server <name>] [--project <dir>] [--config <path>] [--db <path>]
```

The pre-tool hook for built-in tools. The CLI runs it once per tool call, with the call on standard input.
See [Built-in tools](./built-in-tools.md).

| Flag | Meaning |
|---|---|
| `--agent` | name of the agent this hook serves, as in its rules. Required. |
| `--cli` | hook protocol: `claude`, `codex`, `copilot` or `antigravity`. Default: the agent name. |
| `--server` | the name of Derbent's MCP server entry in the CLI, whose tools the hook skips. Default: `derbent`. |
| `--project` | project directory. Default: the git root of the directory the CLI reports. |
| `--config` | config file |
| `--db` | database file |

Once it knows the protocol, every failure is answered as a `deny` with the reason. An unknown `--cli`, bad
flags, or an answer it cannot write exit with status 2, which Claude Code and Codex treat as a block.

## derbent pending

```bash
derbent pending [--json] [--db <path>]
```

Lists the calls waiting for you, oldest first, with their whole arguments. Read-only.

| Flag | Meaning |
|---|---|
| `--json` | print JSON lines instead of rows. Fields: `id`, `agent`, `session`, `tool`, `args`, `created`, `deadline`. |
| `--db` | database file |

## derbent approve, derbent deny

```bash
derbent approve [--session] [--db <path>] <id>
derbent deny [--db <path>] <id>
```

Decides one waiting call, by the id the UI shows, written `12` or `#12`. Flags go before the id. In a shell
that reads `#` as a comment, quote it: `'#12'`.

| Flag | Meaning |
|---|---|
| `--session` | (`approve` only) approve this tool for the rest of the agent's session |
| `--db` | database file. A path that does not exist is refused instead of created. |

## derbent receipts

```bash
derbent receipts [--agent <name>] [--tool <glob>] [--project <dir>] [--since <time>] [--limit <n>] [--json] [--db <path>]
```

Lists receipts, newest first. Read-only. See [Receipts](./receipts.md#list-receipts).

| Flag | Meaning |
|---|---|
| `--agent` | only this agent |
| `--tool` | only tools matching this glob, such as `github__*` |
| `--project` | only this project |
| `--since` | only receipts from this long ago (`1h`) or this time (RFC 3339) on |
| `--limit` | at most this many, the newest. Default: 50. |
| `--json` | print JSON lines instead of a table |
| `--db` | database file |

## derbent verify

```bash
derbent verify [--db <path>]
```

Opens the database read-only, walks the receipt chain and prints the number of receipts, the head hash and
whether the chain is intact. It never creates or migrates the database. See
[Receipts](./receipts.md#verify-the-chain).

## derbent config check

```bash
derbent config check [--config <path>]
```

Loads the config, starts every server once and lists the tools each would give the agents. See
[Configuration](./configuration.md#check-your-config).

## derbent version

```bash
derbent version
```

Prints the version. A build from source prints `dev`.
