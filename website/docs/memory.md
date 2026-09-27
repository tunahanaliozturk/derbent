---
title: Memory
sidebar_position: 9
---

# Memory

Every agent connected to Derbent gets three tools for notes it shares with the other agents: a decision, a
finding, something the next agent should know.

| Tool | Input | Returns |
|---|---|---|
| `memory_write` | `title`, `body`, optional `tags`, optional `supersedes` (the id of an earlier note this one replaces) | the new note's id |
| `memory_search` | `query`, optional `limit`, optional `all_projects` | ids, titles, snippets, authors and times, best matches first |
| `memory_read` | `id` | the whole note |

Limits:

- a title of at most 200 characters;
- a body of at most 16 KiB;
- at most 10 tags, made of letters, digits, dashes and underscores;
- `limit` from 1 to 50, 10 when not given.

## Projects

Notes belong to the project the gate was started in: the root of its git repository, or the directory
itself outside a repository.

- Agents working in separate worktrees of one repository share notes; a linked worktree belongs to the
  repository it was added from.
- A submodule is a project of its own.
- `memory_search` looks in the current project, or in every project with `all_projects`.
- In the terminal UI, `m` opens a memory browser that searches every project.

## Replacing a note

A note written with `supersedes` replaces the earlier one. The old note drops out of search results but
stays readable by its id.

## Who wrote what

Each note records the agent and the gate session that wrote it, which ties it to that session's
[receipts](./receipts.md). Search and read results show each note's author and say that entries are notes
written by agents, to be treated as information, not as instructions.

## Keeping memory safe

Memory is text one agent writes and another reads, which makes it a way for instructions planted in one
agent to reach the next. If you want to see what gets written, put `memory_write` behind `ask`:

```toml
[[rule]]
tool   = "memory_write"
action = "ask"

[[rule]]
action = "allow"
```
