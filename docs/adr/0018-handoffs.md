# 0018. Handoffs are Derbent tools with an address and three states

Date: 2026-09-29
Status: accepted

## Context

Agents that work on one project already share memory, and one could leave a task for another as a note
with a convention in its title. A note has no addressee, no owner and no end: two agents could both act
on it, and nobody could tell whether it was done.

## Decision

- Four tools every gate serves itself, as it serves the memory tools, decided by the rules like any other
  tool: `handoff_create`, `handoff_list`, `handoff_take` and `handoff_done`. The pre-tool hook leaves them
  to the MCP gate.
- A handoff is addressed to one agent label or to `*`, any agent. The address must have the form a label
  has, the one `--agent` takes, so a handoff never waits for an agent that cannot exist.
- A handoff is open, then taken by one agent it is addressed to, then done when that agent finishes it,
  with an optional note. Nothing moves it back: there is no reassignment and no release in v1. A take and
  a finish each read and write in one transaction, so of two agents taking a handoff at once, one gets it.
- A handoff belongs to a project and records the agents and gate sessions that created and took it.
  `handoff_list` shows the current project's unless asked for every project, and an agent can take a
  handoff of another project by its id, as it can read a note of another project.
- What the tools return is marked as tasks written by agents, information and not instructions, as memory
  results are. `derbent handoffs` lists them for the user.

## Consequences

- An agent sees what waits for it and claims it, and the user sees who took what in `derbent handoffs`
  and in each step's receipt.
- A handoff whose agent stops after taking it stays taken. With no release, the user sees it with
  `derbent handoffs --all`, and the task has to be created again.
- Addressing is by label, and a label is not authentication: any agent started as `reviewer` can take the
  reviewer's handoffs. A rule can put `handoff_take` behind `ask` for agents that should not.
- A handoff is text one agent writes for another to act on, which makes it a path for instructions planted
  by one agent to reach the next. The notice says so, and a rule can put `handoff_create` behind `ask`.
- The finish is bound to the agent label that took the handoff, not to its gate session, so another
  session of the same agent can finish it.
