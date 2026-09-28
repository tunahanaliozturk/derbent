# 0014. Project rules can only tighten the user's rules

Date: 2026-09-27
Status: accepted

## Context

A repository knows things about itself: which commands deploy, which paths hold secrets. Its maintainers
may want every agent working in it to ask before `terraform apply`, whatever each user's rules say. But a
repository is written by others, agents included. A rules file inside it that could allow calls would let
anyone who can open a pull request, or any agent that can write a file, turn off the user's rules.

## Decision

- A `.derbent.toml` at the root of the checkout the agent works in may hold `[[rule]]` tables and nothing
  else. Project rules use the rule syntax, first match wins, and they need no final catch-all: when none
  matches, the project adds nothing.
- The call's action is the stricter of the user's decision and the project's: deny, then ask, then allow.
  The rule that set it is named, `rule:<n>` or `project:<n>`, the user's on a tie.
- Project rules decide calls only. They never change tool listings: a tool the project denies stays listed,
  and its calls are refused.
- An invalid file denies every call in the project, on the hook and on the MCP gate, with a reason that
  names the file and the error. A missing file changes nothing.
- Only a regular file of at most 64 KiB is read. A symbolic link, anything else that is not a regular
  file, such as a directory, a FIFO or a device, and a larger file are refused as invalid, with a reason
  that says which. The open does not wait for a FIFO's writer, what was opened must be the file that was
  checked, and the read stops one byte past the limit, so a file replaced or grown after the check is
  refused too, a symbolic link to another file included. The `.git` file that marks a linked worktree,
  and the `commondir` file it leads to, are read under the same limits, and one that fails them is taken
  as not a linked worktree.
- The file is read for each call and kept by path, size and modification time. It is looked up under the
  checkout's root in its own case: the nearest directory holding `.git`, which for a linked worktree is
  the worktree's own root, or the project directory itself outside git.
- A grant for a call the project's rules asked about is keyed on both fingerprints, the user's rules up to
  the one that decided and the project's up to the one that asked (ADR 0011).
- The approval records which list asked (migration 0006), so the UI and the commands can say
  `project rule <n>`. The commands that only read never migrate, and on a database from before 0006 they
  take every approval as asked by the user's rules, which is what it was.

## Consequences

- A repository can make agents stricter, never looser: the worst a hostile `.derbent.toml` can do is deny
  calls or ask about them, and the user sees both. The limits on what is read keep that true: a FIFO or a
  link to a terminal would hold the hook until the CLI gave up on it and ran the call under its own
  permissions, and a link to `/dev/zero` would fill the hook's memory until it was killed.
- Taking the stricter action, instead of merging the two lists into one first-match list, keeps each list
  readable on its own and makes "only tighten" hold by construction: no order of rules can let through
  what the user's rules refuse.
- Listings follow only the user's rules, so a project cannot hide a tool from an agent. Hiding would mean
  reading the project file when the gate starts and whenever it changes, for no gain in safety.
- An agent that can edit the repository can edit or delete the file. That takes the project back to the
  user's rules, never below them.
- A linked worktree reads the file at its own root, not the main checkout's, though it shares the main
  checkout's project key, memory and receipts. Reading the main checkout's file would read nothing in a
  worktree of a bare repository, and would not enforce a branch that adds or tightens the file. Rules
  only tighten, so the worktree's own file can never loosen anything, and a grant is keyed on what the
  rules say, not on where the file is.
- Failing closed on an invalid file means a typo in a repository stops every agent working in it until it
  is fixed. A file that half applied would be worse: the user could not tell which rules held.
- The cache keyed on size and modification time misses an edit that keeps both, which only a file system
  with a coarse clock allows within one tick; the next change is seen.
