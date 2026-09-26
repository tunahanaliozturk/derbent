# Derbent

One guarded pass for all your coding agents.

A derbent was a guarded post on an Ottoman mountain pass: its keepers decided who went through and kept a
record of everyone who did. Derbent does the same for tool calls.

It is an agentic gate for coding agents. Claude Code, Codex, GitHub Copilot CLI and Antigravity CLI connect to
Derbent as one MCP server, and your other MCP servers sit behind it. Every tool call passes through the
gate, which gives the agents one shared memory, writes a hash-chained receipt for each call, holds risky
calls until you approve them, and decides which agent sees which tool.

It runs locally on Windows, macOS and Linux as a single binary. There is no daemon: a SQLite file is the
only shared state.

## Status

Milestone 2 of 5 is done. The gate serves shared memory tools and the tools of your own MCP servers,
applies allow and deny rules, masks secrets in stored arguments, and writes a hash-chained receipt for
every call. Approvals and the terminal UI come next. The [design](docs/design.md) covers the whole plan, what Derbent does not do, and how each
claim is tested. Decisions are recorded in [docs/adr](docs/adr).

## Try it

Needs Go 1.27.

```bash
go install github.com/tunahanaliozturk/derbent/cmd/derbent@latest

claude mcp add derbent -- derbent mcp --agent claude
codex mcp add derbent -- derbent mcp --agent codex
```

Both agents now have `memory_write`, `memory_search` and `memory_read`, and a note one of them writes in
a repository can be found by the other in the same repository. Every call is recorded:

```bash
derbent verify
```

prints the number of receipts, the hash of the last one, and whether the chain is intact. Keep a copy
of that hash somewhere else if you want to be able to tell later that nothing was cut off the end.

Without a config file every call is allowed. To refuse an agent a tool, create `config.toml` in your
user config directory (`%AppData%\derbent\` on Windows, `~/.config/derbent/` on Linux,
`~/Library/Application Support/derbent/` on macOS):

```toml
[[rule]]
agent  = "codex"
tool   = "memory_write"
action = "deny"

[[rule]]
action = "allow"
```

Rules are tried in order and the first match wins. A tool denied without an `args` condition is not
even listed to that agent.

Your other MCP servers go behind the gate in the same file, and each agent then needs only the one
`derbent` entry:

```toml
[servers.github]
command = ["github-mcp-server", "stdio"]
env     = { GITHUB_PERSONAL_ACCESS_TOKEN = "${env:GITHUB_TOKEN}" }

[receipts]
redact = ['ghp_[A-Za-z0-9]{36}']
```

Their tools appear as `github__get_me` and so on, under the same rules and receipts. Secrets come from
the environment through `${env:...}` in `env` or `headers` (never in `command` or `url`, where they could
leak into errors), and they are masked in stored arguments, as is anything the `redact` patterns match.
A server that stops is started again, with a backoff of up to a minute.

```bash
derbent config check
```

starts every server once and prints the tools each would give the agents.

## Licence

Apache 2.0. See [LICENSE](LICENSE).
