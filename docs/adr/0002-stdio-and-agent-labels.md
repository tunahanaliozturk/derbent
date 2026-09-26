# 0002. Agents connect over stdio, and the agent name is a label from each CLI's config

Date: 2026-09-26
Status: accepted

## Context

Claude Code, Codex, GitHub Copilot CLI and Antigravity CLI all start MCP servers as child processes
over stdio. Some also reach servers over HTTP, with different ways of passing headers. The gate needs
to know which agent a call comes from, for the rules and for the receipts.

## Decision

The gate speaks stdio only. Each CLI's MCP entry runs `derbent mcp --agent <name>`, and that name,
1 to 32 lower-case letters, digits, dashes or underscores, is the agent's identity for rules and
receipts.

## Consequences

Setting up a CLI is one line in its MCP config, the same for all four. The name is a label, not
authentication: any process running as the user could start a gate with any name. That is consistent
with the trust boundary of the whole tool, which is the user's account, and the security section of the
design says so. If Derbent ever serves more than one user, identity has to come from somewhere
else, and that is a new decision.
