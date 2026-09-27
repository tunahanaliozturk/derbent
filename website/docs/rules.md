---
title: Rules
sidebar_position: 6
---

# Rules

Rules decide what happens to each call: `allow` lets it run, `deny` refuses it, and `ask` holds it until
you decide (see [Approvals](./approvals.md)).

```toml
[[rule]]
agent  = "codex"
tool   = "memory_write"
action = "deny"

[[rule]]
action = "allow"
```

- Rules are tried in order, and the first one that matches decides.
- The last rule must have no condition. It decides every call the others don't.
- Without a config file, every call is allowed.

## Conditions

A rule matches a call when all of its conditions match. A condition you leave out matches anything.

| Condition | Matches |
|---|---|
| `agent` | the agent name, as a glob |
| `tool` | the tool name, as a glob |
| `args` | named top-level arguments, each as a glob over a string value |

Globs are simple: `*` matches any run of characters, newlines included, and `?` matches exactly one.
Every other character matches itself.

Agent names are lower case, so an `agent` pattern with upper-case letters is refused: it could never match.

### Arguments

```toml
[[rule]]
tool   = "native__Bash"
args   = { command = "git push*" }
action = "ask"
```

- A call without the named argument does not match.
- A value that is not a string (a number, a list, an object) cannot be read by a glob. It matches a `deny`
  or an `ask` rule, and never an `allow` rule, so a condition never lets through what it cannot check.
- Globs match text, not meaning. `git push*` does not match `cd repo && git push`. On shell tools, an
  `args` rule is a convenience, not a boundary.

## Tool names

| Tools | Named |
|---|---|
| Derbent's memory tools | `memory_write`, `memory_search`, `memory_read` |
| Tools of your MCP servers | `<server>__<tool>`, such as `github__create_issue` |
| Built-in tools of a CLI, through its hook | `native__<the CLI's name for it>`, such as `native__Bash` |
| MCP tools configured in Claude Code or Codex directly | `native__mcp__<server>__<tool>` |

The names of built-in tools differ per CLI. See [Built-in tools](./built-in-tools.md#tool-names).

## Hidden tools

A tool that no call can get through is not listed to the agent at all. That is the case when, among the
rules matching that agent and tool, a `deny` without an `args` condition comes before any `allow` or
`ask`.

- A `deny` with an `args` condition refuses only the matching calls, so the tool stays listed.
- A tool behind `ask` stays listed.
- Calling a hidden tool by name anyway is refused by the rule that hid it.
- Built-in tools can't be hidden from the model. A `deny` refuses each call instead.

## Calls refused before any rule

- A call to a name the gate does not serve to that agent is refused with `<tool> is not a tool this gate
  serves`.
- A call whose arguments are not a JSON object is refused: `args` conditions read named arguments and
  could not see into anything else.

## Examples

Keep one agent away from a server:

```toml
[[rule]]
agent  = "copilot"
tool   = "github__*"
action = "deny"
```

Ask before anything that creates something on GitHub:

```toml
[[rule]]
tool   = "github__create_*"
action = "ask"
```

Allow the memory tools, and ask for everything else:

```toml
[[rule]]
tool   = "memory_*"
action = "allow"

[[rule]]
action = "ask"
```

An agent whose call is refused gets a tool error such as `github__create_issue is not allowed for this
agent (rule 2)`.
