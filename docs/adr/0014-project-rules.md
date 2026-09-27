# 0014. Project rules can only tighten the user's rules

Date: 2026-09-27
Status: accepted

## Context

A repository knows things about itself: which commands deploy, which paths hold secrets. Its maintainers
may want every agent working in it to ask before `terraform apply`, whatever each user's rules say. But a
repository is written by others, agents included. A rules file inside it that could allow calls would let
anyone who can open a pull request, or any agent that can write a file, turn off the user's rules.

## Decision

- A `.derbent.toml` at the project root may hold `[[rule]]` tables and nothing else. Project rules use the
  rule syntax, first match wins, and they need no final catch-all: when none matches, the project adds
  nothing.
- The call's action is the stricter of the user's decision and the project's: deny, then ask, then allow.
  The rule that set it is named, `rule:<n>` or `project:<n>`, the user's on a tie.
- Project rules decide calls only. They never change tool listings: a tool the project denies stays listed,
  and its calls are refused.
- An invalid file denies every call in the project, on the hook and on the MCP gate, with a reason that
  names the file and the error. A missing file changes nothing.
- The file is read for each call and kept by path, size and modification time. It is looked up under the
  project root in its own case.
- A grant for a call the project's rules asked about is keyed on both fingerprints, the user's rules up to
  the one that decided and the project's up to the one that asked (ADR 0011).
- The approval records which list asked (migration 0006), so the UI and the commands can say
  `project rule <n>`.

## Consequences

- A repository can make agents stricter, never looser: the worst a hostile `.derbent.toml` can do is deny
  calls or ask about them, and the user sees both.
- Taking the stricter action, instead of merging the two lists into one first-match list, keeps each list
  readable on its own and makes "only tighten" hold by construction: no order of rules can let through
  what the user's rules refuse.
- Listings follow only the user's rules, so a project cannot hide a tool from an agent. Hiding would mean
  reading the project file when the gate starts and whenever it changes, for no gain in safety.
- An agent that can edit the repository can edit or delete the file. That takes the project back to the
  user's rules, never below them.
- Failing closed on an invalid file means a typo in a repository stops every agent working in it until it
  is fixed. A file that half applied would be worse: the user could not tell which rules held.
- The cache keyed on size and modification time misses an edit that keeps both, which only a file system
  with a coarse clock allows within one tick; the next change is seen.
