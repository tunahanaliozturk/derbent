<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/derbent-lockup-on-dark.svg">
  <img alt="Derbent" src="docs/assets/derbent-lockup-on-light.svg" height="64">
</picture>

# Derbent

One guarded pass for all your coding agents.

A derbent was a guarded post on an Ottoman mountain pass: its keepers decided who went through and kept a
record of everyone who did. Derbent does the same for your coding agents' tool calls.

Claude Code, Codex, GitHub Copilot CLI and Antigravity CLI connect to Derbent as one MCP server, and your
other MCP servers sit behind it. Each CLI's pre-tool hook sends its built-in tools, such as the shell and
file edits, through the same gate. The gate gives the agents one shared memory, decides every call with
your allow, deny and ask rules, holds the calls you want to see until you approve them, and writes a
hash-chained receipt for each one.

It is one binary for Windows, macOS and Linux. There is no daemon and no network listener: a SQLite file
is the only shared state.

## Install

Download the binary for your system and `SHA256SUMS` from the
[latest release](https://github.com/tunahanaliozturk/derbent/releases/latest). There are builds for
Windows, Linux and macOS, each on amd64 and arm64, all without cgo. Check the file's hash against its
line in `SHA256SUMS`, then put it on your `PATH` as `derbent` (`derbent.exe` on Windows):

```bash
# Linux on amd64; the other files differ only in the end of the name
curl -LO https://github.com/tunahanaliozturk/derbent/releases/download/v1.0.0/derbent-v1.0.0-linux-amd64
curl -LO https://github.com/tunahanaliozturk/derbent/releases/download/v1.0.0/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
install -m 755 derbent-v1.0.0-linux-amd64 ~/.local/bin/derbent
```

On macOS `shasum -a 256 <file>`, and on Windows `Get-FileHash <file>` in PowerShell, print the hash to
compare.

Or build it with Go 1.27 or later:

```bash
go install github.com/tunahanaliozturk/derbent/cmd/derbent@latest
```

`derbent version` prints the release, or `dev` for a build from source.

### Check a release

A release can be rebuilt byte for byte. The release workflow runs vet, the tests and the linter on the
tag, then builds every binary twice, the second time with an empty build cache, and publishes only when
both builds match. To check a published release,
rebuild it from a clean checkout of its tag with Go 1.27.1 and no `GOFLAGS`, `GOAMD64` or `GOARM64`
overrides, in the environment or set with `go env -w`:

```bash
git clone --branch v1.0.0 https://github.com/tunahanaliozturk/derbent.git
cd derbent
env -u GOFLAGS -u GOAMD64 -u GOARM64 GOTOOLCHAIN=go1.27.1 scripts/release.sh v1.0.0
```

Then compare `dist/SHA256SUMS` with the `SHA256SUMS` published with the release. The script needs bash,
`cmp` and `sha256sum`; on Windows, Git Bash has all three.

## Quick start

Each CLI needs two entries: Derbent as an MCP server, and `derbent gate` as its pre-tool hook. `--agent`
is the name your rules match, so give both entries the same one. Agent names are lower-case letters,
digits, dashes and underscores.

**Claude Code**

```bash
claude mcp add derbent -- derbent mcp --agent claude
```

and in `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "*", "hooks": [ { "type": "command", "command": "derbent gate --agent claude" } ] }
    ]
  }
}
```

**Codex**

```bash
codex mcp add derbent -- derbent mcp --agent codex
```

and in `~/.codex/config.toml`. Codex asks you once to trust a new hook before it runs it:

```toml
[[hooks.PreToolUse]]
matcher = ".*"

[[hooks.PreToolUse.hooks]]
type = "command"
command = "derbent gate --agent codex"
```

**GitHub Copilot CLI**

```bash
copilot mcp add derbent -- derbent mcp --agent copilot
```

and in `~/.copilot/hooks/derbent.json`:

```json
{
  "version": 1,
  "hooks": {
    "preToolUse": [
      { "type": "command", "bash": "derbent gate --agent copilot", "powershell": "derbent gate --agent copilot", "timeoutSec": 120 }
    ]
  }
}
```

**Antigravity CLI**, in `~/.gemini/config/mcp_config.json`:

```json
{
  "mcpServers": {
    "derbent": { "command": "derbent", "args": ["mcp", "--agent", "antigravity"] }
  }
}
```

and in `~/.gemini/config/hooks.json`:

```json
{
  "derbent": {
    "PreToolUse": [ { "matcher": ".*", "hooks": [ { "command": "derbent gate --agent antigravity", "timeout": 120 } ] } ]
  }
}
```

Only Claude Code has been checked in real sessions, through the MCP server and through the hook. The
entries for the other three follow each CLI's documentation (see [Built-in tools](#built-in-tools)).

Every agent now has `memory_write`, `memory_search` and `memory_read`, and a note one agent writes in a
repository can be found by the others in the same repository. Search is full-text, and results mark
each note with its author and as a note, not an instruction. Without a config file every call is
allowed, and `derbent mcp` says so on stderr when it starts. Every call is recorded:

```bash
derbent verify
```

prints the number of receipts, the hash of the last one, and whether the chain is intact.

## Demo

[docs/demo.md](docs/demo.md) is a transcript of two real Claude Code sessions sharing one gate. One writes
a decision to memory; the other finds it and asks to add a note, which waits until `derbent approve`
approves it from another shell; `derbent verify` then finds the chain intact. The version with Codex and
a GitHub issue is described there too, and was not recorded.

## Rules

Rules live in `config.toml` in your user config directory: `%AppData%\derbent\` on Windows,
`~/.config/derbent/` on Linux (or `$XDG_CONFIG_HOME/derbent/`), and
`~/Library/Application Support/derbent/` on macOS. When that file does not exist every call is allowed.
`--config` names another file, which must exist: if it does not, `derbent mcp` and
`derbent config check` stop with an error that names the path, and the hook denies the call.

```toml
[[rule]]
agent  = "codex"
tool   = "memory_write"
action = "deny"

[[rule]]
tool   = "github__issue_read"
action = "allow"

[[rule]]
tool   = "github__*"
action = "ask"

[[rule]]
agent  = "claude"
tool   = "native__Bash"
args   = { command = "git push*" }
action = "ask"

[[rule]]
action = "allow"
```

Rules are tried in order and the first match wins ([ADR 0003](docs/adr/0003-first-match-rules.md)).
Here `github__issue_read` is allowed and every other GitHub tool asks, because a server can write under
names that a pattern such as `github__create_*` misses, like the GitHub server's `issue_write`.
`agent` and `tool` are globs, where `*` matches any run of characters and `?` exactly one, and `args`
matches top-level string arguments the same way. The last rule must have no condition, so every call gets
its decision from your file. A tool that no call can get through, such as one a plain `deny` catches
before any `allow` or `ask`, is not listed to that agent, and a call to it by name is still refused. An
`args` condition that meets a value it cannot read, such as an array, matches a `deny` or an `ask` and
never an `allow`.

The file is read strictly: an unknown key is an error that names the key, and a syntax error names its
line. A `derbent mcp` gate reads the file when its CLI starts it; the hook reads it on every call.

## Project rules

A repository can make Derbent stricter for itself. A `.derbent.toml` at the root of a checkout holds
`[[rule]]` tables and nothing else:

```toml
[[rule]]
tool   = "native__Bash"
args   = { command = "terraform *" }
action = "ask"
```

The file is read at the root of the checkout the agent works in: the git root, or for a linked worktree
the worktree's own root, so a branch that adds or tightens the file is enforced in its worktree. A worktree
still shares the main checkout's memory and receipts.

Project rules are tried in order and need no final rule; when none matches, the project adds nothing. A
call gets the stricter of your decision and the project's, deny over ask over allow, so a project rule can
make a call ask or deny but never lets through what your rules refuse
([ADR 0014](docs/adr/0014-project-rules.md)). Receipts say `project:1` when a project rule decided, and the
UI and `derbent pending` say `project rule 1`. An edit takes effect on the next call. A file with any other
key, or a rule Derbent cannot read, denies every call in that project until it is fixed, and so does a
`.derbent.toml` that is a symbolic link, is not a regular file, or is larger than 64 KiB. Project rules
decide calls only and never change which tools an agent sees. An agent that can edit the repository can
edit or delete the file, which only takes the project back to your own rules.

## Approvals and the UI

An `ask` rule holds the call until you decide. The tool stays listed to the agent:

```toml
[[rule]]
tool   = "github__*"
action = "ask"

[[rule]]
action = "allow"

[approvals]
timeout = "50s"
```

The timeout is optional: 50 seconds unless you set it, and at least one second. A call nobody answers is
denied when it runs out, and the agent is told why.

Run `derbent` in a terminal of its own. Calls waiting for you sit at the top, each as `#12` with the
agent, the tool, the time left, the number of the rule that asked, the project and the start of its
arguments, masked as they are in receipts, and the terminal bell rings when a new one arrives. `enter` shows the highlighted call's whole arguments, and up,
down, page up, page down, home and end scroll them. The keys act on the highlighted call: `a` approves it
once, `d` denies it, and `A` pressed twice within five seconds approves the tool's calls that the same
rule asks about under the same rules above it, for the rest of that agent's session; a call whose rule
could not read one of its arguments, such as a `command` sent as an array, asks every time. Up and down pick
another call. A newly highlighted call takes none of these keys for its first 750 ms on the main screen,
so a key meant for the call before it cannot land on it.

Below the waiting calls are the agents seen in the last hour and a live feed of receipts. `/` filters the
feed, `m` searches memory across projects, `g` lists session grants, `v` verifies the receipt chain, `?`
lists the keys and `q` quits. Quitting the UI changes nothing for running agents: their waiting calls are
denied at the timeout.

Text from agents and tools is escaped before it is drawn: control characters, bidirectional overrides,
zero-width and other format characters, line and paragraph separators, variation selectors, tag
characters, the Hangul fillers, the combining grapheme joiner and the braille blank are written as `\u`
or `\U` codes, and in arguments a run of more than eight spaces, the ASCII space or any other Unicode
space separator, is shown as `␠×N`.

The same decisions work from any shell, by the id the UI shows, written `12` or `#12`:

```bash
derbent pending                # the waiting calls, their rules, projects and whole arguments; --json too
derbent approve 12             # approves once, like a
derbent approve --session 12   # like A; flags go before the id
derbent deny '#12'             # quote the # in a shell that reads it as a comment
```

Editing or reordering the rules never widens a grant ([ADR 0011](docs/adr/0011-grants-follow-the-rule.md)).
The hook reads the config on every call, so there, while the granted rule or a rule above it differs
from when you approved, the calls that rule asks about ask again, and undoing the edit makes the grant
apply again; a change below the granted rule keeps the grant. A `derbent mcp` gate reads the config only
when it starts, so an edit does not affect its grants.

Grants can be listed and taken back:

```bash
derbent grants                 # every session grant: id, agent, session, tool, the rule that asked, when; --json too
derbent revoke 12              # the grant approval #12 made; the next such call asks again
derbent revoke --all
```

In the UI, `g` lists the same grants, and `r` pressed twice within five seconds revokes the highlighted one.

Approvals guard against mistakes and against prompt injection that stays inside MCP. They are not a
boundary against an agent that can already run shell commands as you: it can run `derbent approve` itself
or write the database, so an approval or an `args` rule on a shell tool does not hold it back.

The default timeout sits below Codex's default tool timeout of 60 seconds
([ADR 0005](docs/adr/0005-approval-timeout.md)). If you raise it, raise `tool_timeout_sec` for the
`derbent` server in Codex's config too.

## Budgets

A budget stops an agent stuck in a loop. It limits how many calls one agent may have let through to some
tools within a sliding window:

```toml
[[budget]]
agent = "*"
tool  = "native__Bash"
calls = 200
per   = "1h"
```

`agent` and `tool` are globs as in rules, `calls` is at least 1, and `per` is a duration from `1m` to `24h`.
Every budget that matches a call applies. A call counts when its receipt says it was let through, by a
rule, a grant or you, so the count is shared by every gate and hook that uses the same database. The rules
decide first and a `deny` stays a deny; otherwise a call over a budget is refused without asking you, its
receipt says `budget:1`, and the agent reads when its next call is possible
([ADR 0012](docs/adr/0012-budgets.md)).
Calls made at the same moment can pass a budget by at most the number of calls in flight at once.

## Built-in tools

An agent's shell commands and file edits do not go through MCP, but each of the four CLIs can run a
command of your choice before a tool call. With `derbent gate` as that command (see
[Quick start](#quick-start)), each call is decided as tool `native__<the CLI's tool name>` under the same
rules and approvals, gets a receipt with outcome `gated` or `refused`, and is answered in the CLI's
format: nothing for a call a rule allows, so the CLI's own permission settings still apply, `allow` for a
call you approved, and `deny` with the reason for anything refused
([ADR 0006](docs/adr/0006-pre-tool-hooks.md)).

Rules name a built-in tool by the CLI's own name for it, so they differ per CLI: `native__Bash` (Claude
Code, Codex), `native__apply_patch` (Codex's file edits), `native__bash` and `native__powershell`
(Copilot CLI) and `native__run_command` (Antigravity CLI). The arguments differ too:

```toml
[[rule]]
agent  = "claude"
tool   = "native__Bash"
args   = { command = "git push*" }
action = "ask"

[[rule]]
agent  = "antigravity"
tool   = "native__run_command"
args   = { CommandLine = "git push*" }
action = "ask"
```

Derbent cannot hide a CLI's built-in tools from the model, so a `deny` on one refuses each call instead.
In Claude Code and Codex, which send MCP calls to the hook too, a tool of an MCP server configured in the
CLI directly is decided the same way, as `native__mcp__<server>__<tool>`.

`A` on a built-in tool covers the rest of the CLI's session: its session id in Claude Code, Codex and
Copilot CLI, its conversation id in Antigravity CLI. Every shell command is one tool, such as
`native__Bash`, so an `A` on a `git push` does not let through a `terraform apply` that another rule asks
about. For MCP tools the session is one `derbent mcp` process. A grant on one path never covers the other,
since the tools have different names.

The hook fails closed: when it cannot read the call, load the config, open the database or write the
receipt, it denies the call and says why, so a mistake in the config stops every built-in tool until it
is fixed. Claude Code documents that a hook it times out on decides nothing, and the call goes on through
its own permission flow. The other CLIs do not say, and Derbent assumes they behave the same, so the
hook's timeout must stay above `[approvals] timeout`. Claude Code and Codex give a hook 600 seconds by
default; Copilot CLI and Antigravity CLI give it 30, which is why their examples set 120.

The hook protocol follows the agent name; for any other name add `--cli claude`, `codex`, `copilot` or
`antigravity`. If Derbent's MCP entry in the CLI has another name than `derbent`, pass it with `--server`,
or each call to Derbent's own tools is decided and recorded twice. Two kinds of entry cannot be matched
with `--server`, so their calls to Derbent's own tools are decided and recorded twice: a Derbent bundled in
a Claude Code plugin, whose tools Claude Code names `mcp__plugin_<plugin>_<server>__`, and an entry whose
name Claude Code rewrites because it holds characters other than letters, digits, `_` and `-`. Where the
CLI does not name the MCP server in the hook's input, the reverse can happen: another MCP entry whose tool
names come out as Derbent's prefix followed by a name Derbent serves, such as an entry called
`derbent__github` in Codex, is taken for Derbent's own, and its calls get no decision and no receipt from
the hook (see [Known limits](docs/design.md#known-limits-and-risks)).

The hook reads the same config file but starts no servers, so a `${env:NAME}` that is not set where the
hook runs is left alone, and server secrets can stay in the CLI's MCP entry for Derbent. Receipts from the
hook then mask only the secrets set in the hook's own environment; add a `[receipts] redact` pattern for
the others.

What each CLI's hook covers, from its documentation and, where marked, a real session:

| CLI | Built-in tools gated | Derbent's own tools appear as | Not seen by the gate | Checked in a real session |
|---|---|---|---|---|
| Claude Code | every tool, through PreToolUse | `mcp__derbent__*` | nothing known | Claude Code 2.1.283, on 2026-09-27, with `claude -p --settings`: `echo derbent-allow` ran with a receipt `native__Bash allow rule:2 gated`; `echo derbent-deny` was blocked with Derbent's reason and a receipt `deny rule:1 refused`; `memory_write` through the derbent MCP server, which Claude Code names `mcp__derbent__memory_write`, got one receipt, from the MCP gate, because the hook skipped it; other built-in tools such as ToolSearch reach the hook too, as `native__ToolSearch`. |
| Codex | shell commands (`Bash`), `apply_patch` for every file edit, and other local function tools such as `update_plan` | `mcp__derbent__*` | hosted tools such as web search | Not checked: left out by the owner's choice. |
| Copilot CLI | shell (`bash`, `powershell`), file tools (`view`, `create`, `edit`, `apply_patch`), `grep`, `glob`, `web_fetch`, `web_search` and its other documented tools | `derbent-*`, with names capped at 64 characters | possibly MCP calls: the documentation does not say whether the hook sees them | Not checked: Copilot CLI 1.0.88 stopped at "You have exceeded your monthly quota" before any tool call. |
| Antigravity CLI | built-in tools such as `run_command`, `view_file`, `write_to_file` and `replace_file_content` | `mcp_derbent_*`, an assumption until checked | not documented | Not checked: not installed. |

## Downstream servers

Your other MCP servers go behind the gate in the same `config.toml`, and each agent then needs only the
one `derbent` entry:

```toml
[servers.github]
command = ["github-mcp-server", "stdio"]
env     = { GITHUB_PERSONAL_ACCESS_TOKEN = "${env:GITHUB_TOKEN}" }

[servers.docs]
url     = "https://docs.example.com/mcp"
headers = { Authorization = "Bearer ${env:DOCS_TOKEN}" }

[receipts]
redact = ['ghp_[A-Za-z0-9]{36}']
```

Their tools appear as `github__get_me` and so on, under the same rules and receipts. The GitHub server
writes under many tool names, such as `issue_write` and `add_issue_comment`, so ask about `github__*`
as in [Rules](#rules), or start it with `--read-only` or fewer `--toolsets`. A `command` server
speaks MCP over stdio, and a `url` server over HTTP, which must be `https` except on `localhost`,
`127.0.0.1` or `::1`; redirects are refused, so its headers never reach another host. Secrets come from
the environment through `${env:...}` in `env` or `headers`, never in `command` or `url`, where they could
leak into process listings and errors. Their values are masked in stored arguments when they are at least
eight characters long, as is anything the `redact` patterns match. A server that stops is started again,
with a backoff of up to a minute, and calls to its tools while it is down are tool errors. Derbent
forwards tools only, not a server's resources or prompts.

```bash
derbent config check
```

validates the config, says how many rules and budgets it holds, starts every server once and prints the
tools each would give the agents.

Codex starts MCP servers with only a few environment variables. If your config uses `${env:NAME}`, add
`env_vars = ["NAME"]` to the `[mcp_servers.derbent]` entry in Codex's config; without it the gate does
not start, and its error names the missing variable.

## Tool pins

A server can change a tool's description after you started trusting it, and a description is text the
agent reads. The first time a gate sees a downstream tool, it pins it: it keeps the SHA-256 of the tool's
name, title, description, schemas and annotations. When a later start of the server sends a different
definition, the gate leaves the tool out of the agent's list, so the agent never reads the new text,
refuses calls to it and writes a warning to stderr ([ADR 0013](docs/adr/0013-tool-pins.md)).

```bash
derbent pins                                       # every pin, pinned or changed; --json too
derbent pins show github__create_issue             # the pinned and the new definition, and the lines that differ
derbent pins accept github__create_issue <sha256>  # takes the whole hash that pins show prints
```

For a changed tool, `pins show` ends with the `accept` command, ready to copy. An accept of a hash that
is not the change on record is refused, as is a shortened hash, so what is accepted is what was read.
After `accept`, running gates serve the tool again within two seconds. `derbent config check` shows each
tool's pin state and pins nothing, and the UI says how many tools changed. A server whose descriptions
change on every start can opt out with `pin = false` in its `[servers.<name>]` table. Pins trust what they
see first, so look at a new server's tools with `derbent config check` before an agent uses them.

That goes for a new tool that an update adds to a server already pinned, too: it is pinned and served at
once, even in the middle of a session. For a server whose updates you do not review, name the tools you
allow or ask about, such as `github__create_issue`, and deny `github__*` after them. A tool that no call
can get through is left out of the agents' lists, so a new tool's description reaches no agent until you
add it; an `allow` or `ask` on `github__*` lists it at once.

## Receipts and verify

Each call through the gate appends one receipt: its sequence number, time, project, agent, gate session,
tool, arguments after masking, the SHA-256 of the arguments before masking, the decision and what made it
(a rule, the gate itself, or an approval), the outcome, the size and SHA-256 of the result, the duration,
and the hash of the receipt before it ([ADR 0004](docs/adr/0004-hash-chained-receipts.md)). What a tool
returned is kept as size and hash only.

```bash
derbent verify                              # count, head hash, and whether the chain is intact
derbent receipts --agent codex --since 1h   # also --tool 'github__*', --project, --limit
derbent receipts --json                     # JSON lines
```

When a receipt has been edited, removed, moved or inserted, `derbent verify` names the first one where the
chain breaks and exits with an error. It opens the database read-only and never creates it. Someone able
to write the database could still rewrite the whole chain, or delete the newest receipts, and the chain
would verify. Keep a copy of the head hash somewhere else if you want to be able to tell later that
neither happened.

## Overhead

Measured on GitHub's hosted runners on 2026-09-27, after milestone 6, from one workflow run: Linux and
Windows each on an AMD EPYC 7763 with 4 vCPUs (images `ubuntu-24.04` and `windows-2025-vs2026`). Each
value is the median of ten runs at p50, from [docs/benchmark-results](docs/benchmark-results/README.md):

| | Linux | Windows |
|---|---|---|
| MCP tool call, direct to the server | 304.5 µs | 353.0 µs |
| MCP tool call, through the gate | 825.5 µs | 1033.0 µs |
| Starting the binary and exiting | 4.316 ms | 46.91 ms |
| Hook call, `derbent gate`, allowed | 7.381 ms | 83.03 ms |

So the gate adds 521.0 µs to an MCP call on Linux and 680.0 µs on Windows, for the extra stdio hop, the
rule decision, the project rules check and the receipt written to SQLite. A hook call takes 3.065 ms
longer than starting the binary and exiting on Linux, and 36.12 ms longer on Windows, where most of its
cost is the process start itself. The results page has p99 and calls per second as well, and the numbers
from before milestone 6.

These numbers come from one run on shared runners, one VM for each OS, so compare the gate with its
baseline within one OS, not one OS with the other. Another run may differ by more than the confidence
intervals shown there. The hook's p99 rests on 200 samples per run, so it is about the second largest.
The binary measured is the test build of `cmd/derbent`, not a release build.

## Limits

The whole list, with the reasons, is under [Known limits and risks](docs/design.md#known-limits-and-risks)
in the design. The ones to know first:

- Only calls that pass through the gate are seen. Tools a CLI never shows its hook, such as Codex's hosted
  web search, are outside it.
- Only Claude Code's hook has been checked in a real session. The Codex, Copilot CLI and Antigravity CLI
  adapters follow each CLI's documentation.
- Argument globs match strings, not meaning: `git push*` does not match `cd repo && git push`.
- Approvals depend on you watching. Unattended, `ask` means denied after the timeout.
- Pins trust the first definition they see, including a new tool that an update adds to a pinned server,
  so name the tools you allow for a server whose updates you do not review. A budget can be passed by the
  calls in flight at the same moment.
- CI runs the tests on Windows and Linux and only builds on macOS.

## Decisions

The [design](docs/design.md) is the contract for the build: goals, non-goals, how the parts fit, and how
each claim is tested. Every decision someone could reasonably have made differently has an ADR:

| ADR | Decision |
|---|---|
| [0001](docs/adr/0001-no-daemon.md) | No daemon: gate processes and the UI share state only through SQLite. |
| [0002](docs/adr/0002-stdio-and-agent-labels.md) | Agents connect over stdio, and the agent name is a label from each CLI's config. |
| [0003](docs/adr/0003-first-match-rules.md) | Rules are first-match, and a plain deny hides the tool. |
| [0004](docs/adr/0004-hash-chained-receipts.md) | Receipts are hash-chained, and results are kept as size and hash only. |
| [0005](docs/adr/0005-approval-timeout.md) | Approvals time out below the clients' tool timeouts, and a timeout denies. |
| [0006](docs/adr/0006-pre-tool-hooks.md) | Built-in tools are gated through each CLI's pre-tool hook. |
| [0007](docs/adr/0007-full-text-memory.md) | Memory search is SQLite FTS5, without embeddings. |
| [0008](docs/adr/0008-sqlite-without-cgo.md) | SQLite through modernc.org/sqlite, with no cgo. |
| [0009](docs/adr/0009-supervised-downstream-servers.md) | Downstream servers are supervised from the moment the gate starts. |
| [0010](docs/adr/0010-go.md) | Derbent is written in Go. |
| [0011](docs/adr/0011-grants-follow-the-rule.md) | A session grant covers only the calls the same rule asks about under the same rules above it. |
| [0012](docs/adr/0012-budgets.md) | Budgets count receipts, every matching budget applies, and a used-up budget refuses without asking. |
| [0013](docs/adr/0013-tool-pins.md) | Downstream tools are pinned on first use, and a changed tool is withheld until you accept it. |
| [0014](docs/adr/0014-project-rules.md) | Project rules can only tighten your rules and never change tool listings. |

Changes are listed in the [changelog](CHANGELOG.md). To build, test or send a change, see
[CONTRIBUTING.md](CONTRIBUTING.md). To report a vulnerability, see [SECURITY.md](SECURITY.md).

## Licence

Apache 2.0. See [LICENSE](LICENSE).
