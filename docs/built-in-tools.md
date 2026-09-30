# Built-in tools

An agent's shell commands and file edits do not go through MCP, but each of the four CLIs can run a
command of your choice before a tool call. With `derbent gate` as that command (see
[Install and set up](install.md)), each call is decided as tool `native__<the CLI's tool name>` under the same
rules and approvals, gets a receipt with outcome `gated` or `refused`, and is answered in the CLI's
format: nothing for a call a rule allows, so the CLI's own permission settings still apply, `allow` for a
call you approved (unless only a project rule asked about it, see [Project rules](rules.md#project-rules)), and
`deny` with the reason for anything refused ([ADR 0006](adr/0006-pre-tool-hooks.md)).

Rules name a built-in tool by the CLI's own name for it, so they differ per CLI: `native__Bash` (Claude
Code, Codex), `native__apply_patch` (Codex's file edits), `native__bash` and `native__powershell`
(Copilot CLI) and `native__run_command` (Antigravity CLI). The arguments differ too:

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

`A` on a built-in tool covers the rest of the CLI's session: its session id in Claude Code, Codex and
Copilot CLI, its conversation id in Antigravity CLI. Every shell command is one tool, such as
`native__Bash`, so an `A` on a `git push` does not let through a `terraform apply` that another rule asks
about. For MCP tools the session is one `derbent mcp` process. A grant on one path never covers the other,
since the tools have different names.

The hook fails closed: when it cannot read the call, load the config, open the database or write the
receipt, it denies the call and says why, so a mistake in the config stops every built-in tool until it
is fixed. Claude Code documents that a hook it times out on decides nothing, and the call goes on through
its own permission flow. The other CLIs do not say, and Derbent assumes they behave the same, so the
hook's timeout must stay above `[approvals] timeout`. Claude Code and Codex give a hook 600 seconds by
default; Copilot CLI and Antigravity CLI give it 30, which is why their examples set 120.

The hook protocol follows the agent name; for any other name add `--cli claude`, `codex`, `copilot` or
`antigravity`. If Derbent's MCP entry in the CLI has another name than `derbent`, pass it with `--server`,
or each call to Derbent's own tools is decided and recorded twice. Two kinds of entry cannot be matched
with `--server`, so their calls to Derbent's own tools are decided and recorded twice: a Derbent bundled in
a Claude Code plugin, whose tools Claude Code names `mcp__plugin_<plugin>_<server>__`, and an entry whose
name Claude Code rewrites because it holds characters other than letters, digits, `_` and `-`. Where the
CLI does not name the MCP server in the hook's input, the reverse can happen: another MCP entry whose tool
names come out as Derbent's prefix followed by a name Derbent serves, such as an entry called
`derbent__github` in Codex, is taken for Derbent's own, and its calls get no decision and no receipt from
the hook (see [Known limits](design.md#known-limits-and-risks)).

The hook reads the same config file but starts no servers, so a `${env:NAME}` that is not set where the
hook runs is left alone, and server secrets can stay in the CLI's MCP entry for Derbent. Receipts from the
hook then mask only the secrets set in the hook's own environment; add a `[receipts] redact` pattern for
the others.

What each CLI's hook covers, from its documentation and, where marked, a real session:

| CLI | Built-in tools gated | Derbent's own tools appear as | Not seen by the gate | Checked in a real session |
|---|---|---|---|---|
| Claude Code | every tool, through PreToolUse | `mcp__derbent__*` | nothing known | Claude Code 2.1.283, on 2026-09-27, with `claude -p --settings`: `echo derbent-allow` ran with a receipt `native__Bash allow rule:2 gated`; `echo derbent-deny` was blocked with Derbent's reason and a receipt `deny rule:1 refused`; `memory_write` through the derbent MCP server, which Claude Code names `mcp__derbent__memory_write`, got one receipt, from the MCP gate, because the hook skipped it; other built-in tools such as ToolSearch reach the hook too, as `native__ToolSearch`. |
| Codex | shell commands (`Bash`), `apply_patch` for every file edit, and other local function tools such as `update_plan` | `mcp__derbent__*` | hosted tools such as web search | Not checked: left out by the owner's choice. |
| Copilot CLI | shell (`bash`, `powershell`), file tools (`view`, `create`, `edit`, `apply_patch`), `grep`, `glob`, `web_fetch`, `web_search` and its other documented tools | `derbent-*`, with names capped at 64 characters | possibly MCP calls: the documentation does not say whether the hook sees them | Not checked: Copilot CLI 1.0.88 stopped at "You have exceeded your monthly quota" before any tool call. |
| Antigravity CLI | built-in tools such as `run_command`, `view_file`, `write_to_file` and `replace_file_content` | `mcp_derbent_*`, an assumption until checked | not documented | Not checked: not installed. |
