# 0011. A session grant covers only the calls the same rule asks about

Date: 2026-09-27
Status: accepted

## Context

`A` approves a call and lets later calls through without asking for the rest of that agent's session.
Until now a grant was keyed on agent, session and tool. For MCP tools that is narrow enough, since an
MCP server names each action. Built-in tools are coarse: every shell command in Claude Code is the one
tool `native__Bash`. With a rule that asks about `git push*` and another that asks about
`terraform apply*`, one `A` on a push also let through a later `terraform apply`, which the user never
saw. The hook answered `allow`, so the CLI's own permission prompt was skipped too.

## Decision

A grant is keyed on agent, session, tool and the rule that sent the call to the user. The rule is named
by a fingerprint of its content, not by its position: `rule.Set.Key` is the SHA-256 of a canonical form
of the rule's agent pattern, tool pattern, `args` conditions sorted by name, and action. Each approval
stores the fingerprint of the rule that asked, and `A` writes the grant with it. A later call is let
through by a grant only when the same rule asks about it. An approval with no fingerprint grants nothing.

Migration 0003 adds `rule_key` to `approvals` and rebuilds `grants` on
`(agent, session, tool, rule_key)`.

## Consequences

- `A` on a `git push` covers later pushes in that session, and a `terraform apply` held by another rule
  still asks. `internal/gate` tests this on the MCP path and the hook path.
- Editing or reordering the config never widens a grant: a rule moved to another position keeps its
  fingerprint and its grants, and a call that a different rule now asks about is asked again.
- A rule whose pattern, arguments or action is edited gets a new fingerprint, so the calls it asks about
  ask again, even within a session that had a grant under the old rule.
- The grants written before the upgrade are dropped by migration 0003, since they do not say which rule
  asked. A grant lasts one agent session, so the cost is one more question per rule in the sessions
  running during the upgrade.
- The UI's `A` question and `derbent approve --session` name the scope: the tool, the rule's number and
  the agent.
