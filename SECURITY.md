# Security policy

## Supported versions

| Version | Supported |
|---|---|
| 1.x | Yes |
| Earlier builds | No |

Fixes land in the latest 1.x release.

## Reporting a vulnerability

Report it privately through GitHub:
[open a private vulnerability report](https://github.com/tunahanaliozturk/derbent/security/advisories/new)
on this repository. Please do not open a public issue, pull request or discussion for it.

Say what you found, how to reproduce it (a config, the input an agent or a CLI sends, and the commands
you ran), which version and operating system you used, and what an attacker gains.

## What to expect

- An acknowledgement within 7 days.
- For a confirmed issue in 1.x, a fix or a decision within 30 days of the report. If the fix takes longer,
  you hear why and when to expect it.
- A GitHub security advisory for the fix, with credit to you if you want it.

## Scope

In scope, Derbent's own code and the promises it makes:

- The gate's decisions: a call that runs although a rule denies it or it was never approved, a tool that
  is listed although it should be hidden, a session grant that covers a call it should not, or a call to
  Derbent's own tools that the hook treats wrongly.
- Receipts: a call through the gate that leaves no receipt, or a change to the chain that `derbent verify`
  does not report, within the limits stated in the design.
- Redaction: a secret from `${env:...}`, or a value a `redact` pattern matches, that reaches the database,
  the UI or the output of `derbent receipts` or `derbent pending` unmasked.
- The hook: a call the hook lets through when it should fail closed, or input from a CLI that makes it
  answer wrongly.
- The UI and the commands that print stored text: text from an agent or a tool that reaches the terminal
  unescaped and moves the cursor, writes to the clipboard, hides text or changes what else is shown.

Out of scope:

- Anything done by your own user account or by a program running as it, including an agent that already
  has a shell. Such a program can write the database, edit the config or run `derbent approve`, so
  Derbent is not a boundary against it. The [design's Security section](docs/design.md#security) explains
  why.
- Rewriting the whole receipt chain, or deleting the newest receipts, which `derbent verify` catches only
  against a copy of the head hash kept elsewhere, as documented.
- Tool calls that never pass through the gate, such as a CLI's hosted tools, and the limits listed under
  [Known limits and risks](docs/design.md#known-limits-and-risks).
- Vulnerabilities in the agent CLIs, the MCP servers behind the gate, or Go and the modules Derbent uses,
  unless Derbent's use of them makes the problem possible. Those belong with their own projects.
