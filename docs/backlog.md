# Derbent backlog

Date: 2026-09-28. Status: ideas, not decisions. An item that gets built moves into [design.md](design.md),
and a choice someone could reasonably make differently gets an ADR in the change that builds it. Milestone
7 and the Later list in design.md are not repeated here.

## Before a wider launch

### 1. Hook latency on managed Windows machines

On one Windows 11 Enterprise machine (10.0.26100) with Microsoft Defender Antivirus real-time protection
and Microsoft Defender for Endpoint running, every start of a new, unsigned binary of 2.6 MB or more took
over a second. Each row is 15 launches after 3 warm-up launches, timed from process start to exit, from
the user's temp directory. The binaries were built with Go 1.27.1 as
`go build -trimpath -ldflags="-s -w -buildid="` with CGO off, `derbent` from commit 5d66222:

| Binary | p50 | p90 |
|---|---|---|
| Empty `main`, 1.2 MB | 22.2 ms | 26.1 ms |
| Empty `main` with 1.3 MB of random bytes embedded, 2.6 MB | 1.393 s | 10.94 s |
| Empty `main` with 3.3 MB of random bytes embedded, 4.6 MB | 1.375 s | 10.81 s |
| `modernc.org/sqlite` and `BurntSushi/toml` only, opening a database and inserting a row, 6.4 MB | 1.860 s | 11.08 s |
| `derbent version`, 15 MB | 1.817 s | 11.01 s |

`GODEBUG=inittrace=1 derbent version` put all package initialisation at about 30 ms, 24 ms of it in
`BurntSushi/toml`. The same `derbent.exe` copied to `%LOCALAPPDATA%` took 959 ms at p50. So the cost is
the scanner, not Go: an empty Go program starts fast until it grows past a size between 1.2 and 2.6 MB.
The p90 near 11 seconds looks like Defender's cloud check running into its timeout, 10 seconds by
default; that is a guess and has not been checked.

The hook starts one process per built-in tool call, so on such a machine every shell command and file
edit can wait between 1.4 and 11 seconds. GitHub's Windows runner took 46.87 ms for the same start (README,
Overhead), so CI does not show this.

- Run the same measurement on a Windows machine with only the default Defender, to learn whether managed
  machines alone are affected.
- Sign the Windows release binaries with Authenticode, publish build provenance with
  `actions/attest-build-provenance`, and measure again. A signed binary no longer matches a rebuild byte
  for byte, so the release has to publish the checksums of the unsigned binaries as well, or the check
  in the README has to strip the signature first.
- Let `derbent doctor` (item 3) time one hook call on the user's own machine and say when it is slow.
- If signing does not help: a hook client under 1 MB that asks a running `derbent mcp` over a named pipe.
  It bends ADR 0001 and needs a security design of its own. It is also the one place where a language
  other than Go would earn its keep, since Go's smallest binary is already 1.2 MB.

### 2. A byte order mark on hook input

`derbent gate` denies input that starts with a UTF-8 byte order mark, with the reason "unusable hook
input: invalid character '﻿' looking for beginning of value". A .NET Framework program that writes
the hook input through `Process.StandardInput` sends one when the console input encoding is UTF-8, which
is how it was found. Whether any of the four CLIs sends one is not known. The hook fails closed, so this
is safe, but it would stop every built-in tool. Strip a leading `EF BB BF` before decoding the JSON.

### 3. `derbent init` and `derbent doctor`

Setup takes two entries per CLI in two files, and several mistakes fail silently or far from their cause.

- `derbent init` finds the installed CLIs and writes Derbent's MCP entry and hook into each one's config.
  It shows each change and asks before writing, keeps a copy of the file it changes, and never replaces
  an existing entry.
- `derbent doctor` reads each CLI's config and names what is wrong: a hook timeout at or below
  `[approvals] timeout`, an MCP entry with another name than `derbent` and no `--server`, a
  `${env:NAME}` that Codex's `env_vars` does not pass, an `--agent` that differs between the MCP entry
  and the hook, and a missing config file, under which every call is allowed.

## Rules that match meaning

### 4. Shell commands parsed, not globbed

`args = { command = "git push*" }` misses `cd repo && git push` (see Known limits in design.md). A shell
condition would parse the command with `mvdan.cc/sh` (pure Go, BSD-3-Clause, the parser behind `shfmt`)
into simple commands, including those inside pipelines, lists, subshells and `$(...)`, and match when any
of them matches. A command that does not parse, or that runs text the parser cannot see into, such as
`eval`, counts as an argument the rule could not read: it matches a `deny` or an `ask`, never an `allow`
(ADR 0003). The syntax is open. PowerShell has no parser in Go, so `native__powershell` stays on plain
globs.

### 5. Paths compared after they are resolved

A glob over the raw string misses `./../../.ssh/id_rsa`, or a path in another case on Windows. A path
condition would make the path absolute against the project, clean it, resolve symbolic links where the
file exists and fold case where the file system does, and then match globs such as `~/.ssh/**` or
`**/.env`. An `outside_project` condition would match any path that leaves the project root. A path that
cannot be resolved counts as an argument the rule could not read.

### 6. Secrets on the way out

A value from `${env:...}` or a match of a `[receipts] redact` pattern is masked in the receipt, but the
call still carries it to the tool. The same matchers could feed the decision: a condition such as
`secret = true` would match a call whose arguments hold one, so an `ask` or a `deny` stops an agent that
pastes a token into an issue body. It reuses `internal/redact`.

### 7. Taint for the rest of a session

An agent that has read untrusted text, such as a web page or an issue from a stranger, and can also send
data out is the dangerous combination. A condition such as `after = "native__WebFetch"` would match only
once the same session has a receipt for a matching tool, so pushing, posting or fetching asks from then
on. It reads receipts by agent, session and time the way budgets do (ADR 0012), so every gate process and
the hook share it. The MCP gate and the hook have different session ids (see Built-in tools in
design.md), so taint crosses from one path to the other only by agent and time.

## Response

### 8. A lockdown switch

`derbent lockdown`, and a key in the UI that takes a second press, would make every gate and hook refuse
every call with `decided_by` = `lockdown` until `derbent unlock`. It is one row in the database, read on
each call by both paths, which already read the grants table on each call.

### 9. Invisible characters in tool results

Hidden text in a tool result, such as Unicode tag characters (U+E0000 to U+E007F), is read by the model
and not by the user. On the MCP path the gate could remove tag characters from downstream results and
note in the receipt that it did, using the character classes in `internal/visible`. The result hash is
taken over what the agent received, so it stays checkable. Subdivision flags such as Scotland's are
spelled with tag characters and would show as a plain black flag. The hook runs before the tool, so the
results of built-in tools are out of reach.

## Rules and receipts

### 10. Replaying a new config

`derbent explain` (milestone 7) says why one call got its decision. A replay would apply a config the user
is editing to the receipts of the last days and list the calls whose decision would change. Stored
arguments are masked, so a rule on a masked value can decide differently in the replay, and the output
has to say so for each such call.

### 11. The head hash kept somewhere else

`derbent verify` catches a rewritten chain, or deleted newest receipts, only against a copy of the head
hash kept elsewhere (ADR 0004), and today the user keeps that copy. Derbent could write a checkpoint, the
sequence number and the head hash, every so many receipts to a place that is harder to rewrite quietly:
the Windows event log, syslog, or a git note in a repository the user pushes. `derbent verify` would then
compare against the latest checkpoint. None of these holds against the user's own account, the limit
receipts already state.

## Agents reviewing agents

### 12. A reviewer agent before the user

The user may want one model, such as Claude Opus in the session that plans the work, to look first at
what the other agents ask to do: Codex asks to push, the reviewer reads the plan and the call, and denies
it with a reason. No single CLI can do this across vendors, and the gate already holds every call in one
place.

- A rule opts in with `reviewer = "opus"` on an `ask` rule. The reviewer sees only the calls such rules
  ask about, never a call a `deny` decided, and never a call from its own agent label.
- Its power is lopsided on purpose. Denying is always safe, so the reviewer can deny any call it is given.
  It can approve once, never for the session, and it can pass the call to the user, who then decides as
  today. An agent label is not authentication (ADR 0002), and a model reading arguments an attacker may
  have written can be talked into things, so a rule whose calls must never go through without the user
  leaves `reviewer` out.
- There are two ways to reach the reviewer. The first is a command in the config, such as a headless
  `claude -p` with its tools turned off, started once per call with the call on standard input and the
  project's notes available through memory, answering allow, deny or pass. It needs no session to be
  waiting, and the same mechanism takes a plain script. The second is a pair of MCP tools for an agent
  named as reviewer, one that waits for the next held call and one that decides it. The orchestrating
  session then judges with its whole context, but only while it is waiting on that tool, not while it
  works. The command comes first.
- The reviewer's decision and reason go into the receipt, as `review:<agent>:<id>`, and to the agent whose
  call it was. When the reviewer has not answered within half of `[approvals] timeout`, the call goes to
  the user for the rest of it.
- The orchestrator should learn what happened to the calls of the agents it directs. A read-only tool
  that lists the project's recent decisions, escaped and marked as records rather than instructions, lets
  it see that Codex was denied and why, and plan again.

## Language

A rewrite in Rust was considered on 2026-09-28 and not taken. The one cost that looked like Go's, the
Windows hook start, is the scanner (item 1), and a Rust hook with SQLite, TOML and regular expressions
would likely still be larger than 1.2 MB, the largest size measured without the cost. On Linux the hook
adds about 2 ms to starting the binary (README, Overhead). Go is memory safe, the parts that decide
safety are rules and fail-closed paths that a rewrite would have to prove again, and the release build is
reproducible without cgo (ADR 0008, ADR 0010).
