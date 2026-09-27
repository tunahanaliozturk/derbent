---
title: Security and limits
sidebar_position: 12
---

# Security and limits

Derbent guards against mistakes, and against prompt injection that stays inside MCP. This page says what
that covers and what it does not.

## What Derbent does

- **No network listener.** Agents talk to the gate over stdio, and the gate talks to your servers over
  stdio or outbound HTTP. HTTP servers must use `https` unless they are on your machine.
- **Only your config sets rules.** Nothing inside a repository can change them.
- **Secrets are masked** in stored arguments: values from `${env:...}` in `env` or `headers`, and whatever
  your `redact` patterns match. Secrets can't be put in a server's `command` or `url`.
- **Hidden tools stay hidden.** A tool no call can get through is not listed to the agent, and calling it
  by name is refused.
- **Unreadable arguments fail closed.** An `args` condition that meets a value it cannot read matches
  `deny` and `ask`, never `allow`.
- **The hook fails closed.** When it cannot decide or record a built-in tool call, it denies the call.
- **Text is escaped.** The UI and `derbent receipts` escape control characters, bidirectional overrides
  and invisible characters, so a value cannot hide text or fake lines.

## What Derbent does not do

- **It is not a boundary against an agent with a shell.** An agent that can run shell commands as you can
  run `derbent approve` itself, or write the database. Approvals and `args` rules on shell tools don't hold
  it back.
- **Agent names are labels.** Any process running as you could start a gate under any name.
- **Receipts record what passed through the gate.** They are not protection against your own account or
  anything running as it, which can write the database. The chain shows changes in the middle, but a full
  rewrite, or cutting off the newest receipts, is caught only by comparing the head hash with a copy kept
  elsewhere. See [Receipts](./receipts.md#verify-the-chain).
- **Globs match text, not meaning.** `git push*` does not match `cd repo && git push`.
- **Approvals depend on you watching.** Unattended, `ask` means denied after the timeout.
- **Memory can carry instructions** from one agent to the next. Notes are marked with their author and as
  notes, not instructions, and you can put `memory_write` behind `ask`. See [Memory](./memory.md).

## What the gate does not see

- Tools a CLI never shows its hook, such as Codex's hosted web search.
- MCP servers configured in a CLI directly, when that CLI's hook does not report MCP calls.
- In a CLI whose hook input does not name the MCP server (Codex, Copilot CLI, Antigravity CLI), calls of
  another MCP entry named so that its tools look like Derbent's own, such as `derbent__github` in Codex.
  The hook skips them. Don't give other MCP entries names that start with `derbent`.
- When a CLI times out on its hook, the call goes on through the CLI's own permission flow. Keep each hook's
  timeout above `[approvals] timeout`.

## Other limits

- Only Claude Code's hook has been checked in a real session. The Codex, Copilot CLI and Antigravity CLI
  hooks follow each CLI's documentation.
- Every agent session starts its own copy of each server. A server that keeps state in memory does not share
  it between agents.
- Hook receipts mask only the secrets set in the hook's own environment. Add a `redact` pattern for secrets
  held only by a CLI's MCP entry.
