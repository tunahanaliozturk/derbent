# 0005. Approvals time out below the clients' tool timeouts, and a timeout denies

Date: 2026-09-27
Status: accepted

## Context

A call behind an `ask` rule waits for a person. Each agent CLI stops waiting for an MCP tool call after a
timeout of its own and shows the model a transport error. If the gate kept waiting past that point, the
user could approve a call whose agent had already moved on: the tool would run and nobody would receive
its result.

Default tool call timeouts, checked on 2026-09-27:

| CLI | Default | Setting | Source |
|---|---|---|---|
| Codex CLI 0.157.1 | 60 s | `tool_timeout_sec` under `[mcp_servers.<name>]` | developers.openai.com/codex/mcp |
| GitHub Copilot CLI 1.0.88 | 180 s, stated in the project's issue tracker; the documentation names the `timeout` field but no default | `timeout` in `~/.copilot/mcp-config.json` | github.com/github/copilot-cli/issues/1378 |
| Claude Code 2.1.283 | about 27.8 hours; a stdio call with no response or progress for 30 minutes is aborted | `MCP_TOOL_TIMEOUT` | code.claude.com/docs/en/mcp |
| Antigravity CLI | not documented | | antigravity.google/docs/mcp |

## Decision

The default approval timeout is 50 seconds, ten below the shortest documented default. When it passes,
the approval is marked expired and the agent gets a tool error saying that no approval came in time and
that it should try again and ask the user to approve it in the UI while it waits. `[approvals] timeout`
changes it, as a Go duration of at least one second; a value above an agent's own timeout gives that
agent a transport error instead of the clear denial.

A decision and the timeout can land in the same instant. The approval row leaves `pending` once, in an
update guarded by `state = 'pending'`, so whichever write SQLite serialises first is the outcome: a
decision that loses is refused as too late, and a timeout that loses reads back the decision and lets it
stand. A decision is also refused once the deadline has passed, so a row left behind by a gate that
stopped while waiting drops out of the UI and cannot be approved from then on.

## Consequences

- An `ask` nobody answers is a denial after 50 seconds, and the agent can tell the user why.
- A user who wants longer must raise the CLI's own timeout as well, such as Codex's `tool_timeout_sec`.
- The CLI's timeout covers the whole call, so the time a tool takes after a late approval comes out of
  the same margin. A Codex call approved at 49 seconds has about eleven seconds left to run.
- Antigravity's timeout is unknown. If it proves shorter than 50 seconds, its users see a transport
  timeout rather than a clear denial. It is still unchecked: Antigravity CLI was not installed when
  milestone 4 was checked in real sessions.
- A decision reaches the waiting call within one poll, 200 ms. The approval row is written before the
  wait starts and the receipt after the call, and under heavy write contention either write can wait up
  to the busy timeout (ADR 0008); the ten seconds of margin cover ordinary contention, not a database
  locked for half a minute.
