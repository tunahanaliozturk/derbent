# Portcullis

An agentic gate for coding agents. Claude Code, Codex, GitHub Copilot CLI and Antigravity CLI connect to
Portcullis as one MCP server, and your other MCP servers sit behind it. Every tool call passes through the
gate, which gives the agents one shared memory, writes a hash-chained receipt for each call, holds risky
calls until you approve them, and decides which agent sees which tool.

It runs locally on Windows, macOS and Linux as a single binary. There is no daemon: a SQLite file is the
only shared state.

## Status

Milestone 1 of 5 is done. The gate serves shared memory tools, applies allow and deny rules, and
writes a hash-chained receipt for every call. Downstream MCP servers, approvals and the terminal UI come
next. The [design](docs/design.md) covers the whole plan, what Portcullis does not do, and how each
claim is tested. Decisions are recorded in [docs/adr](docs/adr).

## Try it

Needs Go 1.27.

```bash
go install github.com/tunahanaliozturk/portcullis/cmd/portcullis@latest

claude mcp add portcullis -- portcullis mcp --agent claude
codex mcp add portcullis -- portcullis mcp --agent codex
```

Both agents now have `memory_write`, `memory_search` and `memory_read`, and a note one of them writes in
a repository can be found by the other in the same repository. Every call is recorded:

```bash
portcullis verify
```

prints the number of receipts, the hash of the last one, and whether the chain is intact. Keep a copy
of that hash somewhere else if you want to be able to tell later that nothing was cut off the end.

Without a config file every call is allowed. To refuse an agent a tool, create `config.toml` in your
user config directory (`%AppData%\portcullis\` on Windows, `~/.config/portcullis/` on Linux):

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

## Licence

Apache 2.0. See [LICENSE](LICENSE).
