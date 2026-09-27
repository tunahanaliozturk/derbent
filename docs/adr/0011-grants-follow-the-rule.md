# 0011. A session grant covers only the calls the same rule asks about under the same rules above it

Date: 2026-09-27
Status: accepted

## Context

`A` approves a call and lets later calls through without asking for the rest of that agent's session.
Until now a grant was keyed on agent, session and tool. For MCP tools that is narrow enough, since an
MCP server names each action. Built-in tools are coarse: every shell command in Claude Code is the one
tool `native__Bash`. With a rule that asks about `git push*` and another that asks about
`terraform apply*`, one `A` on a push also let through a later `terraform apply`, which the user never
saw. The hook answered `allow`, so the CLI's own permission prompt was skipped too.

Keying the grant on the rule alone is not enough either. Rules are tried in order and the first match
wins (ADR 0003), so which calls rule n catches depends on rules 1 to n. A first version fingerprinted
rule n by itself. With `ask git push*--force*` above `ask git push*`, `A` on a plain push granted the
second rule; after the two rules were swapped, a force push reached the plain rule first and the grant
let it through.

## Decision

A grant is keyed on agent, session, tool and the rule that sent the call to the user. The rule is named
by a fingerprint of itself and every rule above it: `rule.Set.Key(n)` is the SHA-256 of the canonical
forms of rules 1 to n, in order. A rule's canonical form is its agent pattern, tool pattern and action,
then its `args` conditions sorted by name, each string written as its length, a colon and its bytes.
Each form goes into the hash the same way, as its length, a colon and its bytes, so two different rule
lists never give the same bytes. Each approval stores the fingerprint of the rule that asked, and `A`
writes the grant with it. A later call is let through by a grant only when a rule with the same
fingerprint asks about it. An approval with no fingerprint grants nothing. A call that its rule matched
on an argument the rule could not read (ADR 0003) gets no fingerprint, so no grant covers it and
approving it writes none: nobody has seen what such a call does, so each one asks.

Migration 0003 adds `rule_key` to `approvals` and rebuilds `grants` on
`(agent, session, tool, rule_key)`.

## Consequences

- `A` on a `git push` covers later pushes in that session, and a `terraform apply` held by another rule
  still asks. `internal/gate` tests this on the hook path and on the MCP path, where the rules are on
  `memory_write`, since the MCP gate refuses `native__` names.
- Editing or reordering the config never widens a grant. A change to the granted rule or to any rule
  above it gives a new fingerprint. On the hook path, which reads the config on every call, the calls
  that rule asks about then ask again while the change stands, even within a session that had a grant;
  grants are kept and looked up by fingerprint, so undoing the change makes the grant apply again. A
  change below it keeps the grant, since it cannot change which calls reach the rule. A `derbent mcp`
  gate reads the config only when it starts, so an edit does not affect its grants, and its next
  process is a new session with none. `internal/gate` tests on the hook path that a grant holds when the
  rules below it are reordered or edited, and asks again when the granted rule is edited, when a rule
  above it is narrowed, and when the two push rules described under Context swap places.
- The cost is extra questions on the hook path: any edit above a granted rule asks again while it
  stands, even one that cannot change what the rule catches, and a rule added at the top asks again for
  every grant. Telling a harmless edit from a widening one would mean comparing glob patterns, which is
  not worth saving one question per grant.
- The grants written before the upgrade are dropped by migration 0003, since they do not say which rule
  asked. A grant lasts one agent session, so the cost is one more question per rule in the sessions
  running during the upgrade.
- A gate from before the upgrade that is still running writes approvals with no fingerprint, as does a
  call matched on an argument its rule could not read. The UI's `A` question says it approves such a
  call once, and does. `derbent approve --session` has no question to confirm, so it refuses such an
  approval and says to approve it once. `internal/gate` tests on both paths that after `A` on a string
  `git push`, a `command` sent as an array still asks, and `A` on it writes no grant.
- The UI's `A` question, the UI's status after the approval and `derbent approve --session` name the
  scope: the tool, the rule's number and the agent.
