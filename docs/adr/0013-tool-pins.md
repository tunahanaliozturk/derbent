# 0013. Downstream tools are pinned on first use, and a changed tool is withheld

Date: 2026-09-27
Status: accepted

## Context

A tool's description is text the agent reads and follows. A downstream server can change it at any start,
through an update, a compromised package or a hostile server, for example to tell the agent to read a
secret and pass it to another tool. Rules match tool names, not descriptions, so the gate would serve the
new text under the same name and the same rules.

## Decision

- A pin is the SHA-256 of the tool's definition as the gate receives it from the MCP SDK: name, title,
  description, input schema, output schema and annotations, encoded as JSON with sorted object keys, no
  insignificant whitespace and every key present. Pins live in the database, keyed on server name and tool
  name (migration 0005).
- Trust on first use: the first time a gate sees a tool, it pins it and serves it.
- When a definition differs from its pin, the gate withholds the tool from the agent's list, records the
  new definition next to the pin, and writes a warning to stderr. A call to it is refused before any rule
  is read, with `decided_by` = `pin`. The user reviews the change with `derbent pins show` and accepts it
  with `derbent pins accept`, giving the hash `pins show` printed, and a running gate serves it again
  within two seconds.
- Pins are global per server and tool, not per agent or project.
- `pin = false` on a server turns pinning off, for servers whose descriptions change on every start.

## Consequences

- The gate withholds instead of asking. Asking the user about a changed tool would keep it listed, so the
  agent would read the changed description while the user decides, and the description is the attack.
- Trust on first use cannot tell whether the first definition was honest. A server that is hostile from
  its first listing is pinned as it is. `derbent config check` shows the tools and their pin states before
  an agent uses them.
- The same holds for a tool that an update adds to a server already pinned: it is new, so it is pinned
  and served at once, mid-session too through `list_changed`, and its description can carry the kind of
  instruction the Context describes. Pins only catch a change to a tool already seen. For a server whose
  updates are not reviewed, the rules are the guard: allowing its tools by name, such as
  `github__create_issue`, and denying `github__*` after them leaves a new tool out of every list, where an
  `allow` or `ask` on `github__*` serves it.
- Global pins mean one review covers every agent and project, and a tool the rules hide from one agent is
  still pinned for the next agent that can see it. The agent it is hidden from gets the refusal of the
  rule that hides it, changed or not, so a pin never tells an agent that a hidden tool exists.
- A tool that disappears keeps its pin, so it cannot come back changed without notice. This version never
  deletes a pin.
- The pin covers the definition as the SDK decodes it, so a server that sends the same schema with other
  key order or spacing has not changed it.
- `derbent pins accept` takes the whole hash of the change the user reviewed, all 64 hex digits as
  `pins show` prints them, and refuses when the change on record has another, so a change recorded after
  the review is never accepted unseen. A prefix is not enough: a hostile server controls every
  definition of its tool, so it could find two whose hashes share their first 8 hex digits in about 2^16
  tries, show one for review and send the other.
- The watcher of a gate waiting on a changed tool only reads its pin, and records its own definition as
  the change only when none is recorded, so it never replaces the recorded change. A gate that lists the
  server's tools again, at its start, on a reconnect or after `list_changed`, does record its definition
  as the change, and two gates holding different definitions, such as `npx pkg@latest` resolved at
  different starts, can then replace each other's. An accept of the hash the user reviewed is refused
  after that, so what is accepted is always what was read.
- The commands that only read the database never migrate it, so right after an upgrade they can meet a
  database without the pins table. It counts as holding no pins: every tool is `new`.
- Only downstream servers are pinned: the memory tools are Derbent's own, and the CLIs' built-in tools
  have no definition the gate receives.
- A pin check that fails withholds the server's tools until a check succeeds, so a database problem never
  serves an unchecked tool.
