---
title: Approvals
sidebar_position: 7
---

# Approvals

A rule with `action = "ask"` holds a call until you approve or deny it. Put it above the rule that allows
everything else. The tool stays listed to the agent.

```toml
[[rule]]
tool   = "github__create_*"
action = "ask"

[[rule]]
action = "allow"

[approvals]
timeout = "50s"
```

`timeout` is optional: 50 seconds unless you set it, and at least one second. A call nobody answers in time
is denied, and the agent is told that no approval came in time and to try again while you watch the UI.

## The terminal UI

Run `derbent` in a terminal of its own.

```bash
derbent
```

Calls waiting for you sit at the top, each as `#12` with the agent, the tool, the time left and the start
of its arguments, masked as they are in receipts. The terminal bell rings when a new one arrives.

| Key | What it does |
|---|---|
| `a` | approve the highlighted call once |
| `A`, twice within five seconds | approve this tool for the rest of that agent's session |
| `d` | deny the highlighted call |
| up, down | pick another call |
| `enter` | show the highlighted call's whole arguments; scroll with up, down, page up, page down, home and end |
| `/` | filter the receipt feed by agent or tool name |
| `m` | search memory across projects |
| `v` | verify the receipt chain |
| `?` | list the keys |
| `q` | quit |

- A newly highlighted call takes no `a`, `A` or `d` for its first 750 ms on the main screen, so a key meant
  for the call before it cannot land on it.
- Below the waiting calls are the agents seen in the last hour and a live feed of receipts.
- Text from agents and tools is escaped before it is drawn: control characters, bidirectional overrides and
  invisible characters are written as `\uXXXX`, and a run of more than eight spaces in arguments is shown
  as `␠×N`. A value cannot hide text or draw lines of its own.
- Quitting the UI changes nothing for running agents. Their calls that need an approval wait for the
  timeout and are denied.
- Nothing you do in the UI changes your config file.

## From any shell

The same decisions work from any shell, by the id the UI shows, written `12` or `#12`:

```bash
derbent pending                # the waiting calls with their whole arguments; --json for JSON lines
derbent approve 12             # approves once, like a
derbent approve --session 12   # like A; flags go before the id
derbent deny '#12'             # quote the # in a shell that reads it as a comment
```

A call can be decided only while it is waiting. Otherwise you get `approval 12 is not pending: it was
already decided, timed out, or its agent gave up`.

## Session grants

`A` (or `approve --session`) approves the tool for the rest of that agent's session:

- for an MCP tool, the session is one `derbent mcp` process, which lives as long as the agent CLI session
  that started it;
- for a built-in tool, the session is the CLI's own session: its session id in Claude Code, Codex and
  Copilot CLI, its conversation id in Antigravity CLI.

A grant on one path never covers the other, since the tools have different names.

## Timeouts and agent CLIs

The default of 50 seconds sits below Codex's default tool timeout of 60 seconds, so the agent sees a clear
denial instead of a transport timeout. If you raise `timeout`, raise `tool_timeout_sec` for the `derbent`
server in Codex's config too. For built-in tools, keep each CLI's hook timeout above it as well (see
[Built-in tools](./built-in-tools.md#hook-timeouts)).

## What approvals do not protect against

Approvals guard against mistakes and against prompt injection that stays inside MCP. They are not a
boundary against an agent that can already run shell commands as you: it can run `derbent approve` itself
or write the database. See [Security and limits](./security.md).
