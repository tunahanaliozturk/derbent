# 0012. Budgets count receipts, every matching budget applies, and a used-up budget refuses

Date: 2026-09-27
Status: accepted

## Context

An agent stuck in a loop can run the same tool hundreds of times: a shell command that keeps failing, an
issue it keeps filing. Rules decide each call on its own and cannot see that. Under an `ask` rule, each of
those calls also lands in the user's approval queue.

## Decision

A `[[budget]]` table limits how many calls one agent label may have let through to the tools its `tool`
glob matches, within a sliding window `per` of one minute to 24 hours.

- Every budget whose `agent` and `tool` globs match the call applies. Budgets are limits that add up, not
  alternatives, so there is no first match: a call under a wide budget can still be over a narrow one.
- The count reads the receipts. A call counts when its receipt says `decision` = `allow`, whatever let it
  through, and its time is inside the window. Receipts are already shared by every gate process and hook
  that uses the same database, so a budget holds across them with no new state. Migration 0004 indexes
  receipts on `(agent, at)` so the count reads only that agent's window.
- The rules decide first, and a `deny` stays a deny. Otherwise a used-up budget refuses the call without
  asking: `decided_by` = `budget:<n>`, outcome `refused`, and the agent reads which budget, the limit and
  about how long until the next call is possible.
- A budget that cannot be counted refuses the call, with `decided_by` = `gate`.

## Consequences

- A looping agent never floods the approval queue: once its budget is used up, its calls stop there.
- Counting and appending are not one transaction across processes. Two calls counted at the same moment
  can both pass when one call is left, so a budget can be passed by at most the number of calls in flight
  at once. An exact count would need a lock held from the count to the append, across processes, on every
  call; a budget is a brake for loops, not an accounting system.
- A counter table was the other choice. It would need resetting per window and could drift from what the
  receipts say happened, and the receipts are the record anyway.
- Calls the user approved count too. A user who approves the same call many times in an hour is told by
  the budget to look at why.
- A budget only refuses. Asking the user instead would bring back the flood it exists to stop.
