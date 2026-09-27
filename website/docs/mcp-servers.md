---
title: MCP servers
sidebar_position: 5
---

# MCP servers

Your MCP servers go behind the gate in `config.toml`. Each agent then needs only its one `derbent` entry,
and every call to those servers passes the same rules, approvals and receipts.

## A stdio server

```toml
[servers.github]
command = ["github-mcp-server", "stdio"]
env     = { GITHUB_PERSONAL_ACCESS_TOKEN = "${env:GITHUB_TOKEN}" }
```

The server gets the gate's environment plus the variables in `env`. What it writes to stderr goes to the
gate's stderr, which the agent CLI shows or logs.

## An HTTP server

```toml
[servers.docs]
url     = "https://docs.example.com/mcp"
headers = { Authorization = "Bearer ${env:DOCS_TOKEN}" }
```

- The url must use `https`, unless the server is on your machine: `localhost` or a loopback address such
  as `127.0.0.1` or `::1`.
- A url server may not redirect, because the configured headers would follow the redirect.

## Rules for the server entry

- A server name is 1 to 32 lower-case letters, digits and dashes. `memory` and `native` are reserved.
- Set exactly one of `command` and `url`.
- `env` belongs to command servers and `headers` to url servers.

## Secrets from the environment

`${env:NAME}` is replaced by the variable's value when the gate starts.

- Use it only in `env` and `headers`. It is refused in `command`, which shows up in process listings and
  start errors, and in `url`, which HTTP errors quote to stderr and to the agent.
- A variable that is not set stops the gate with `environment variable NAME is not set`. Codex hands MCP
  servers only a few variables; see [Connect your agents](./connect-agents.md#codex).
- Every value taken from the environment that is at least eight characters long is masked in stored
  arguments, as `[redacted]`. See [Receipts](./receipts.md).

## Tool names

A server's tools are offered to the agents as `<server>__<tool>`, such as `github__get_me`, so two servers
never collide. Your [rules](./rules.md) use these names.

A tool name must be 1 to 64 letters, digits, underscores or dashes. A tool whose name does not fit, or
whose input schema is not an object, is left out with a warning on stderr, never shortened.
`derbent config check` marks it.

Derbent forwards tools only. Resources and prompts from your servers are not passed on.

## Starting, stopping and restarting

- The gate starts your servers when the agent starts it. The agent gets its first answer at once; its tool
  list and its calls wait up to five seconds for servers still starting. A slower server's tools appear
  later, and the agent is told the list changed.
- A server that stops is started again. The wait starts at one second, doubles after every failure up to
  one minute, and starts over after a server has run for at least thirty seconds.
- While a server is down, its tools stay listed and calls to them return a tool error. Everything else
  keeps working.
- When a server changes its tool list, the gate applies your rules to the new list and tells the agent.
- Every agent session runs its own copy of each server. A server that keeps state in memory does not share
  it between agents.

The pre-tool hook (`derbent gate`) reads the same config but starts no servers. See
[Built-in tools](./built-in-tools.md).
