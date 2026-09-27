# 0006. Built-in tools are gated through each CLI's pre-tool hook

Date: 2026-09-27
Status: accepted

## Context

An agent's shell commands and file edits do not pass through MCP, so the gate cannot see them from its
MCP server. Each of the four CLIs can run a command before a tool call and act on its answer: Claude Code
and Codex through PreToolUse, with the same JSON contract, GitHub Copilot CLI through preToolUse, and
Antigravity CLI through PreToolUse. Their inputs, answers, timeouts and names for MCP tools differ.

## Decision

`derbent gate --agent <name> [--cli <protocol>]` is installed as the pre-tool hook of each CLI, and one
process runs per tool call. It reads the call, names it `native__<tool>` with the CLI's own tool name,
decides it with the same rules, session grants and approvals as the MCP gate through the same code,
appends a receipt with outcome `gated` or `refused`, and answers in that CLI's format. A call a rule
allows gets no decision, so the CLI's own permissions still apply on top; a call the user approved gets
`allow`; anything refused gets `deny` with the reason.

Calls to Derbent's own MCP tools get no decision and no receipt, because the MCP gate already decides and
records them. A call counts as Derbent's own only when its name starts with the CLI's prefix for the MCP
entry named by `--server` (default `derbent`), the rest of the name is a tool Derbent serves (a memory
tool, or `<server>__<tool>` for a server in the config), and, for Claude Code 2.1.274 and later, which
name the MCP server in the hook input, that server is the `--server` entry. Any other call, including one
to an MCP server configured in the CLI directly, is decided as a built-in tool.

The gate session of a hook call is the CLI's session id (Claude Code, Codex, Copilot CLI) or conversation
id (Antigravity CLI).

The hook reads the same config but starts no servers, so a `${env:NAME}` in a server's `env` or `headers`
that is not set in the hook's environment stays as written instead of failing the load. Server secrets can
then stay in the CLI's MCP entry for Derbent.

The hook fails closed. Once the protocol is known, any error answers `deny` with the reason; an unknown
protocol, bad flags, or an answer that cannot be written exit with status 2.

## Consequences

- A hook the CLI times out on decides nothing. Claude Code documents that the call then goes through its
  normal permission flow; the other CLIs do not document it, and Derbent assumes the same. Claude Code and
  Codex give a hook 600 seconds by default. Copilot CLI and Antigravity CLI give it 30 seconds, below
  the 50-second approval timeout, so their hook entries must set a longer timeout; the README's examples
  use 120 seconds.
- Rules for built-in tools are per CLI, since the CLIs name the same kind of tool differently
  (`native__Bash`, `native__bash`, `native__run_command`) and pass different argument names (`command`,
  `CommandLine`).
- Derbent cannot hide a CLI's built-in tools, so a `deny` on one refuses each call instead.
- A session grant never covers both paths. `A` on a hook tool covers later calls of that `native__` tool
  in the same CLI session; the MCP gate's session is one `derbent mcp` process with a random id, and its
  tools have other names.
- Hook receipts mask only the secrets set in the hook's own environment, and whatever the `redact`
  patterns match. A secret that only the CLI's MCP entry holds needs a `redact` pattern to be masked in
  them.
- For CLIs that do not name the MCP server in the hook input, a foreign MCP entry named so that its tool
  names start with Derbent's prefix and `<configured server>__`, such as `derbent__github` in Codex, looks
  like Derbent's own, and its calls get no decision and no receipt from the hook.
- Every tool call starts a short process that opens the database, and the first one creates it. Opening a
  current database takes no write lock, so a hook call does not queue behind other gates' appends.
- Hosted tools that never reach the hook, such as Codex's web search, are outside the gate, and the README
  says so per CLI.
