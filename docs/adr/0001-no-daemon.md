# 0001. No daemon: gate processes and the UI share state only through SQLite

Date: 2026-09-26
Status: accepted

## Context

Several agents run at once and must share memory, receipts and, later, approvals. The obvious shape is
a long-running hub that every agent connects to. That hub has to be started before any agent, kept
alive, restarted after a crash, and reached over a local socket or port that then needs its own
authentication.

## Decision

There is no hub process. Each agent CLI starts `portcullis mcp` as an ordinary stdio MCP server, and
every gate process opens the same SQLite database in WAL mode. Writes that must not interleave run in
`BEGIN IMMEDIATE`. The terminal UI in milestone 3 is one more process on the same file, and pending
approvals are noticed by polling.

## Consequences

Nothing needs to be running before an agent starts, and a crash takes down one agent's gate, not
everyone's. There is no network listener to secure. The costs: every agent session starts its own
copies of the downstream servers, as it would without Portcullis, approvals are seen within the polling
interval rather than at once, and everything depends on SQLite's file locking, which is why the database
lives in the local state directory and never in a synced folder.
