# 0009. Downstream servers are supervised from the moment the gate starts

Date: 2026-09-26
Status: accepted

## Context

A downstream server can fail to start, crash in the middle of a session, or add and drop tools while
running. An agent asks for the tool list once at the start of its session and then only when told the
list changed, so a server whose tools are unknown at that moment is invisible until something changes.

## Decision

Each gate process runs one supervisor goroutine per configured server. It connects, loads the tool
list, hands it to the gate, and waits for the session to end; then it waits and connects again. The
wait starts at one second, doubles on every failure up to a minute, and starts over after a session that
lasted at least thirty seconds. One attempt to start and initialise a server may take a minute, so a cold
start such as a first `npx` download still comes up. The agent's initialize is answered at once, and its
tool listings and calls wait up to five seconds from the start of the session for servers still on their
first attempt: a working server is in the first tool list, and a slow one cannot use up Codex's ten-second
startup timeout; it appears later through list_changed. A server's tools stay listed
while it is down, and calls to them are tool errors that say so. A list_changed notification from a
server reloads its list, and the SDK passes the change on to the agent. A command server inherits the
gate's environment plus its configured `env`, and its stderr goes to the gate's stderr, which the agent
CLI shows or logs; nothing a server prints can reach the agent's stdout.

## Consequences

A server that keeps crashing is restarted at most once a minute rather than on every call, and is
restarted even when nobody is calling it. Every agent session runs its own copy of every server, which
is the price of having no daemon (ADR 0001). Tools whose names do not fit the 64-character limit, or whose
input schema is not an object, are left out with a warning; `derbent config check` shows them. The
session of a server that has gone is closed explicitly, because the SDK keeps a goroutine for its
notification subscription until then; the leak test found this. Closing the gate closes every session
first, so a command server can exit on its own when its stdin closes, and only then cancels what is still
running. A command server still running two seconds after its stdin closed is stopped, with SIGTERM and
then a kill (a kill at once on Windows), instead of after the SDK's default of five seconds: an agent CLI
waits about five seconds for the gate to exit, and a gate waiting that long for one server was still
running when a client gave up on it in CI. A url server that answers with a redirect is refused, because
the configured headers would follow it to another host, even over plain http.
