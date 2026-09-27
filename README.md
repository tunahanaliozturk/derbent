<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/derbent-lockup-on-dark.svg">
  <img alt="Derbent" src="docs/assets/derbent-lockup-on-light.svg" height="64">
</picture>

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

Milestone 3 of 5 is done. The gate serves shared memory tools and the tools of your own MCP servers,
applies allow, deny and ask rules, masks secrets in stored arguments, and writes a hash-chained receipt
for every call. It can hold a call until you approve it in a terminal UI or from another shell. The
milestone's exit check ran with a real Claude Code session whose held call was approved from another
process, not with Codex approved from the UI as first planned. Gating Claude Code's built-in tools
through its hooks comes next. The [design](docs/design.md) covers the whole plan, what Derbent
does not do, and how each claim is tested. Decisions are recorded in [docs/adr](docs/adr).

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

## Approvals

A rule can hold a call until you decide. It goes above the rule that allows everything else, and the
tool stays listed to the agent:

```toml
[[rule]]
tool   = "github__create_*"
action = "ask"

[[rule]]
action = "allow"

[approvals]
timeout = "50s"
```

The timeout is optional: 50 seconds unless you set it, and at least one second.

Run `derbent` in a terminal of its own. Calls waiting for you sit at the top, each as `#12` with the
agent, the tool, the time left and the start of its arguments, masked as they are in receipts, and the
terminal bell rings when a new one arrives. `enter` shows the highlighted call's whole arguments, and
up, down, page up, page down, home and end scroll them. The keys act on the highlighted call: `a`
approves it once, `A` pressed twice within five seconds approves the tool for the rest of that agent's
session, `d` denies it, and up and down pick another call. A call that has just been highlighted takes
none of these keys for its first 750 ms, so a key meant for the call before it cannot land on it. A call
nobody answers is denied after the timeout, and the agent is told why. Below the waiting calls are the
agents seen in the last hour and a live feed of receipts; `/` filters the feed, `m` searches memory
across projects, `v` verifies the receipt chain, `?` lists the keys and `q` quits. Text from agents and
tools is escaped before it is drawn, so it cannot send control sequences to your terminal or hide
behind invisible characters.

The same decisions work from any shell, by the id the UI shows, written `12` or `#12`, and the waiting
calls and the receipts can be listed without the UI:

```bash
derbent pending                # the waiting calls with their whole arguments; --json for JSON lines
derbent approve 12             # approves once, like a
derbent approve --session 12   # like A; flags go before the id
derbent deny '#12'             # quote the # in a shell that reads it as a comment
derbent receipts --agent codex --since 1h
derbent receipts --json        # JSON lines
```

Approvals guard against mistakes and against prompt injection that stays inside MCP. They are not a
boundary against an agent that can already run shell commands as you: it can run `derbent approve`
itself or write the database, so an approval or an `args` rule on a shell tool does not hold it back.

The timeout sits below Codex's default tool timeout of 60 seconds ([ADR 0005](docs/adr/0005-approval-timeout.md)).
If you raise it, raise `tool_timeout_sec` for the `derbent` server in Codex's config too.

## Licence

Apache 2.0. See [LICENSE](LICENSE).
