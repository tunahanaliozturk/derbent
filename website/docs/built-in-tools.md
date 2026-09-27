---
title: Built-in tools
sidebar_position: 8
---

# Built-in tools

An agent's shell commands and file edits don't go through MCP, but each of the four CLIs can run a command
of your choice before a tool call. Set that command to `derbent gate`, and it:

- decides the call as the tool `native__<the CLI's tool name>`, under the same [rules](./rules.md) and
  [approvals](./approvals.md);
- writes a receipt with outcome `gated` (the call may run) or `refused`;
- answers in the CLI's format: nothing for a call a rule allows, so the CLI's own permission settings still
  apply; `allow` for a call you approved; `deny` with the reason for anything refused.

## Install the hook

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

## Hook timeouts

Claude Code documents that a hook it times out on decides nothing, and the call goes on through its own
permission flow. The other CLIs don't say, and Derbent assumes they behave the same. So the hook's timeout
must stay above `[approvals] timeout`. Claude Code and Codex give a hook 600 seconds by default; Copilot CLI
and Antigravity CLI give it 30, which is why their examples set 120.

## Flags

- `--agent` is the name your rules match. Give it the same value as the CLI's `derbent mcp` entry.
- `--cli` picks the hook protocol: `claude`, `codex`, `copilot` or `antigravity`. It defaults to the
  `--agent` value, so you need it only for another agent name.
- `--server` is the name of Derbent's MCP entry in the CLI, `derbent` by default. If yours differs, pass it,
  or each call to Derbent's own tools is decided and recorded twice.
- `--project`, `--config` and `--db` work as for `derbent mcp`; see the [CLI reference](./cli.md#derbent-gate).

The project is `--project`, else the git root of the directory the CLI reports, else that of the hook's
working directory.

## Tool names

Rules name a built-in tool by the CLI's own name for it, so they differ per CLI, and so do the arguments:

| CLI | Shell | File edits |
|---|---|---|
| Claude Code | `native__Bash` | each of its tools, as `native__` plus the tool's name |
| Codex | `native__Bash` | `native__apply_patch` |
| Copilot CLI | `native__bash`, `native__powershell` | `native__create`, `native__edit`, `native__apply_patch` |
| Antigravity CLI | `native__run_command` | `native__write_to_file`, `native__replace_file_content` |

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

These rules go above the one that allows everything else.

Derbent cannot hide a CLI's built-in tools from the model, so a `deny` on one refuses each call instead.
In Claude Code and Codex, which send MCP calls to the hook too, a tool of an MCP server configured in the
CLI directly is decided the same way, as `native__mcp__<server>__<tool>`.

## Derbent's own tools

Calls to Derbent's own MCP tools get no decision and no receipt from the hook: the gate already decides and
records them. The hook recognises them by the name each CLI gives them:

| CLI | Derbent's tools appear as |
|---|---|
| Claude Code, Codex | `mcp__derbent__*` |
| Copilot CLI | `derbent-*` |
| Antigravity CLI | `mcp_derbent_*` (assumed; not yet seen in a real session) |

Where a CLI does not name the MCP server in the hook's input, another MCP entry whose tool names come out
as Derbent's prefix followed by a name Derbent serves, such as an entry called `derbent__github` in Codex,
is taken for Derbent's own. Its calls then get no decision and no receipt from the hook. Don't give other
MCP entries names that start with `derbent`.

## Secrets in hook receipts

The hook reads the same config file but starts no servers. A `${env:NAME}` that isn't set where the hook
runs is left as written, so server secrets can stay in the CLI's MCP entry for Derbent. Receipts from the
hook then mask only the secrets set in the hook's own environment. Add a `[receipts] redact` pattern for
the others.

## The hook fails closed

When the hook cannot read the call, load the config, open the database or write the receipt, it denies the
call and says why. A mistake in the config stops every built-in tool until you fix it; run
`derbent config check` to find it.

## What each CLI's hook covers

| CLI | Built-in tools gated | Not seen by the gate | Checked in a real session |
|---|---|---|---|
| Claude Code | every tool, through PreToolUse | nothing known | yes |
| Codex | shell commands, `apply_patch` for every file edit, and other local function tools such as `update_plan` | hosted tools such as web search | not yet |
| Copilot CLI | shell (`bash`, `powershell`), file tools (`view`, `create`, `edit`, `apply_patch`), `grep`, `glob`, `web_fetch`, `web_search` and its other documented tools | possibly MCP calls: the documentation doesn't say whether the hook sees them | not yet |
| Antigravity CLI | built-in tools such as `run_command`, `view_file`, `write_to_file` and `replace_file_content` | not documented | not yet |

Coverage for the CLIs not yet checked follows each CLI's documentation.
