---
title: Introduction
sidebar_position: 1
slug: /
---

# Introduction

Derbent is a gate between coding agents and their tools. Claude Code, Codex, GitHub Copilot CLI and
Antigravity CLI connect to it as one MCP server, and your other MCP servers sit behind it. Every tool call
passes through the gate, which:

- gives the agents one shared memory, kept per project;
- decides with your rules which agent may call which tool;
- holds the calls you choose until you approve them;
- writes a hash-chained receipt for each call.

Shell commands and file edits don't go through MCP, but each of the four CLIs can run a command before a
tool call. Set that command to `derbent gate`, and built-in tools get the same rules, approvals and
receipts.

It runs locally on Windows, macOS and Linux as a single binary.

## How it fits together

```text
 Claude Code ─┐   stdio    ┌───────────────────────────┐   stdio / HTTP
 Codex ───────┤ ─────────> │ derbent mcp --agent X     │ ──────────────> your MCP servers
 Copilot CLI ─┤  one gate  │   rules, memory, receipts │
 Antigravity ─┘  process   └─────────────┬─────────────┘
                 per agent               │
                 session                 v
                              derbent.db (SQLite)
                                         ^
                                         │
                              derbent (terminal UI): approvals, receipts, memory
```

Each agent CLI starts `derbent mcp --agent <name>` as an ordinary stdio MCP server. That process reads
your config, starts your MCP servers, lists their tools together with the memory tools, applies your
rules to each call, forwards the calls that may run and appends a receipt. Run `derbent` with no
arguments in another terminal to open the UI, which reads and writes the same database.

There is no daemon. Nothing has to run before an agent starts, and the database file is the only state
the gate processes and the UI share. Each agent session starts its own copies of your MCP servers, as it
would without Derbent.

Two names come up everywhere in these docs:

- **Agent**: the `--agent` name you give Derbent in each CLI's config. Your rules match it. It is a label
  you choose, not a proof of identity.
- **Project**: the root of the git repository the gate was started in, or that directory itself when it
  is not in a repository. A linked worktree belongs to the repository it was added from; a submodule is a
  project of its own. Memory and receipts are filed by project.

## What Derbent is not

- **Not a sandbox.** An agent that can run shell commands as you can also run `derbent approve` or write
  the database. See [Security and limits](./security.md).
- **Not a model proxy.** It sits between agents and their tools, not between agents and model providers.
- **Not a shared service.** One person, one machine, no network listener.
- **Not all-seeing.** It only sees the calls that pass through it: MCP calls to the gate, and built-in
  tool calls a CLI shows its pre-tool hook.

Next: [install Derbent](./installation.md).
