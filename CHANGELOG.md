# Changelog

Notable changes to Derbent, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0] - 2026-09-27

The first release.

### Added

- `derbent mcp --agent <name>`: one MCP server for Claude Code, Codex, GitHub Copilot CLI and Antigravity
  CLI, with the memory tools `memory_write`, `memory_search` and `memory_read`. Notes belong to a project
  (its git root), any agent can write them, any agent can find them with full-text search, and a note can
  supersede an older one.
- A hash-chained receipt for every call: which agent called which tool with which arguments (masked),
  what decided it, the outcome, and the size and hash of the result. `derbent verify` walks the chain,
  names the first receipt where it breaks, and prints the head hash to keep elsewhere. `derbent receipts`
  lists receipts by agent, tool, project and time, as a table or as JSON lines.
- Rules in `config.toml` that `allow`, `deny` or `ask` by agent, tool and argument, where the first match
  wins. A tool that no call can get through is not listed to that agent.
- Your other MCP servers behind the gate, over stdio or HTTP, with their tools named `<server>__<tool>`.
  A server that stops is started again with a backoff of up to a minute. `derbent config check` validates
  the config, starts each server once and prints the tools it would give the agents.
- Redaction: secrets that reach a server through `${env:...}`, and anything the `[receipts] redact`
  patterns match, are masked in stored arguments.
- Approvals: an `ask` rule holds the call until you decide, or denies it when `[approvals] timeout` (50
  seconds unless you set it) runs out. `derbent` opens a terminal UI with the waiting calls, the agents
  seen in the last hour, a live receipt feed, a memory browser and verify. `derbent approve [--session]
  <id>` and `derbent deny <id>` decide from any shell, and `derbent pending` lists the waiting calls with
  their whole arguments.
- The UI's detail view: `enter` shows a waiting call's whole arguments, escaped and scrollable, and `a`,
  `A` and `d` work there too.
- `A` approves the tool's calls for the rest of the agent's session and needs a second press within 5
  seconds. A newly highlighted call takes `a`, `A` or `d` only after 750 ms on screen, so a key meant for
  the call before it cannot land on it.
- Session grants follow the rule that asked and the rules above it
  ([ADR 0011](https://github.com/tunahanaliozturk/derbent/blob/v1.0.0/docs/adr/0011-grants-follow-the-rule.md)):
  an `A` on a `git push` does not let through a `terraform apply` that another rule asks about, and
  editing or reordering the rules never widens a grant.
- `derbent gate`: a pre-tool hook for Claude Code, Codex, Copilot CLI and Antigravity CLI, so built-in
  tools such as the shell and file edits pass the same rules, approvals and receipts as MCP tools, named
  `native__<tool>`.
- The hook loads the config without failing on an unset `${env:NAME}`, since it starts no servers and
  needs none of their secrets.
- Release binaries for Windows, Linux and macOS on amd64 and arm64, with `SHA256SUMS`. Each binary is
  built twice and published only when both builds are identical, and a clean checkout of the tag rebuilds
  the same bytes with Go 1.27.1 (see the README's Check a release).
- Benchmarks of what the gate adds to an MCP call and what a hook call costs, with results from GitHub's
  Linux and Windows runners in
  [docs/benchmark-results](https://github.com/tunahanaliozturk/derbent/blob/v1.0.0/docs/benchmark-results/README.md).

### Security

- The hook fails closed: when it cannot read the call, load the config, open the database or write the
  receipt, it denies the call and says why.
- A session grant never covers a call that its rule matched on an argument it could not read, such as a
  `command` sent as an array: each such call asks.
- A `url` server must use HTTPS, except on localhost, and redirects are refused, so its headers never
  reach another host.
- Secrets come from `${env:...}` only through a server's `env` or `headers`, never in `command` or `url`,
  where they could show up in process listings and error messages.
- The UI, `derbent receipts` and `derbent pending` escape control characters, bidirectional overrides and
  invisible characters in text from agents and tools before it reaches your terminal.

[1.0.0]: https://github.com/tunahanaliozturk/derbent/releases/tag/v1.0.0
