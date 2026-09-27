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

Milestone 4 of 5 is done. The gate serves shared memory tools and the tools of your own MCP servers,
applies allow, deny and ask rules, masks secrets in stored arguments, and writes a hash-chained receipt
for every call. It can hold a call until you approve it in a terminal UI or from another shell.
Milestone 3's exit check ran with a real Claude Code session whose held call was approved from another
process, not with Codex approved from the UI as first planned. Built-in tools such as the shell and
file edits now pass the same rules through each CLI's pre-tool hook: `derbent gate` speaks the hook
protocols of Claude Code, Codex, Copilot CLI and Antigravity CLI, and only Claude Code's has been
checked in a real session (see [Built-in tools](#built-in-tools)). The overhead benchmark, a demo
recording and the v1.0.0 release come next. The [design](docs/design.md) covers the whole plan, what
Derbent does not do, and how each claim is tested. Decisions are recorded in [docs/adr](docs/adr).

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

Codex starts MCP servers with only a few environment variables. If your config uses `${env:NAME}`, add
`env_vars = ["NAME"]` to the `[mcp_servers.derbent]` entry in Codex's config; without it the gate does
not start, and its error names the missing variable.

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
approves it once, `A` pressed twice within five seconds approves the tool's calls that the same rule
asks about, for the rest of that agent's session, `d` denies it, and up and down pick another call. A
call that has just been highlighted takes none of these keys for its first 750 ms on the main screen, so
a key meant for the call before it cannot land on it. A call nobody answers is denied after the timeout,
and the agent is told why. Below the waiting calls are the agents seen in the last hour and a live feed
of receipts; `/` filters the feed, `m` searches memory across projects, `v` verifies the receipt chain,
`?` lists the keys and `q` quits. Text from agents and tools is escaped before it is drawn: control
characters, bidirectional overrides, zero-width and other format characters, line and paragraph
separators, variation selectors, tag characters, the Hangul fillers, the combining grapheme joiner and
the braille blank are written as `\u` or `\U` codes, and in arguments a run of more than eight spaces of
any kind is shown as `␠×N`.

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

## Built-in tools

An agent's shell commands and file edits do not go through MCP, but each of the four CLIs can run a
command of your choice before a tool call. Set that command to `derbent gate`, and it decides the
call as tool `native__<the CLI's tool name>` under the same rules and approvals, writes a receipt with
outcome `gated` or `refused`, and answers in the CLI's format: nothing for a call a rule allows, so the
CLI's own permission settings still apply, `allow` for a call you approved, and `deny` with the reason
for anything refused ([ADR 0006](docs/adr/0006-pre-tool-hooks.md)).

Claude Code, in `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "*", "hooks": [ { "type": "command", "command": "derbent gate --agent claude" } ] }
    ]
  }
}
```

Codex, in `~/.codex/config.toml`. Codex asks you once to trust a new hook before it runs it:

```toml
[[hooks.PreToolUse]]
matcher = ".*"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "derbent gate --agent codex"
```

Copilot CLI, in `~/.copilot/hooks/derbent.json`:

```json
{
  "version": 1,
  "hooks": {
    "preToolUse": [
      { "type": "command", "bash": "derbent gate --agent copilot", "powershell": "derbent gate --agent copilot", "timeoutSec": 120 }
    ]
  }
}
```

Antigravity CLI, in `~/.gemini/config/hooks.json`:

```json
{
  "derbent": {
    "PreToolUse": [ { "matcher": ".*", "hooks": [ { "command": "derbent gate --agent antigravity", "timeout": 120 } ] } ]
  }
}
```

Claude Code documents that a hook it times out on decides nothing, and the call goes on through its own
permission flow. The other CLIs do not say, and Derbent assumes they behave the same, so the hook's
timeout must stay above `[approvals] timeout`. Claude Code and Codex give a hook 600 seconds by
default; Copilot CLI and Antigravity CLI give it 30, which is why their examples set 120.

`--agent` is the name your rules match, so give it the same value as the CLI's `derbent mcp` entry. The
hook protocol follows the agent name; for any other name add `--cli claude`, `codex`, `copilot` or
`antigravity`. If Derbent's MCP entry in the CLI has another name than `derbent`, pass it with
`--server`, or each call to Derbent's own tools is decided and recorded twice. Two kinds of entry cannot
be matched with `--server`, so their calls to Derbent's own tools are decided and recorded twice: a
Derbent bundled in a Claude Code plugin, whose tools Claude Code names
`mcp__plugin_<plugin>_<server>__`, and an entry whose name Claude Code rewrites because it holds
characters other than letters, digits, `_` and `-`. Where the CLI does not name the MCP server in the
hook's input, the reverse can happen: another MCP entry whose tool names come out as Derbent's prefix
followed by a name Derbent serves, such as an entry called `derbent__github` in Codex, is taken for
Derbent's own, and its calls get no decision and no receipt from the hook (see
[Known limits](docs/design.md#known-limits-and-risks)). The hook fails closed: when it cannot read the
call, load the config, open the database or write the receipt, it denies the call and says why, so a
mistake in the config stops every built-in tool until it is fixed.

Rules name a built-in tool by the CLI's own name for it, so they differ per CLI: `native__Bash` (Claude
Code, Codex), `native__apply_patch` (Codex's file edits), `native__bash` and `native__powershell`
(Copilot CLI) and `native__run_command` (Antigravity CLI). The arguments differ too, and these rules go
above the one that allows everything else:

```toml
[[rule]]
agent  = "claude"
tool   = "native__Bash"
args   = { command = "git push*" }
action = "ask"

[[rule]]
agent  = "antigravity"
tool   = "native__run_command"
args   = { CommandLine = "git push*" }
action = "ask"
```

Derbent cannot hide a CLI's built-in tools from the model, so a `deny` on one refuses each call instead.
In Claude Code and Codex, which send MCP calls to the hook too, a tool of an MCP server configured in the
CLI directly is decided the same way, as `native__mcp__<server>__<tool>`.

`A` on a built-in tool approves the tool's calls that the same rule asks about, for the rest of the CLI's
session: its session id in Claude Code, Codex and Copilot CLI, its conversation id in Antigravity CLI.
Every shell command is one tool, such as `native__Bash`, so an `A` on a `git push` does not let through
a `terraform apply` that another rule asks about, and editing or reordering the rules never widens a
grant ([ADR 0011](docs/adr/0011-grants-follow-the-rule.md)). For MCP tools the session is one
`derbent mcp` process. A grant on one path never covers the other, since the tools have different names.

The hook reads the same config file but starts no servers, so a `${env:NAME}` that is not set where the
hook runs is left alone, and server secrets can stay in the CLI's MCP entry for Derbent. Receipts from
the hook then mask only the secrets set in the hook's own environment; add a `[receipts] redact`
pattern for the others.

What each CLI's hook covers, from its documentation and, where marked, a real session:

| CLI | Built-in tools gated | Derbent's own tools appear as | Not seen by the gate | Checked in a real session |
|---|---|---|---|---|
| Claude Code | every tool, through PreToolUse | `mcp__derbent__*` | nothing known | Claude Code 2.1.283, on 2026-09-27, with `claude -p --settings`: `echo derbent-allow` ran with a receipt `native__Bash allow rule:2 gated`; `echo derbent-deny` was blocked with Derbent's reason and a receipt `deny rule:1 refused`; `memory_write` through the derbent MCP server, which Claude Code names `mcp__derbent__memory_write`, got one receipt, from the MCP gate, because the hook skipped it; other built-in tools such as ToolSearch reach the hook too, as `native__ToolSearch`. |
| Codex | shell commands (`Bash`), `apply_patch` for every file edit, and other local function tools such as `update_plan` | `mcp__derbent__*` | hosted tools such as web search | Not checked: left out by the owner's choice. |
| Copilot CLI | shell (`bash`, `powershell`), file tools (`view`, `create`, `edit`, `apply_patch`), `grep`, `glob`, `web_fetch`, `web_search` and its other documented tools | `derbent-*`, with names capped at 64 characters | possibly MCP calls: the documentation does not say whether the hook sees them | Not checked: Copilot CLI 1.0.88 stopped at "You have exceeded your monthly quota" before any tool call. |
| Antigravity CLI | built-in tools such as `run_command`, `view_file`, `write_to_file` and `replace_file_content` | `mcp_derbent_*`, an assumption until checked | not documented | Not checked: not installed. |

## Licence

Apache 2.0. See [LICENSE](LICENSE).
