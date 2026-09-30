# Memory and handoffs

## Memory

Every agent now has `memory_write`, `memory_search` and `memory_read`, and a note one agent writes in a
repository can be found by the others in the same repository. Search is full-text, and results mark
each note with its author and as a note, not an instruction. `handoff_create`, `handoff_list`,
`handoff_take` and `handoff_done` let one agent leave a task for another (see [Handoffs](#handoffs)).

## Handoffs

One agent can leave a task for another. `handoff_create` addresses it to an agent label, such as
`reviewer`, or to `*` for any agent, with a title, a body of up to 16 KiB and tags. `handoff_list` shows
the open handoffs in the current project addressed to the calling agent or to `*`; `mine` shows the ones
it created instead, `state` picks `taken`, `done` or `all`, and `all_projects` looks in every project.
`handoff_take` gives one to the agent and returns it whole, and `handoff_done` marks it done with an
optional note of up to 4 KiB. Only one agent can take a handoff, and only the agent that took it can
finish it. There is no release or reassignment yet: a handoff whose agent stops after taking it stays
taken ([ADR 0018](adr/0018-handoffs.md)).

```bash
derbent handoffs          # the open handoffs of every project; --all for every state, --json for JSON lines
```

A handoff is text one agent writes for another to act on, so what the tools return is marked as tasks
written by agents, information and not instructions, and the rules decide the four tools like any other.
An agent label is not authentication, so any agent started as `reviewer` can take the reviewer's handoffs.
Ids count up from 1, and `handoff_take` takes a handoff of any project by its id, so an agent can try one
id after another, claim every open handoff addressed to `*`, and learn from the refusals whom the others
are for or who took them. For an agent you want to watch or do not trust, put `handoff_create` and
`handoff_take` behind `ask` and answer each call with `a`, once, not `A`: a session grant lets every later
take from that session through. The balanced and strict presets allow the four tools.
