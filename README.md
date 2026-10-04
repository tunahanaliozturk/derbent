<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/derbent-lockup-on-dark.svg">
  <img alt="Derbent" src="docs/assets/derbent-lockup-on-light.svg" height="64">
</picture>

# Derbent

One guarded pass for all your coding agents.

[![Derbent MCP server – quality and maintenance score on Glama](https://glama.ai/mcp/servers/tunahanaliozturk/derbent/badges/card.svg)](https://glama.ai/mcp/servers/tunahanaliozturk/derbent)

Every tool call that reaches Derbent, MCP or a CLI's built-in tools, is decided by one policy and written
to a tamper-evident log, and the calls you care about wait for you.

![A terminal session: derbent explain shows that git push origin main matches an ask rule; Claude Code
hook calls fed to derbent gate let go test run and deny rm -rf by a rule; derbent receipts lists both
calls and derbent verify finds the chain intact; after one receipt is edited with sqlite3, derbent verify
names receipt 2 as where the chain breaks](docs/assets/demo/demo.gif)

Derbent guards against mistakes and prompt injection, and keeps an audit trail. It is not a sandbox: an
agent that can already run shell commands as you can get around it (see [Limits](#limits)).

In Turkish history, a derbent was a guarded post on a mountain pass: its keepers decided who went through
and kept a record of everyone who did. Derbent does the same for your coding agents' tool calls.

Claude Code, Codex, GitHub Copilot CLI and Antigravity CLI connect to Derbent as one MCP server, and your
other MCP servers sit behind it. Each CLI's pre-tool hook sends its built-in tools, such as the shell and
file edits, through the same gate. Every call is decided by your rules and written down.

Support for Codex and Antigravity CLI is experimental (configured from their docs, not yet checked in a
real session).

![Four coding agent CLIs send MCP calls to derbent mcp and built-in tool calls to the derbent gate hook; both write a receipt to derbent.db, which the derbent terminal UI reads to show and approve calls, allowed MCP calls go on to your MCP servers, and a built-in call that is not denied runs in the CLI, which still applies its own permission check to a rule allow](docs/assets/diagrams/how-it-works.png)

It is one binary for Windows, macOS and Linux. There is no daemon and no network listener: a SQLite file
is the only shared state ([ADR 0001](docs/adr/0001-no-daemon.md)).

## What you get

- **Rules that allow, deny or ask**, by agent, tool and argument. The first match wins. The same rules
  apply to MCP tools and to built-in tools such as the shell, in all four CLIs, and a repository can add
  project rules that only make them stricter.
- **Approvals.** A call your rules ask about waits until you press `a` in the terminal UI, or is denied
  after 50 seconds by default.
- **Receipts.** Every call gets a hash-chained receipt, and `derbent verify` names the first receipt
  where the chain breaks after one was edited, moved, inserted or removed. Keep the head hash it prints
  to catch the newest ones being deleted too.

Also:

- **Shared memory and handoffs.** Agents keep notes per repository and can leave tasks for each other.
- **Pins** hold back a downstream server's tool when its definition changes.
- **Budgets** stop an agent stuck in a loop.

## Install

Download the binary for your system and `SHA256SUMS` from the
[latest release](https://github.com/tunahanaliozturk/derbent/releases/latest), check the hash, and put it
on your `PATH` as `derbent` (`derbent.exe` on Windows):

```bash
# Linux on amd64; the other files differ only in the end of the name
curl -LO https://github.com/tunahanaliozturk/derbent/releases/download/v1.0.0/derbent-v1.0.0-linux-amd64
curl -LO https://github.com/tunahanaliozturk/derbent/releases/download/v1.0.0/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
install -m 755 derbent-v1.0.0-linux-amd64 ~/.local/bin/derbent
```

Or build it with Go 1.27 or later:

```bash
go install github.com/tunahanaliozturk/derbent/cmd/derbent@latest
```

Homebrew (`brew install tunahanaliozturk/tap/derbent`) and Scoop install it too; see
[Install and set up](docs/install.md#homebrew).

Every release can be rebuilt byte for byte from its tag. [Install and set up](docs/install.md) has the
other systems, how to check a release, and each CLI's entries by hand.

## Quick start

```bash
derbent init --preset balanced   # add Derbent to each CLI it finds, and write a first config
derbent doctor                   # check what init wrote
derbent                          # open the terminal UI in a terminal of its own
```

`derbent init` shows every change and asks once before it makes any. It copies each file it changes
first and never replaces an entry you already have. With `--dry-run` it only shows the changes. Two of
them, from a real run with the paths shortened:

```text
claude: ~/.claude.json
  copied first to ~/.claude.json.derbent-backup-20260930T205424Z
  runs:
    claude mcp add --scope user derbent -- ~/bin/derbent mcp --agent claude
derbent: ~/.config/derbent/config.toml
  a new file
  adds the balanced preset (derbent init --preset balanced --print shows it)
```

It also adds the `derbent gate` hook to each CLI's settings; [Install and set up](docs/install.md) shows
every entry. Start your agents as usual. Their calls now appear in the UI, and calls the rules ask about
wait there for you. [Built-in tools](docs/built-in-tools.md) says what each CLI's hook covers.

To put your other MCP servers behind the gate, so each agent needs only the one `derbent` entry, see
[Downstream servers](docs/servers.md).

## How a call is decided

![A tool call is checked against your rules, where the first match wins, then against the project's rules, which can only make it stricter, then against budgets; the result is allow, ask, which waits for you, or deny, and every call gets one receipt](docs/assets/diagrams/how-a-call-is-decided.png)

Rules live in `config.toml` in your user config directory (`%AppData%\derbent\` on Windows,
`~/.config/derbent/` on Linux, `~/Library/Application Support/derbent/` on macOS). A downstream MCP
server's tools are named `<server>__<tool>`, and a CLI's built-in tools `native__<tool>`:

```toml
[[rule]]
tool   = "github__issue_read"
action = "allow"

[[rule]]
tool   = "github__*"          # every other GitHub tool waits for you
action = "ask"

[[rule]]
agent  = "claude"
tool   = "native__Bash"       # Claude Code's shell, through the hook
args   = { command = "git push*" }
action = "ask"

[[rule]]
action = "allow"              # the last rule has no condition
```

To see how a call would be decided before it happens:

```bash
derbent explain --agent claude --tool native__Bash --args '{"command":"git push origin main"}'
```

It prints each rule and why it matches or not, down to the first match, then the project's rules,
budgets, pin and grant, and ends with `verdict:  ask (rule:3)` for this call under the rules above. [Rules](docs/rules.md) covers globs, the three presets (`watch`,
`balanced`, `strict`), project rules, budgets and rule suggestions.

## Receipts

![Each receipt stores who called which tool, the decision and a hash of the result, plus the hash of the receipt before it; derbent verify recomputes the chain and names the first broken receipt, and the head hash can be kept elsewhere to check an export](docs/assets/diagrams/receipt-chain.png)

```bash
derbent verify                              # count, head hash, and whether the chain is intact
derbent receipts --agent codex --since 1h   # also --tool, --project, --limit, --json
```

A receipt keeps the arguments after masking secrets, and only the size and hash of what a tool returned
([ADR 0004](docs/adr/0004-hash-chained-receipts.md)). Someone who can write the database could still
rewrite the whole chain or delete the newest receipts, so keep the head hash somewhere else if you want
to be able to tell later. [Receipts and verify](docs/receipts.md) covers exports that can be
checked without the database.

## Commands

| Command | What it does | More |
|---|---|---|
| `derbent` | Terminal UI: waiting calls, agents, live receipts, memory, grants | [Approvals](docs/approvals.md) |
| `derbent init`, `derbent doctor` | Add Derbent to each CLI, then check the setup | [Install](docs/install.md) |
| `derbent mcp --agent <name>` | The MCP server each CLI starts | [Install](docs/install.md) |
| `derbent gate --agent <name>` | The pre-tool hook for built-in tools | [Built-in tools](docs/built-in-tools.md) |
| `derbent pending`, `approve`, `deny` | Answer waiting calls from any shell | [Approvals](docs/approvals.md) |
| `derbent grants`, `revoke` | List and take back session grants | [Approvals](docs/approvals.md) |
| `derbent explain` | Show how a call would be decided | [Rules](docs/rules.md) |
| `derbent suggest` | Print the rules your answers point to | [Rules](docs/rules.md#rule-suggestions) |
| `derbent config check` | Validate the config and list each server's tools | [Servers](docs/servers.md) |
| `derbent pins` | See and accept changed downstream tools | [Servers](docs/servers.md#tool-pins) |
| `derbent receipts`, `verify` | Read, export and verify receipts | [Receipts](docs/receipts.md) |
| `derbent handoffs` | List the tasks agents left for each other | [Memory and handoffs](docs/memory-and-handoffs.md) |
| `derbent version` | Print the release | |

## Overhead

Measured on GitHub's hosted runners on 2026-10-02 (Linux on an AMD EPYC 9V74, Windows on an AMD EPYC
7763, 4 vCPUs each), median of ten runs at p50, from
[docs/benchmark-results](docs/benchmark-results/README.md):

| | Linux | Windows |
|---|---|---|
| MCP tool call, direct to the server | 201.0 µs | 342.5 µs |
| MCP tool call, through the gate | 609.5 µs | 1004.0 µs |
| Starting the binary and exiting | 3.443 ms | 50.99 ms |
| Hook call, `derbent gate`, allowed | 5.812 ms | 105.66 ms |
| Hook call while a gate is running | 4.795 ms | 53.62 ms |

The gate adds 408.5 µs to an MCP call on Linux and 661.5 µs on Windows, for the extra stdio hop, the rule
decision, the project rules check and the receipt written to SQLite. A lone hook call costs about 2.4 ms
more than starting the binary on Linux and 55 ms more on Windows. Beside a running gate, as while a CLI
has Derbent's MCP entry running, it costs 1.4 ms more on Linux and 2.6 ms more on Windows, where that is
within the start's own noise. Why the lone call costs more is not proven yet; the likely cause is that it
is the database's last connection, so its close checkpoints the WAL and deletes the `-wal` and `-shm`
files. The numbers come from one run on shared runners, and runs differ by more than one run's intervals:
the run before, with the same code on the lone hook's path and an EPYC 9V74 for Windows, put the Windows
hook 24.11 ms over the start. The results page has p99, calls per second and the caveats.

## Limits

The whole list, with the reasons, is under [Known limits and risks](docs/design.md#known-limits-and-risks).
The ones to know first:

- Only calls that pass through the gate are seen. Tools a CLI never shows its hook, such as Codex's hosted
  web search, are outside it.
- The CLIs marked experimental at the top have entries and hook adapters that follow each CLI's
  documentation and have not been checked in a real session. Claude Code and Copilot CLI (1.0.88, on
  2026-10-02) have.
- Argument globs match strings, not meaning: `git push*` does not match `cd repo && git push`.
- Approvals guard against mistakes and prompt injection inside MCP. They do not stop an agent that can
  already run shell commands as you: it can run `derbent approve` itself.
- A receipt export shows its last run unchanged only against a head you kept, and never that nothing was
  left out of it.
- A suggestion refuses the shells, launchers and operators it knows, but a text rule can be fooled and
  those lists cannot be complete, and an `allow` for a command prefix lets any options through.
- A handoff's address is a label, not an identity: any agent that can call `handoff_take` can claim every
  open handoff addressed to `*`.
- Approvals depend on you watching. Unattended, `ask` means denied after the timeout.
- Pins trust the first definition they see, including a new tool that an update adds to a pinned server,
  so name the tools you allow for a server whose updates you do not review.
- A budget can be passed by the calls in flight at the same moment.
- CI runs the tests on Windows and Linux and only builds on macOS.

## For teams

A team audit trail, with receipts synced off each machine and an export for a SIEM, is an idea, not a
plan. If you would use it, say so in [this discussion](https://github.com/tunahanaliozturk/derbent/discussions/34).

## Docs

- [Install and set up](docs/install.md): every system, checking a release, each CLI by hand
- [Rules](docs/rules.md): rules, presets, `explain`, project rules, budgets, suggestions
- [Approvals and the UI](docs/approvals.md): keys, grants, answering from a shell
- [Built-in tools](docs/built-in-tools.md): the hook, tool names per CLI, what each CLI covers
- [Downstream servers and tool pins](docs/servers.md)
- [Receipts and verify](docs/receipts.md): what a receipt holds, exports, `verify --file`
- [Memory and handoffs](docs/memory-and-handoffs.md)
- [Demo](docs/demo.md): a transcript of two real Claude Code sessions sharing one gate
- [Design](docs/design.md), the contract for the build, and the [decisions](docs/adr/README.md) behind it

Changes are listed in the [changelog](CHANGELOG.md). To build, test or send a change, see
[CONTRIBUTING.md](CONTRIBUTING.md). To report a vulnerability, see [SECURITY.md](SECURITY.md).

## Licence

Apache 2.0. See [LICENSE](LICENSE).
