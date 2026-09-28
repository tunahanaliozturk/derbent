# 0015. Setup writes the CLIs' configs

Date: 2026-09-28
Status: accepted

## Context

Each CLI needs two entries, an MCP server and a pre-tool hook, in files that each CLI lays out its own
way, and several mistakes fail silently: a hook whose command is not found decides nothing in Claude
Code, so the call goes on under the CLI's own permissions, and a hook timeout at or below the approval
timeout lets a waiting call through. A command can write the entries, but a CLI's config belongs to the
user, is often edited by hand, and in Claude Code's case is rewritten by the CLI while it runs.

## Decision

- `derbent init` adds the MCP entry through the CLI's own `mcp add` command where the CLI has one
  (Claude Code at user scope, Codex, Copilot CLI), since the CLI owns that registry, and edits Antigravity
  CLI's `mcp_config.json`, for which there is no command. It adds the hook by editing the file each CLI
  reads user hooks from. Where a CLI documents a variable that moves its files, init follows it.
- It never replaces or removes an entry. An MCP entry named `derbent`, or a hook that runs
  `derbent gate` in any form, counts as set up and is left alone.
- It shows every change, asks once, and copies each file that exists before its first change. JSON is
  decoded and written back; Codex's TOML is appended to as text, never re-encoded, and only when the file
  and the result both parse. A file that does not parse is left as it is.
- The binary is written as the running binary's absolute path, never a bare `derbent`, in the form each
  field is read: exec form for Claude Code, `& "<path>"` in PowerShell, a double-quoted word elsewhere
  when the path needs one. A path that some shell would read even inside double quotes is refused.
- `derbent doctor` reads the same files, checks what init writes and the mistakes init cannot prevent,
  and writes nothing.

## Consequences

- A user who moves the binary runs init again; doctor names the path that is gone.
- A JSON file's keys may come back in another order, with two-space indentation; the copy keeps the
  original.
- init follows each CLI's file layout and `mcp add` syntax, which change between releases. They were
  checked against each CLI's documentation on 2026-09-28, and doctor names what no longer matches.
- Claude Code's exec-form hook needs Claude Code 2.1.139 or later.
