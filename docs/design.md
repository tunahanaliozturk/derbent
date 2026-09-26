# Portcullis design

Portcullis is an agentic gate: the one MCP server that Claude Code, Codex, GitHub Copilot CLI and
Antigravity CLI all connect to, with the user's other MCP servers behind it. Every tool call passes
through it, so it can give all the agents one shared memory, write a receipt for each call, hold risky
calls until the user approves them, and decide which agent sees which tool.

It runs on the user's machine, on Windows as well as macOS and Linux, as one binary with no daemon.
A SQLite file is the only shared state.

This document is the contract for the build. Each decision that someone could reasonably have made
differently has an ADR under `docs/adr/`.

## Goals

- One MCP entry in each agent CLI, instead of one entry per server per CLI.
- Memory that any agent can write and any agent can search, kept per project.
- A receipt for every call through the gate, hash-chained so that an edited or deleted receipt is found.
- Rules that allow, deny or ask by agent, tool and argument, with the user approving from one terminal.
- Tools an agent may not use are not listed to that agent at all.
- Claude Code's built-in tools (shell, file edits) pass the same rules and get the same receipts, through
  its hooks.

## Non-goals

- Seeing calls that do not pass through it. Built-in tools of a CLI without a pre-tool hook are outside
  the gate, and the README states per CLI what is covered.
- Model traffic. Portcullis sits between agents and tools, not between agents and model providers.
- Several users or machines. One person, one machine, no network listener.
- Semantic search. Memory uses full-text search.
- A web UI.

## How it fits together

```
 Claude Code ─┐   stdio    ┌───────────────────────────┐   stdio / HTTP
 Codex ───────┤ ─────────> │ portcullis mcp --agent X  │ ──────────────> the user's MCP servers
 Copilot CLI ─┤  one gate  │   rules, memory, receipts │
 Antigravity ─┘  process   └─────────────┬─────────────┘
                 per agent               │
                 session                 v
                              portcullis.db (SQLite, WAL)
                                         ^
                                         │
                              portcullis (terminal UI): approvals, receipts, memory
```

Each agent CLI starts `portcullis mcp --agent <name>` as a stdio MCP server, exactly as it starts any
other server. That process reads the config, starts the downstream servers as MCP clients, lists their
tools together with the memory tools, applies the rules to each call, forwards allowed calls, and
appends a receipt. Running `portcullis` with no arguments in another terminal opens the UI, which reads
and writes the same database.

There is no daemon (ADR 0001). The gate processes and the UI coordinate only through SQLite in WAL mode,
so there is nothing to start first or keep alive, and a crash takes down one agent's gate, not everyone's.
The costs: each agent session starts its own copy of the downstream servers, as it would without
Portcullis, and a pending approval is noticed by polling every 200 ms.

**Agent identity** is the `--agent` value written in each CLI's MCP config. It is a label, not
authentication: any process running as the user could claim any name (see Security).

**Project** is the git root of the directory the gate was started in, or `--project <path>`. A linked
worktree counts as the repository it was added from, so agents working in separate worktrees of one
repository share notes; a submodule is a project of its own.

## Config

TOML in the user config directory (`%APPDATA%\portcullis\config.toml` on Windows,
`$XDG_CONFIG_HOME/portcullis/config.toml` on Linux), decoded strictly: an unknown key is an error that names
the key, and a syntax error names its line. `portcullis config check`
validates it and starts each downstream server once to list its tools.

```toml
[servers.github]
command = ["github-mcp-server", "stdio"]
env     = { GITHUB_PERSONAL_ACCESS_TOKEN = "${env:GITHUB_TOKEN}" }

[servers.docs]
url = "https://docs.example.com/mcp"

[[rule]]
tool   = "memory_*"
action = "allow"

[[rule]]
agent  = "copilot"
tool   = "github__*"
action = "deny"

[[rule]]
tool   = "github__create_*"
action = "ask"

[[rule]]
tool   = "native__Bash"
args   = { command = "git push*" }
action = "ask"

[[rule]]
action = "ask"

[approvals]
timeout = "50s"

[receipts]
redact = ['(?i)bearer\s+\S+', 'ghp_[A-Za-z0-9]{36}']
```

- Rules are tried in order and the first match wins. A rule matches on `agent` and `tool` as globs, and
  on `args` as globs over top-level string arguments. The last rule must have no condition.
- A `deny` rule with no `args` condition hides the tool from that agent's tool list. With an `args`
  condition the tool stays listed and matching calls are refused.
- `${env:NAME}` is resolved when the gate starts. Resolved values never reach receipts or logs.
- A `url` server must use `https`, except on `localhost`, `127.0.0.1` or `::1`. `env` belongs to command
  servers and `headers` to url servers.
- Every value that came from `${env:...}` in `env` or `headers` and is at least eight characters long is
  masked in stored arguments, as well as whatever the `redact` patterns match. Masking works on each
  string value, so the stored arguments stay valid JSON.
- An `args` condition that meets a value it cannot read (not a string) matches a `deny` and never an
  `allow` (ADR 0003).

## Tools

- Downstream tools are listed as `<server>__<tool>`, so two servers can never collide. Server names
  are lower-case letters, digits and dashes, so the name splits one way only. A gate tool name must be
  1 to 64 letters, digits, underscores or dashes; a tool that does not fit, or whose input schema is not
  an object, is left out with a warning on stderr and marked by `portcullis config check`, never
  truncated.
- A downstream `notifications/tools/list_changed` is passed on to the agent after the rules are applied
  to the new list.
- A downstream server is supervised from the moment the gate starts (ADR 0009): it is started again
  after it stops, with a backoff from one second to one minute, and calls to its tools while it is down
  are tool errors. The gate keeps serving everything else.
- v1 forwards tools only. Downstream resources and prompts are not exposed.

Memory tools:

| Tool | Input | Output |
|---|---|---|
| `memory_write` | title, body (up to 16 KiB), tags, optional id it supersedes | id |
| `memory_search` | query, limit, optional `all_projects` | id, title, snippet, author, time |
| `memory_read` | id | the full entry |

Entries belong to a project and record the agent and the gate session that wrote them, which ties them to
that session's receipts.
Superseded entries drop out of search results but stay readable by id. Search is SQLite FTS5 ranked by
bm25 (ADR 0007).

## Receipts

Each call through the gate appends one row: sequence number, time, project, agent, gate session, tool,
arguments after redaction, SHA-256 of the arguments before redaction, the decision and what made it (a
rule index or the user), the outcome, the size and SHA-256 of the result, the duration, the previous
receipt's hash, and this receipt's hash.

- The hash is SHA-256 over the previous hash and the stored bytes of the row's fields. Appends run in a
  `BEGIN IMMEDIATE` transaction that reads the current head, so gate processes appending at the same
  moment are serialised by SQLite and the chain has no forks.
- Results are stored as size and hash only. What a tool returned can be large or private, and the hash
  is enough to show later that a given result was the one returned. It is taken over a form of the
  result that can be rebuilt from what the agent received (ADR 0004).
- `portcullis verify` opens the database read-only, never creates or migrates it, walks the chain and
  names the first sequence number whose hash, predecessor or position is wrong. It prints the head hash. Someone able to write the database could rewrite the
  whole chain consistently, and keeping a copy of the head hash elsewhere is what catches that
  (ADR 0004).
- `portcullis receipts` lists and filters receipts by agent, tool, project and time, as a table or as
  JSON lines.

## Approvals

When a rule says `ask`, the gate writes a pending approval and polls for a decision. In the UI the
pending call shows at the top with the agent, the tool and the redacted arguments, and the terminal bell
rings.

- `a` approves once, `d` denies, and `A` approves this tool for the rest of that agent's gate session.
  Nothing the UI does changes the config file.
- `portcullis approve <id>` and `portcullis deny <id>` do the same without the UI.
- If no decision arrives within `approvals.timeout`, the call is denied with an error that tells the
  agent the approval timed out. The default of 50 seconds sits below the shortest default tool timeout
  among the four CLIs, which is checked against their documentation when the repository is scaffolded,
  so the agent sees a clear denial instead of a transport timeout (ADR 0005).

## Built-in tools

`portcullis gate --agent claude` is installed as a Claude Code `PreToolUse` hook matching every tool. It
reads the hook's JSON from standard input, treats the call as tool `native__<tool_name>` with the tool's
input as arguments, applies the same rules, waits for an approval when a rule says `ask`, and appends a
receipt.

- A call allowed by a rule gets no decision from the hook, so Claude Code's own permission settings
  still apply on top.
- A call the user approved gets `allow`.
- A denied or timed-out call gets `deny` with the reason, which Claude Code shows to the model.
- The receipt's outcome is `gated`, since the hook runs before the tool does.

Codex, Copilot CLI and Antigravity CLI are checked for an equivalent hook in milestone 4. Each one that
has it gets an adapter in `internal/hook/`, and the README's coverage table says what is and is not gated
for each CLI.

## Terminal UI

Bubble Tea. Pending approvals sit at the top, above a live feed of receipts (agent, tool, decision,
duration) and a list of the agents seen in the last hour. `m` opens a memory browser with search, `v`
runs verify, `/` filters the feed, `?` shows the keys, `q` quits. Quitting the UI changes nothing for
running agents: their calls that need an approval wait for the timeout and are denied.

## State

`%LOCALAPPDATA%\portcullis\portcullis.db` on Windows and `$XDG_STATE_HOME/portcullis/portcullis.db`
elsewhere, never inside a synced folder. `modernc.org/sqlite` needs no cgo, so the Windows binary builds
without a C toolchain (ADR 0008); FTS5 support in it is confirmed when the repository is scaffolded. WAL
mode with a thirty-second busy timeout (ADR 0008). Migrations are embedded, numbered and forward only, and the first
process to open an older database migrates it inside `BEGIN IMMEDIATE`.

## Security

- No network listener. Agents talk to the gate over stdio, and the gate talks to downstream servers over
  stdio or outbound HTTP.
- Agent names are labels, and the database is writable by the user's account. Receipts are a record of
  what passed through the gate, not protection against the user's own account or anything running as it.
- Secrets reach downstream servers through `${env:...}` references and never appear in receipts. The
  redaction patterns are applied to arguments before they are stored.
- Memory is text written by one agent and read by another, which makes it a path for instructions planted
  by one agent to reach the next. Search and read results mark each entry with its author and as notes,
  not instructions, and a rule can put `memory_write` behind `ask`.
- Only the user's config sets rules. Nothing inside a repository can change them in v1.

## Evidence

- **Chain.** Tests edit, delete, reorder and insert rows, and `verify` names the first bad sequence
  number each time. Four real gate processes append 10,000 receipts at once, and the chain verifies with
  no gaps.
- **Rules.** Table tests over agent, tool and argument matching, and a test that a hidden tool is absent
  from the tool list and refused if called by name anyway.
- **Proof the checks can fail.** Redaction, rule evaluation and tool hiding each have a test that
  switches them off through a knob visible only to tests and asserts that the matching test then fails.
- **Approvals across processes.** A gate waits on `ask`, a second process approves, and the call goes
  through. The timeout path denies.
- **MCP behaviour.** The SDK's client drives the gate end to end: initialize, tool listing, calls,
  forwarded `list_changed`, and a downstream server killed in the middle of a session.
- **Hook.** Golden standard input and output for the Claude Code hook.
- **Overhead.** A benchmark calls an echo MCP server directly and through the gate with an allow rule,
  and reports p50, p99 and calls per second, compared with `benchstat` over ten runs, on Windows and
  Linux. Results live under `docs/benchmark-results/`, and the README states only numbers in those files.
- **Demo.** A recorded session: Claude Code writes a decision to memory, Codex finds it; Copilot asks for
  `github__create_issue` and the user approves it from the UI; `portcullis verify` ends clean.

## Repository layout

```
portcullis/
├── cmd/portcullis/               wiring: subcommands, config, signals
├── internal/config/              TOML decoding and validation
├── internal/rule/                matching and decisions
├── internal/gate/                the MCP server facing agents, tool listing, forwarding
├── internal/downstream/          MCP clients for stdio and HTTP servers, restarts
├── internal/memory/              memory tools over FTS5
├── internal/receipt/             appending, redaction, verify
├── internal/approval/            pending approvals and polling
├── internal/hook/                Claude Code hook, and others where they exist
├── internal/store/               SQLite, migrations
├── internal/tui/                 Bubble Tea UI
├── testdata/                     golden files, fuzz corpus, echo server
├── docs/adr/  docs/benchmark-results/  docs/design.md
├── .golangci.yml  go.mod  go.sum
└── README.md  CHANGELOG.md  CONTRIBUTING.md  SECURITY.md  LICENSE
```

## Toolchain and gates

- Go 1.27.1, verified on go.dev on 2026-09-26. `go.mod` carries `go 1.27` and `toolchain go1.27.1`.
- Dependencies: the MCP Go SDK v1.8.0 (Apache-2.0, with older parts still MIT while the project
  relicenses), `modernc.org/sqlite` v1.59.0 (BSD-3-Clause), `BurntSushi/toml` (MIT), Bubble Tea and Lip
  Gloss (MIT), `golang.org/x/sync` (BSD-3-Clause), goleak (MIT). Versions verified on the module proxy on
  2026-09-26 where given, and the rest when the repository is scaffolded.
- `gofumpt` and `golangci-lint` v2 as build gates, with at least `errcheck`, `govet`, `staticcheck`,
  `noctx`, `errorlint`, `gosec`, `exhaustive`, `sqlclosecheck` and `rowserrcheck`.
- `go mod tidy` leaves `go.mod` and `go.sum` unchanged, and `go test ./... -race -count=1` passes with
  `goleak` in every package that starts goroutines.
- A licence check reads the licence each module in the build graph ships and fails on anything outside
  the folder allow list.
- GitHub Actions runs every gate on `windows-latest` and `ubuntu-latest`, and builds on `macos-latest`.
  Release binaries are built with `-trimpath` and CGO off, and two builds must produce the same `sha256`.

## Decisions

| ADR | Decision |
|---|---|
| 0001 | No daemon: gate processes and the UI share state only through SQLite in WAL mode. |
| 0002 | Agents connect over stdio, and the agent name is a label from each CLI's config. |
| 0003 | Rules are first-match, and a plain `deny` hides the tool. |
| 0004 | Receipts are hash-chained, and results are kept as size and hash only. |
| 0005 | Approvals time out below the clients' tool timeouts, and a timeout denies. |
| 0006 | Built-in tools are gated through each CLI's pre-tool hook where one exists, with coverage stated per CLI. |
| 0007 | Memory search is FTS5, without embeddings. |
| 0008 | SQLite through `modernc.org/sqlite`, with no cgo. |
| 0009 | Downstream servers are supervised from the moment the gate starts. |

## Milestones

Each milestone gets its own implementation plan and ends with a green CI run.

1. **Gate core.** Config, store, memory tools, receipts and `verify`, allow and deny rules, the gate
   serving memory tools only. Exit: the SDK client suite and the four-process chain test pass on Windows
   and Linux.
2. **Downstream servers.** Stdio and HTTP clients, namespacing, hiding, redaction, `list_changed`,
   restarts. Exit: the GitHub MCP server behind the gate, used from a real Claude Code session.
3. **Approvals and the UI.** Exit: a real Codex session waits on `ask` and is approved from the UI.
4. **Built-in tools.** The Claude Code hook, and the other three CLIs checked for hooks, with adapters
   where they exist. Exit: the coverage table is filled from real sessions.
5. **Proof and release.** The overhead benchmark, the demo recording, README, ADRs finished,
   reproducible binaries, `v1.0.0`.

## Later

Not in v1, in rough order of value:

1. **Secret broker.** Downstream tokens live in the operating system's credential store (Windows
   Credential Manager, macOS Keychain, Secret Service), so neither the agents nor the config file ever
   hold them.
2. **Rule suggestions.** After the user has approved the same agent and tool five times, the UI offers
   to turn it into an allow rule. Repeated denials offer a deny rule.
3. **Budgets.** Call limits per agent and tool per hour, which stop an agent stuck in a loop.
4. **Handoffs.** `handoff_create` and `handoff_list` tools, so one agent can leave a task addressed to
   another.
5. **Approvals away from the desk.** A push notification with approve and deny actions. It needs a relay
   or a listener, so it comes with its own security design.
6. **Timeline.** Receipts grouped into sessions per agent, and export to OpenTelemetry.
7. **Better memory.** Local embeddings, expiry, and flagging notes from two agents that contradict
   each other.
8. **Skill suggestions.** A tool sequence that repeats across sessions offered as a draft skill.
9. **More of MCP.** Resources and prompts from downstream servers, and approvals shown inside the
   agent's own UI through elicitation.
10. **Project rules.** A rules file inside a repository that can only tighten the user's rules.

## Known limits and risks

- Only calls that pass through the gate are seen. An agent can still use built-in tools of a CLI without
  a hook, and other MCP servers configured in that CLI directly.
- Every agent session starts its own downstream servers. A server that keeps state in memory does not
  share it between agents.
- The chain shows edits made without rewriting everything after them. A full rewrite is caught only by
  comparing the head hash with a copy kept elsewhere.
- Approvals depend on the user watching. Unattended, `ask` means denied after the timeout.
- Go and the Codex, Copilot and Antigravity CLIs are not installed on the development machine yet.
  Milestone 1 needs only Go.
