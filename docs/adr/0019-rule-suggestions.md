# 0019. Rules are suggested from the user's answers, never written

Date: 2026-09-29
Status: accepted

## Context

After approving the same agent's calls to the same tool again and again, a user wants a rule that stops the
asking. Every answer is in the approvals table, so Derbent could write that rule itself. But the config is
the user's, often edited by hand, and its order is the policy. And one approval too many could turn into a
rule that lets far more through than the user ever looked at, a shell most of all.

## Decision

- `derbent suggest` prints TOML snippets with the counts behind them, and the UI says when a tool the user
  just approved has reached the count. Derbent never edits the config.
- Answers are grouped by agent and tool. At least N approvals and no denial suggest an `allow`; at least N
  denials and no approval a `deny`. N is 5, and `--min` changes it.
- A tool whose calls carry a `command`, or Antigravity CLI's `CommandLine`, is a shell. Its snippet matches
  the longest start its commands share, cut before any `*` or `?` and back to where a word ends, followed by
  `*`. With nothing but white space left, or a call whose command is missing or not a string, there is no
  suggestion: Derbent never suggests allowing a whole shell.
- A snippet says where to put it: above the rule that asked, from the approvals, so first match reaches it.
  Calls a project's rules asked about are left out, since a rule in the user's config cannot loosen them.
- Agent labels, tool names and prefixes come from agents and the database, so every string in a snippet is
  a TOML basic string with quotes, backslashes, control characters and invisible or reordering characters
  escaped. It reads back as it was and cannot end early, add a line or start a table.

## Consequences

- The user reads every rule before it takes effect, and places it: a rule appended at the end would never
  be reached under first match.
- A prefix pattern matches text: `git *` also matches `git status && rm -rf build`, as any `args` rule on a
  shell tool does. A snippet is a starting point to narrow, not a proof that the calls are safe.
- Five is a guess at "the same thing again": fewer would suggest rules from chance, more would keep the
  user approving the same call for longer. `--min` lets a user choose.
- The group is the agent and the tool, so one denial of any command stops an `allow` for the whole tool,
  and a user who approves many unrelated commands of one shell gets no suggestion when they share no start.
