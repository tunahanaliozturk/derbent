---
title: Troubleshooting
sidebar_position: 13
---

# Troubleshooting

Messages below are quoted as Derbent prints them. Commands print their errors to stderr after `derbent:`.

## The gate does not start

**`unknown keys: ...`**

A key in `config.toml` is misspelled or in the wrong table. The message names it. See
[Configuration](./configuration.md#keys).

**`at least one rule is required`**

Once the config file exists, it needs rules. End it with `[[rule]]` and `action = "allow"` to keep
allowing everything else.

**`the last rule must have no agent, tool or args condition`**

Add a last rule with only an `action`.

**`agent "..." can never match: agent names are lower-case letters, digits, dashes and underscores`**

Write the agent name in lower case, as in `--agent`.

**`environment variable NAME is not set`**

A `${env:NAME}` in a server's `env` or `headers` has no value where the gate runs. In Codex, add
`env_vars = ["NAME"]` to Derbent's entry. See [Connect your agents](./connect-agents.md#codex).

**`url cannot use ${env:...}; send secrets in headers`** and **`command cannot use ${env:...}; pass secrets to the server in env`**

Move the secret to `headers` (url server) or `env` (command server).

**`url must use https unless the server is on this machine`**

Use an `https` url, or run the server locally.

**`--agent must be 1 to 32 lower-case letters, digits, dashes or underscores`**

Fix the `--agent` value in the CLI's MCP entry or hook.

**`schema version N is newer than this binary knows (M): upgrade derbent`**

The database was written by a newer Derbent. Install the newer version.

## A server's tools are missing

- Run `derbent config check`. A server shown as `not running` did not start; its errors are on stderr.
- A tool marked `left out` has a name longer than 64 characters or outside letters, digits, underscores and
dashes, or an input schema that is not an object.
- A tool your rules deny without an `args` condition is hidden from that agent. See
[Rules](./rules.md#hidden-tools).
- A server that is slow to start appears once it has started; the agent is told the list changed.

## Calls are refused

**`<tool> is not allowed for this agent (rule N)`**

Rule N denied it. Rules are tried in order, first match wins.

**`<tool> is not a tool this gate serves`**

The agent called a name that isn't listed to it, such as a hidden tool or a server that isn't configured.

**`<tool> needs the user's approval and none came within 50s, so it was denied`**

Nobody decided in time. Run `derbent` in another terminal and approve while the agent retries. To wait
longer, raise `[approvals] timeout`, and raise Codex's `tool_timeout_sec` and each hook's timeout with it.

## Approving from a shell

**`approval 12 is not pending: it was already decided, timed out, or its agent gave up`**

The call is no longer waiting. Run `derbent pending` to see what is.

**`flags go before the id`**

Write `derbent approve --session 12`, not `derbent approve 12 --session`.

**`give one approval id, as the UI shows it`** after `derbent deny #12`

Your shell read `#12` as a comment, so no id arrived. Quote it, `derbent deny '#12'`, or drop the `#`.

## Built-in tools

**Every built-in tool is denied**

The hook fails closed: when the config does not load or the database cannot be opened, it denies every
call and says why. Run `derbent config check` and fix what it reports.

**Calls to Derbent's own tools are recorded twice**

Derbent's MCP entry in the CLI has another name than `derbent`. Pass that name with `--server`.

**A call went through without waiting for approval**

The CLI timed out on the hook and went on through its own permission flow. Keep the hook's timeout above
`[approvals] timeout`. See [Built-in tools](./built-in-tools.md#hook-timeouts).

## Receipts

**`the receipt chain is broken at receipt N: ...`**

Receipt N, or the one before it, was changed, removed or inserted after it was written.

**`... ran (outcome ...) but could not be recorded; do not repeat it without checking its effect`**

The tool ran, but its receipt could not be written, for example because the disk is full. Check the
tool's effect before running it again.

**Errors about a locked or unreadable database**

Keep the database out of synced folders such as OneDrive, Dropbox or iCloud Drive. The default location
is outside them.
