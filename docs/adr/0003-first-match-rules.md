# 0003. Rules are first-match, and a plain deny hides the tool

Date: 2026-09-26
Status: accepted

## Context

Every call through the gate needs one decision. Policy engines that combine all matching rules
(most specific wins, deny overrides) are powerful and hard to read: to know what happens to a call you
have to find every rule that touches it. The people writing these rules are developers editing a TOML
file, not a security team.

Agents also decide what to try from the tool list they are given. A tool that is listed but always
refused wastes the agent's turns and invites it to look for another way to do the same thing.

## Decision

Rules are tried in the order they are written and the first match decides. The last rule must have no
condition, so the file always states what happens to everything else. `*` and `?` are the only
wildcards; everything else in a pattern is literal, and `*` spans newlines so a multi-line argument
cannot slip past a pattern.

A `deny` rule without an `args` condition removes the tool from that agent's tool list. A rule with an
`args` condition keeps the tool listed, because calls with other arguments may still be allowed.

## Consequences

The rule that decided a call is one number, and every receipt records it. Reordering the file changes
the policy, which the README says plainly. A call to a hidden tool by name still reaches the rules and
is refused with a receipt; hiding is a convenience for the agent, not the protection.
