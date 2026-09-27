---
title: Receipts
sidebar_position: 10
---

# Receipts

Every call through the gate, and every built-in tool call the hook decides, appends one receipt to the
database. Receipts form a hash chain: each one carries the hash of the one before it, so a receipt edited,
removed, inserted or moved later breaks the chain.

## What a receipt holds

- sequence number and time
- project, agent and gate session
- tool name
- arguments, with secrets masked, and the SHA-256 of the arguments before masking
- the decision (`allow` or `deny`) and what made it
- the outcome
- the size and SHA-256 of the result, not the result itself
- the duration
- the previous receipt's hash, and this receipt's own hash

What made the decision:

| Value | Meaning |
|---|---|
| `rule:<n>` | the rule at position n in your config |
| `gate` | the gate itself, such as a call to a tool it does not serve |
| `user:<id>` | your decision on approval id |
| `grant:<id>` | an earlier session approval (`A`) |
| `timeout:<id>` | no decision came before the timeout |
| `withdrawn:<id>` | the agent gave up, or the gate was stopped, while the call waited |

The outcome is `ok`, `error` or `refused` for a call through the gate, and `gated` or `refused` for a call
the hook decided, since the hook runs before the tool does.

## Secrets

Before arguments are stored, Derbent masks as `[redacted]`:

- every value at least eight characters long that came from `${env:...}` in a server's `env` or `headers`;
- whatever your `[receipts] redact` patterns match.

```toml
[receipts]
redact = ['(?i)bearer\s+\S+', 'ghp_[A-Za-z0-9]{36}']
```

Patterns use Go's regular expression syntax. Masking works on each string value, so the stored arguments
stay valid JSON. The approval queue and the UI only ever hold arguments after masking.

Receipts written by the hook mask only the secrets set in the hook's own environment. See
[Built-in tools](./built-in-tools.md#secrets-in-hook-receipts).

## Verify the chain

```bash
derbent verify
```

```text
receipts: <number of receipts>
head:     <hash of the newest receipt>
chain:    intact
```

`verify` opens the database read-only and walks the chain. When something is wrong, it ends with an error
that names the first bad receipt: `the receipt chain is broken at receipt <n>: <reason>`.

The chain alone cannot show two things: receipts cut off the end, or a chain rewritten as a whole by
someone who can write the database. Both leave a chain that verifies. To catch them, keep a copy of the head
hash and its receipt number somewhere else. Later the chain may have grown, but the receipt at that number
must still carry the same hash.

## List receipts

```bash
derbent receipts --agent codex --since 1h
derbent receipts --tool 'github__*' --limit 200
derbent receipts --json
```

| Flag | Keeps |
|---|---|
| `--agent` | only this agent |
| `--tool` | only tools matching this glob |
| `--project` | only this project |
| `--since` | only receipts from this long ago (`1h`) or this time (RFC 3339) on |
| `--limit` | at most this many, the newest; 50 by default |
| `--json` | prints JSON lines instead of a table |

The table shows `SEQ`, `TIME`, `AGENT`, `TOOL`, `DECISION`, `BY`, `OUTCOME` and `MS`. A JSON line has the
fields `seq`, `at`, `project`, `agent`, `session`, `tool`, `args`, `decision`, `decided_by`, `outcome`,
`result_size`, `result_sha256`, `duration_ms` and `hash`.

Stored text can come from an agent, so control characters, bidirectional overrides and invisible characters
are escaped in both forms.

## When a receipt cannot be written

If a tool ran but its receipt could not be written, the agent gets an error instead of the result:
`<tool> ran (outcome <outcome>) but could not be recorded; do not repeat it without checking its effect`.
The same line goes to stderr.
