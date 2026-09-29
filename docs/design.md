# Derbent design

Derbent is an agentic gate: the one MCP server that Claude Code, Codex, GitHub Copilot CLI and
Antigravity CLI all connect to, with the user's other MCP servers behind it. Every tool call passes
through it, so it can give all the agents one shared memory, write a receipt for each call, hold risky
calls until the user approves them, and decide which agent sees which tool.

It runs on the user's machine, on Windows as well as macOS and Linux, as one binary with no daemon.
A SQLite file is the only shared state.

This document is the contract for the build. Each decision that someone could reasonably have made
differently gets an ADR under `docs/adr/`, listed under Decisions.

## Goals

- One MCP entry in each agent CLI, instead of one entry per server per CLI.
- Memory that any agent can write and any agent can search, kept per project.
- A receipt for every call through the gate, hash-chained so that an edited or deleted receipt is found
  (the newest receipts only against a copy of the head hash, see Receipts).
- Rules that allow, deny or ask by agent, tool and argument, with the user approving from one terminal.
- Tools an agent may not use are not listed to that agent at all.
- Each CLI's built-in tools (shell, file edits) pass the same rules and get the same receipts, through
  its pre-tool hook.

## Non-goals

- Seeing calls that do not pass through it. Tools a CLI never shows its pre-tool hook, such as Codex's
  hosted web search, are outside the gate, and the README states per CLI what is covered.
- Model traffic. Derbent sits between agents and tools, not between agents and model providers.
- Several users or machines. One person, one machine, no network listener.
- Semantic search. Memory uses full-text search.
- A web UI.

## How it fits together

```
 Claude Code ─┐   stdio    ┌───────────────────────────┐   stdio / HTTP
 Codex ───────┤ ─────────> │ derbent mcp --agent X     │ ──────────────> the user's MCP servers
 Copilot CLI ─┤  one gate  │   rules, memory, receipts │
 Antigravity ─┘  process   └─────────────┬─────────────┘
                 per agent               │
                 session                 v
                              derbent.db (SQLite, WAL)
                                         ^
                                         │
                              derbent (terminal UI): approvals, receipts, memory
```

Each agent CLI starts `derbent mcp --agent <name>` as a stdio MCP server, exactly as it starts any
other server. That process reads the config, starts the downstream servers as MCP clients, lists their
tools together with Derbent's own memory and handoff tools, applies the rules to each call, forwards
allowed calls, and appends a receipt. Running `derbent` with no arguments in another terminal opens the
UI, which reads and writes the same database.

There is no daemon (ADR 0001). The gate processes and the UI coordinate only through SQLite in WAL mode,
so there is nothing to start first or keep alive, and a crash takes down one agent's gate, not everyone's.
The costs: each agent session starts its own copy of the downstream servers, as it would without
Derbent, and a pending approval is noticed by polling every 200 ms.

**Agent identity** is the `--agent` value written in each CLI's MCP config. It is a label, not
authentication: any process running as the user could claim any name (see Security).

**Project** is the git root of the directory the gate was started in, that directory itself when it is
not inside a git repository, or `--project <path>`. A linked worktree counts as the repository it was
added from, so agents working in separate worktrees of one repository share notes; a submodule is a
project of its own. The directory is resolved as git for Windows resolves it: symbolic links, and on
Windows junctions, `subst` drives and short names, lead to the directory they name. A directory that does
not exist yet is resolved through its nearest parent that does, so a link above it is still followed.

## Config

TOML in the user config directory (`%APPDATA%\derbent\config.toml` on Windows,
`$XDG_CONFIG_HOME/derbent/config.toml` or `~/.config/derbent/config.toml` on Linux,
`~/Library/Application Support/derbent/config.toml` on macOS), decoded strictly: an unknown key is an
error that names the key, and a syntax error names its line. `derbent config check` validates it, says
how many rules and budgets it holds, and starts each downstream server once to list its tools. When the
default file does not exist, every call is allowed: `derbent mcp` says so in one line on stderr when it
starts, `derbent config check` says so, and the hook, which runs on every call, says nothing. A
`--config` that names a file that does not exist is an error: `derbent mcp` and `derbent config check`
exit with it, and the hook denies the call and names the path, so a mistyped path never allows
everything.

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
tool   = "github__issue_read"
action = "allow"

[[rule]]
tool   = "github__*"
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

[[budget]]
agent = "*"
tool  = "native__Bash"
calls = 200
per   = "1h"
```

- Rules are tried in order and the first match wins. A rule matches on `agent` and `tool` as globs, and
  on `args` as globs over top-level string arguments. The last rule must have no condition.
- A tool is hidden from an agent's tool list when no call to it can be allowed: among the rules matching
  that agent and tool, a `deny` with no `args` condition comes before any `allow` (ADR 0003). A `deny`
  with an `args` condition refuses the matching calls and leaves the rest to later rules.
- `${env:NAME}` is resolved when the gate starts. Resolved values never reach receipts or logs. A `url`
  cannot hold one, because HTTP errors quote the url to stderr and to the agent, and neither can a
  `command`, which shows up in process listings and start errors; a secret goes in `headers` for a url
  server and in `env` for a command server.
- A `url` server must use `https`, except on `localhost`, `127.0.0.1` or `::1`. `env` belongs to command
  servers and `headers` to url servers.
- Every value that came from `${env:...}` in `env` or `headers` and is at least eight characters long is
  masked in stored arguments, as well as whatever the `redact` patterns match. Masking works on each
  string value, so the stored arguments stay valid JSON.
- An `args` condition that meets a value it cannot read (not a string) matches a `deny` or an `ask` and
  never an `allow` (ADR 0003).
- `[approvals] timeout` is a Go duration of at least one second, 50 seconds when it is not set (ADR 0005).
- `[[budget]]` tables are optional (see Budgets), and a server's table can say `pin = false` (see Tool
  pins).

## Tools

- Downstream tools are listed as `<server>__<tool>`, so two servers can never collide. Server names
  are lower-case letters, digits and dashes, so the name splits one way only. A gate tool name must be
  1 to 64 letters, digits, underscores or dashes; a tool that does not fit, whose input schema is not
  an object, or whose definition the MCP SDK refuses to serve, such as an `x-mcp-header` on a property
  that is not a string, integer or boolean, is left out with a warning on stderr and marked by
  `derbent config check`, never truncated. The SDK panics on such a definition, so the gate tries it on
  a throwaway server first, and one bad tool never takes down the gate.
- A downstream `notifications/tools/list_changed` is passed on to the agent after the rules are applied
  to the new list.
- A downstream server is supervised from the moment the gate starts (ADR 0009): it is started again
  after it stops, with a backoff from one second to one minute, and calls to its tools while it is down
  are tool errors. The gate keeps serving everything else. The agent's initialize is answered at once;
  its tool listings and calls wait up to five seconds for servers still starting, and a slower server
  appears later through `list_changed`.
- Arguments that are not a JSON object are refused before any rule is read, since `args` conditions
  read named arguments and could not see into anything else.
- A call to a name the gate does not serve to that agent is refused before any rule is read, so an agent
  cannot fill the approval queue with calls that would fail anyway. A tool the rules hide is still
  refused by the rule that hid it.
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

Handoff tools (ADR 0018):

| Tool | Input | Output |
|---|---|---|
| `handoff_create` | to (an agent label or `*`), title, body (up to 16 KiB), tags | id |
| `handoff_list` | state (`open` unless given, `taken`, `done` or `all`), `mine`, `all_projects` | id, project, from, to, title, state, time |
| `handoff_take` | id | the whole handoff |
| `handoff_done` | id, optional note (up to 4 KiB) | id, state |

A handoff is a task one agent leaves for another. The four tools are served like the memory tools and
pass the rules like any tool. A handoff belongs to a project and records the agents and gate sessions
that created and took it, as a memory entry does. `handoff_list` lists the current project's handoffs
addressed to the calling agent or to `*`, or with `mine` the ones it created, and with `all_projects`
every project's. A handoff is open until an agent it is addressed to takes it, then taken by that agent,
then done when that agent finishes it, with its note. Taking one that is not open or not addressed to the
agent, and finishing one the agent did not take, is a tool error; of two agents taking one at once, one
gets it. Nothing moves a handoff back: there is no reassignment or release in v1. `to` must be an agent
label, the form `--agent` takes, or `*`, so a handoff never waits for an agent that cannot exist. What the
tools return is marked as tasks written by agents, information and not instructions.
`derbent handoffs [--db path] [--json] [--all]` lists every project's open handoffs, and every state with
`--all`, newest first, escaped, from the database opened read-only.

## Receipts

Each call through the gate appends one row: sequence number, time, project, agent, gate session, tool,
arguments after redaction, SHA-256 of the arguments before redaction, the decision and what made it
(`rule:<n>` for the user's rules, `project:<n>` for a project's, `budget:<n>`, `pin`, `gate` for the gate
itself, or an approval, see Approvals), the outcome, the size and SHA-256 of the
result, the duration, the previous receipt's hash, and this receipt's hash.

- The hash is SHA-256 over the previous hash and the stored bytes of the row's fields, each field
  prefixed with its length so that moving bytes from one field to the next changes the hash. Appends
  run in a `BEGIN IMMEDIATE` transaction that reads the current head, so gate processes appending at
  the same moment are serialised by SQLite and the chain has no forks.
- Results are stored as size and hash only. What a tool returned can be large or private, and the hash
  is enough to show later that a given result was the one returned. It is taken over a form of the
  result that can be rebuilt from what the agent received (ADR 0004).
- `derbent verify`, `derbent receipts` and `derbent pending` open the database file read-only, so the
  file and its `-wal` stay byte for byte as they were, even when the `-wal` still holds receipts that
  are not in the file yet. SQLite may add its `-shm` index beside them, and beside a copy that has no
  `-wal` an empty `-wal` as well; both stay after the command exits. A file with tables but no Derbent
  schema version is another program's: every command given it refuses it and leaves it as it was,
  `derbent mcp` and `derbent gate` included, which check it read-only before they write anything.
- `derbent verify` opens the database read-only, never creates or migrates it, walks the chain and
  names the first sequence number whose hash, predecessor or position is wrong. It prints the head
  hash, and checks an export without the database with `--file` (see below). Someone able to write the
  database could rewrite the whole chain consistently, or delete the newest receipts and leave a shorter
  chain that still verifies, and keeping a copy of the head hash elsewhere is what catches both
  (ADR 0004).
- `derbent receipts` opens the database read-only and lists receipts filtered by agent, tool glob,
  project and time, the newest 50 unless `--limit` says otherwise, every one with `--limit 0`, as a table
  or as JSON lines. Stored text can come from an agent, so control characters, bidirectional overrides
  and invisible characters are escaped in both, by the same function the UI uses (`internal/visible`).
- A JSON line of `derbent receipts --json` carries every field the hash covers, exactly as stored: `seq`,
  `at` (the stored RFC 3339 text with its fraction, which is what the hash reads), `project`, `agent`,
  `session`, `tool`, `args`, `args_sha256`, `decision`, `decided_by`, `outcome`, `result_size`,
  `result_sha256`, `duration_ms`, `prev_hash` and `hash` (ADR 0017). JSON escaping is as it was and
  decodes back to the stored text.
- `derbent verify --file <path>`, or `-` for standard input, checks such an export without the database.
  It recomputes each line's hash from its fields with the function the database check uses, and checks
  that each line's `prev_hash` is the hash of the line before it when their sequence numbers follow on. A
  gap in the numbers, which a filtered export has, starts a new run and is reported, not failed; a number
  that does not rise, a receipt 1 whose `prev_hash` is not the genesis hash, and a later receipt whose
  `prev_hash` is, fail. It prints how many lines it checked, the runs, the gaps, the anchor (the first
  line's `prev_hash`) and the head (the last line's `hash`), and `--head <hash>` checks the head against a
  hash the user kept. With a kept head and more than one run it says that only the last run is tied to
  that head, and without `--head` it says that nothing ties the export to the database. The first line
  that fails is named, and the exit status is 1. Lines of any length are read, with LF or CRLF endings, in
  UTF-8 with or without a byte order mark or in UTF-16 with one, as Windows PowerShell 5.1 writes a
  redirected command's output. A line must be one JSON object that holds each field listed above once,
  under its exact name, none of them null, and nothing else: a field the hash does not cover, a field
  given twice, left out or null, and text after the object are refused. Blank lines are skipped, and a
  file with no receipt lines fails.
- A line whose hash matches its fields shows that it was not changed only when its run ends at a kept
  head. The hash takes no key, so anyone holding an export can edit a line and compute its hash again: a
  run cut off by a gap, and any export checked without a kept head, shows only that its lines agree with
  each other. An export also cannot show that nothing was left out: a line removed from the middle looks
  like a filter's gap, lines cut from the start look like an export that starts later, and lines cut from
  the end go unseen without a kept head.

## Approvals

When a rule says `ask`, the gate writes a pending approval and polls for a decision every 200 ms. In
the UI the pending call shows at the top with the agent, the tool, the time left, the number of the
rule that asked, the project and the redacted arguments, and the terminal bell rings. A tool behind `ask` stays in the agent's tool list.

- `a` approves once, `d` denies, and `A` approves the tool's calls that the same rule asks about under
  the same rules above it, for the rest of that agent's gate session: one `derbent mcp` process for an
  MCP call, the CLI's own session for a hook call (see Built-in tools). Under first match, which calls a
  rule catches depends on the rules above it, so the grant is keyed on a fingerprint of the rule and
  every rule above it.
  Editing or reordering the config never widens a grant. The hook reads the config on every call, so
  there a change to the granted rule or to any rule above it asks again while the change stands, and
  the grant applies again if the change is undone; a change below it keeps the grant. A `derbent mcp`
  gate reads the config only when it starts, so an edit does not affect its grants (ADR 0011). No
  grant covers a call that its rule matched on an argument the rule could not read, such as a `command`
  sent as an array, so each such call asks. `A`
  takes a second press within five seconds, so one press can never grant the session, and the question
  and the status after it name the tool, the rule's number and the agent. An `a` typed with Caps Lock on
  arrives as `a` with Caps Lock from a terminal that reports modifiers, and the UI takes it as `a`; a
  terminal that reports none sends `A`, which still needs the second press. Nothing the UI does
  changes the config file.
- `derbent approve [--session] <id>` and `derbent deny <id>` do the same from any shell, by the id the
  UI shows, written `12` or `#12`. An approval for a call matched on an argument its rule could not
  read, or one written by a gate from before grants followed the rule, still running after the upgrade,
  names no rule and grants nothing: `A` in the UI says it approves the call once, and
  `derbent approve --session` refuses it and says to approve it once. `derbent pending`
  lists the waiting calls with the rule that asked, the project and their whole arguments, read-only, as
  rows or JSON lines, so they can be read before deciding. `derbent approve` and `derbent deny` say what
  they decided, such as `approved #12 once: native__Bash for claude`. The deciding commands, and the UI given `--db`, refuse a database path that does
  not exist instead of creating one.
- If no decision arrives within `approvals.timeout`, the call is denied with a tool error that says no
  approval came in time and tells the agent to try again and ask the user to approve it in the UI while
  it waits. The default of 50 seconds sits ten below Codex's 60, the shortest documented default tool
  timeout among the four CLIs, so the agent sees a clear denial instead of a transport timeout
  (ADR 0005).
- Approvals live in the database with a deadline. A decision is refused once the call is no longer
  waiting: decided, timed out, given up by its agent, or past its deadline, which is how a call left
  behind by a gate that stopped ends.
- The receipt of an asked call records the final decision and names the approval behind it:
  `user:<id>` for the user's decision, `grant:<id>` for a call covered by an earlier `A`, `timeout:<id>`
  when no decision came, and `withdrawn:<id>` when the agent gave up or the gate was told to stop
  (SIGINT or SIGTERM), so that no call can be approved while its gate shuts down. A call that reaches a
  gate already told to stop writes no approval and is refused with `gate`, since it never waited. A
  gate that cannot ask at all refuses the call and keeps the rule's `rule:<n>`.
- The approval queue and the UI hold arguments only after redaction.
- `derbent grants` lists every session grant: the approval id (as `#12`), the agent, the gate or CLI
  session, the tool, the rule that asked (from the approval) and when it was granted, as rows or JSON
  lines, escaped like `derbent receipts`. `derbent revoke <id>`, written `12` or `#12`, deletes that grant
  and says what it revoked; `derbent revoke --all` deletes every grant and says how many. An id with no
  grant is an error that names it. Both paths read the grants table on every call, so a revoked grant
  stops covering calls at once.

## Rule suggestions

`derbent suggest [--db path] [--min N] [--json]` reads the calls the user approved or denied and prints
TOML rule snippets with the counts behind them (ADR 0019). Derbent never edits the config file: the user
copies what they want.

- Calls are grouped by agent and tool. A group with at least N approvals (5 unless `--min` says) and no
  denials gets an `allow`; one with at least N denials and no approvals gets a `deny`. Calls that timed
  out or were withdrawn were never answered and do not count, and calls a project's rules asked about are
  left out, since a rule in the user's config cannot loosen a project's.
- A tool whose calls carry a `command` argument, or `CommandLine` as Antigravity CLI's `run_command`
  does, is a shell, and every one of its answered calls must carry it as a string. Its snippet adds
  `args = { <key> = "<prefix>*" }`, with the key its calls carry (`command` or `CommandLine`), where the
  prefix is the longest start its commands share, cut before any `*` or `?`, which a pattern reads as
  wildcards, and back to where a word ends. When nothing but white space is left there is no suggestion
  for that tool, so Derbent never suggests allowing a whole shell. A tool or agent name that holds a `*`
  or `?` gets none either.
- Each snippet says where to put it: above the lowest-numbered rule that asked about those calls, from
  the approvals, so first match reaches it.
- Strings in a snippet are TOML basic strings with quotes, backslashes, control characters and invisible
  or reordering characters escaped, so they read back as they were and cannot end early. The comment above
  each snippet is escaped like any text for a terminal. `--json` prints one line per suggestion.
- After the user approves a call in the UI, when that agent's answered calls to that tool now make an
  `allow`, the status line says "<agent>'s <tool> approved <n> times: run derbent suggest for a rule".

## Budgets

A budget limits how many calls one agent label may have let through to the tools its `tool` glob
matches, within a sliding window `per`, a Go duration from one minute to 24 hours (ADR 0012). `agent` and
`tool` are globs with the rule syntax, and `calls` is at least 1.

- Every budget whose `agent` and `tool` match the call applies; unlike rules, there is no first match. A
  call counts against a budget when its receipt says it was let through (`decision` = `allow`, whether a
  rule, a grant or the user let it through) and its time is inside the window. Counting reads the
  receipts, so every gate process and hook that uses the same database shares it.
- The rules decide first, and a `deny` stays a deny. Otherwise, when a matching budget is used up, the
  call is refused without asking the user: `decided_by` is `budget:<n>`, the budget's 1-based position in
  the config, the outcome is `refused`, and the agent reads "derbent: budget <n> in the config is used
  up: <calls> calls to <tool glob> per <per> for <agent>; the next call is possible in about <d>", with
  "call" for a limit of one. When more than one matching budget is used up, it names the one with the
  longest wait, since the call is refused until every one of them has room again.
- Asked calls that the user denies, or that time out, are not let through and do not count, so a loop
  of them still reaches the user. Once the calls let through use a budget up, further calls are refused
  without asking.
- Counting and appending are not one transaction across processes, so calls made at the same moment can
  pass a budget by at most the number of calls in flight at once.
- Receipts are indexed on agent and time, so the count reads only that agent's window. A budget that
  cannot be counted refuses the call, with `decided_by` = `gate`.

## Tool pins

A downstream server can change a tool's description between two starts, and a description is text the
agent reads and follows. The gate pins each downstream tool (ADR 0013).

- A pin is the SHA-256 of the tool's definition as the gate receives it: name, title, description, input
  schema, output schema and annotations, encoded as JSON with sorted object keys and no insignificant
  whitespace. Every key is present, the five annotation keys included, which Derbent writes itself: the
  SDK's own encoding of them changes with its version and with `MCPGODEBUG=hintomitempty`. Pins live in
  the database, keyed on server and tool name, and every gate process shares them.
- The first time a gate sees a tool, it pins it and serves it (trust on first use). Pins are taken for
  every tool the gate could serve, including tools the rules hide from one agent, since another agent may
  see them. A tool the rules hide from this agent is refused by the rule that hides it whether or not it
  changed, as before pins, so a refusal never tells the agent that the tool exists.
- A tool that an update adds to a server already pinned is new too, so it is pinned and served at once,
  and mid-session through `list_changed`. Its description can tell the agent what to do as well as a
  changed one can. Pins catch a change to a tool already seen; the rules are the guard against a new
  one. For a server whose updates are not reviewed, name the tools to allow or ask about, such as
  `github__create_issue`, and deny `github__*` after them: a tool that no call can get through is left
  out of the list, so its description never reaches the agent. An `allow` or `ask` on `github__*` lists
  every new tool as soon as it appears.
- When a tool's definition differs from its pin, the gate leaves the tool out of the agent's tool list, so
  the agent never reads the changed text, keeps the new definition next to the pin, and writes one warning
  line to stderr. A call to it from an agent holding an older list is refused before any rule is read:
  `decided_by` is `pin`, the outcome is `refused`, and the agent reads "derbent: <tool> changed since it
  was pinned; the user can review it with derbent pins". A server that sends the pinned definition again
  gets the tool served again, and the recorded change is dropped. A pin check that fails withholds the
  server's tools until a later check succeeds.
- `derbent pins` lists the pins with their state, `pinned` or `changed`, when each was pinned and when the
  change was seen, as rows or JSON lines. `derbent pins show <server>__<tool>` prints the pinned and the
  new definition as indented JSON with their hashes, the lines that differ and the command that accepts
  that change, with the `--db` it was given, all escaped. The `--db` is written so it pastes into bash,
  PowerShell and cmd alike: as it is when it holds only letters, digits and `_ . / : -`, in double quotes
  when it also holds spaces or single backslashes, and as `<path>` to fill in, with a line that says so,
  when it holds any of ``" $ ` % !``, a control character, two backslashes in a row or a trailing one,
  which one of the three would run, expand or change inside double quotes. The lines that differ are
  lined up on a longest common subsequence, so a line that only moved, such as a description swapped from one property to another,
  shows where it left and where it arrived; definitions too long to line up within 16 MiB are to be
  compared in full. `derbent pins accept <server>__<tool> <sha256>` takes the new definition's whole
  hash, all 64 hex digits as `pins show` prints them, makes that definition the pin and prints its hash.
  A prefix is refused, since a hostile server can find two definitions whose hashes share a short one.
  When the change on record has another hash, because the server changed the tool again after the
  review, it refuses and says to run `derbent pins show` again, so what is accepted is always what was
  read.
- A running gate looks at its withheld tools every two seconds and serves one again once its definition
  is the pin, which the agent learns through `list_changed`. For a changed tool the watcher only reads
  the pin, and writes its own definition as the change only when none is recorded, after the user
  accepted another, so it never replaces the recorded change and takes no write lock. For a tool withheld
  because its first pin check failed, the watcher checks again, pinning the tool if it has no pin, and
  likewise records its definition only when no change is recorded and never drops one. A gate that lists
  the server's tools again, at its start, on a reconnect or after `list_changed`, does record its
  definition, and can replace a change the user is reviewing; the accept of the reviewed hash is then
  refused.
- `pin = false` in a server's table turns pinning off for a server whose descriptions change on every
  start: its tools are served as they come and never pinned.
- A tool that disappears keeps its pin, so it cannot come back changed without notice.
- Only downstream tools are pinned. The memory and handoff tools are Derbent's own, and the CLIs'
  built-in tools have no definition the gate receives.
- The UI shows one line above the waiting calls while any tool is changed. `derbent config check` shows
  each tool's pin state (`new`, `pinned`, `changed`) and pins nothing. It, `derbent pins` and `pins show`
  read the database without migrating it, so a database from before the pins table holds no pins for
  them.

## Project rules

A repository can make Derbent stricter for itself, never looser (ADR 0014). A `.derbent.toml` file at the
root of the checkout the agent works in may hold `[[rule]]` tables and nothing else. That root is the
nearest directory holding `.git` at or above the directory the project is found from (`--project`, the
call's directory on the hook, or the working directory), which for a linked worktree is the worktree's
own root, or that directory itself outside git. The file is looked up there in the path's own case, not
under the lower-cased key. A linked worktree reads its own file, so a branch that adds or tightens it is
enforced in its worktree, and a worktree of a bare repository has a file to read; its calls still share
the main checkout's project key, memory and receipts. Rules only tighten, so reading the worktree's own
file never loosens anything, and a grant is keyed on what the rules say, not on where the file is.

- Project rules use the rule syntax and are tried in order, first match wins, with no final catch-all:
  when none matches, the project adds nothing.
- The call's action is the stricter of the user's decision and the project's, in the order deny, ask,
  allow. `decided_by` names the rule that set it: `rule:<n>` for the user's rules, `project:<n>` for the
  project file; on a tie it names the user's rule.
- A file with any other key, or a rule the rule syntax refuses, is invalid, and every call in that project
  is denied on both paths, with a reason that names the file and the error and `decided_by` = `gate`. A
  missing file changes nothing. Keys count only as written: `ACTION`, `Action` or `[[Rule]]` is another
  key, since the TOML decoder would fill a field from any of them and keep one of two spellings at random.
  Text from the file in the reason is escaped, since the reason reaches the agent and the CLI.
- Only a regular file of at most 64 KiB is read, through a reader that stops one byte past the limit. A
  symbolic link, a directory, a FIFO, a device or a larger file is refused as invalid, with a reason that
  says which, since reading it could block the hook or fill its memory. The open does not wait for a
  FIFO's writer, and what was opened must be the file that was checked, so a FIFO, a device or a
  symbolic link swapped in after the check is refused too. The `.git` file of a linked worktree, and the
  `commondir` file it leads to, are read under the same limits, and one that fails them is taken as not
  a linked worktree.
- The file is read for each call's project and kept by path, size and modification time, so an edit takes
  effect on the next call on both paths.
- Project rules decide calls and never change tool lists: a tool the project denies stays listed, and its
  calls are refused.
- A session grant for a call that a project rule sent to the user is keyed on both fingerprints: the
  user's rules up to the rule that decided, and the project's rules up to the rule that asked. An edit to
  either list at or above those rules stops the grant from applying, as ADR 0011 says for the user's rules.
- The approval queue records which list asked, so the UI, `derbent pending`, `derbent approve` and
  `derbent grants` say `project rule <n>` for a project rule. `derbent pending` and `derbent grants` read
  the database without migrating it, so on a database from before migration 0006 they name every rule as
  one of the user's.
- On the hook path, a call that only the project's rules asked about, which the user's rules allow, gets
  no decision once the user approves it, now or through an earlier `A`: that is what the user's rules alone
  give, so the CLI's own permission settings still apply. An explicit `allow` would skip them, which is
  looser than the user's rules without the project.
- An agent that can edit the repository can edit or delete `.derbent.toml`. That only takes the project
  back to the user's own rules, never below them.

## Explain

`derbent explain --agent <label> --tool <name> [--args <json>] [--project <dir>] [--config path]
[--db path] [--session <id>] [--json]` shows how Derbent would decide one call, and changes nothing. It
loads the config as the hook does, starts no servers, and opens the database read-only, only when it
exists.

- It prints each of the user's rules in order with why it matches or not: the agent glob, the tool glob,
  and, when both match, each `args` condition in name order with the value it read, or that the value
  was missing or not a string. It stops at the first match and says the rules below it are not read.
  Then it prints the project's rules the same way, from the `.derbent.toml` of the checkout `--project`,
  or the working directory, is in; then every budget that applies, with its count in the window; then,
  for a downstream tool, its pin as the database records it; then, for a call that asks and with
  `--session`, whether a session grant covers it; and last the verdict and what would decide it:
  `rule:<n>`, `project:<n>`, `budget:<n>`, `pin`, `gate` or `grant:<id>`. For a deny by a rule or a
  budget, the verdict carries the text the gate gives the agent.
- The gate refuses some calls before it reads any rule: a name it does not serve, a tool its pin
  withholds, arguments that are not a JSON object, and any call in a project whose rules file cannot be
  read. For those explain reads no rule either: it says the rules are not read, and that the grant is not
  reached. A call the rules send to the user that a used-up budget refuses first gets the same grant line.
- A `native__` name is explained as the hook decides it. Any other name is explained as the MCP gate
  decides it, which refuses a name it does not serve before any rule is read. That includes a server's
  tool under a name agents cannot be offered, one that is not 1 to 64 letters, digits, underscores and
  dashes, which the gate leaves out. Explain says what it cannot know: whether a downstream server offers
  the tool, which definition it sends now, and whether a running gate could check its pin.
- Without the database, budgets, pins and grants are reported as not checked. Explain never migrates, so
  in a database from before the grants were keyed on the rule (schema version 3) or before pins (version
  5), the grant or the pin is reported as not checked until the next gate to start migrates it.
- `rule.Set` explains a call with the same matching `Decide` uses, and explain combines the user's and the
  project's decisions with the function the gate uses, `gate.Judge`, so explain and the gate cannot
  disagree on what the rules decide. It counts budgets from the receipts with the gate's own read, tells
  a downstream tool's name by the same check the hook uses, and words a rule's deny with the gate's own
  text. Every case in the rule tests checks that `Explain` decides as `Decide` does.
- Text from agents and the config is escaped. `--json` prints one object, in which `config` is always a
  path, `config_missing` says the default file does not exist, and a list with nothing in it is `[]`,
  never `null`. An `--args` that is not JSON is an error that shows the text as it arrived, since Windows
  PowerShell 5.1 drops the double quotes inside an argument unless each is written as `\"`.

## Built-in tools

`derbent gate --agent claude` is installed as a Claude Code `PreToolUse` hook matching every tool. It
reads the hook's JSON from standard input, treats the call as tool `native__<tool_name>` with the tool's
input as arguments, applies the same rules, waits for an approval when a rule says `ask`, and appends a
receipt. Its gate session is the hook input's `session_id`, so `A` covers the tool's calls that the same
rule asks about under the same rules above it, for the rest of that Claude Code session.

- Calls to Derbent's own tools (see below) get no decision and no receipt from the hook.
  The gate already decides and records them, and would otherwise ask for and record each one twice.
- A call allowed by a rule gets no decision from the hook, so Claude Code's own permission settings
  still apply on top.
- A call the user approved, now or through an earlier `A`, gets `allow`, unless only the project's rules
  asked about it and the user's rules allow it: then it gets no decision (see Project rules).
- A refused, denied or timed-out call gets `deny` with the reason, which Claude Code shows to the model.
- The receipt's outcome is `gated` for a call that may run and `refused` for one that may not, since the
  hook runs before the tool does.

Codex, GitHub Copilot CLI and Antigravity CLI have the same kind of hook, and `derbent gate` speaks
each one's protocol (`--cli codex|copilot|antigravity`, which defaults to the `--agent` value). The
README's quick start shows how each hook is installed with a timeout above `approvals.timeout`, and its
coverage table says, per CLI, which tools the hook sees and which name each CLI gives Derbent's own
tools (ADR 0006). One process runs per tool call, and for every CLI:

- A UTF-8 byte order mark at the start of the hook input is skipped. A .NET program that writes the
  input through `Process.StandardInput` sends one when the console input encoding is UTF-8, and the hook
  would otherwise deny every call as unreadable.

- A call is the gate's own only when three things hold. Its name starts with the CLI's prefix for the
  MCP entry named by `--server` (default `derbent`): `mcp__derbent__` in Claude Code and Codex,
  `derbent-` in Copilot CLI, and `mcp_derbent_` for Antigravity CLI, which is assumed until a real
  session shows it. The rest of the name is a tool the gate serves: a memory or handoff tool, or
  `<server>__<tool>` for a server in the config, since the hook starts no servers and cannot know their
  exact tools. And when the hook input names the MCP server, as Claude Code 2.1.274 and later do in
  `mcp_server.name`, that server is the `--server` entry. Any other call, including one to an MCP server
  configured in the CLI directly, is decided as `native__` plus the CLI's name for it, such as
  `native__mcp__github__get_me`.
- The gate session is the CLI's session id (Claude Code, Codex, Copilot CLI) or conversation id
  (Antigravity CLI). `A` on a hook tool covers later calls of that `native__` tool that the same rule
  asks about under the same rules above it, in the same CLI session. Every shell command is one tool,
  such as `native__Bash`, so an `A` on a `git push` does not let through a `terraform apply` that
  another rule asks about (ADR 0011).
  It cannot cover an MCP tool: the MCP gate's session is one `derbent mcp` process with a random id, and
  its tools have other names.
- The project is `--project`, else the git root of the directory the CLI reports, else that of the
  hook's working directory (Antigravity CLI can report no workspace).
- The hook loads the config with every check `derbent mcp` makes, except that a `${env:NAME}` in a
  server's `env` or `headers` whose variable is not set in the hook's environment stays as written
  instead of failing: the hook starts no servers, and a CLI can hand a secret only to its MCP entry. Such
  a value is no secret to mask, so hook receipts mask only the secrets set in the hook's own environment
  and whatever the `redact` patterns match.
- It fails closed. Once the protocol is known, any failure answers `deny` with the reason: an invalid
  agent name, input it cannot use or larger than 16 MiB, a config that does not load (which denies
  Derbent's own tools too, since the config says which they are), a project or database it cannot open,
  or a receipt it cannot write. An unknown `--cli`, bad flags, or an answer that cannot be written exit
  with status 2, which Claude Code and Codex take as a block.
- The first hook call on a machine creates the database. Opening a current database reads the schema
  version and takes no write lock, so a hook call does not queue behind other gates' appends before it
  decides.

## Setup

Setup takes two entries per CLI in two files, and several mistakes fail silently or far from their
cause. `derbent init` writes them, `derbent doctor` checks them, and a preset replaces the blank page of
a first config.

- **Presets.** `watch`, `balanced` and `strict` are commented TOML files embedded in the binary. `watch`
  is one rule that allows and records every call, and names no tool. `balanced` allows reading, and asks
  about pushing, `--force`, discarding work with `git reset --hard` or `git checkout --`, deleting files,
  running a downloaded script, the terraform, tofu, pulumi, kubectl and helm subcommands its
  infrastructure section lists, writing a `.env` file with a file tool or a shell redirect, `~/.ssh` in a
  file path or a shell command, text typed into a running Copilot CLI shell, and every tool of a GitHub
  server behind Derbent but twelve known reads. It names each CLI's read-only, shell and file tools and
  the argument keys they use (see Built-in tools), and its comments say what its patterns catch, what
  they miss and where they catch too much. Its shell rules match text, so a command in a form they do not
  list, such as `"git" push`, gets through; the preset says so at the top. `strict` allows Derbent's
  memory and handoff tools, each CLI's built-in tools that only read, keep notes and plans or ask the
  user, named per CLI, and the same twelve GitHub reads, and asks about everything else, every shell
  command included, so it needs no argument keys. A preset is a file written once, not a mode: Derbent
  never changes it after writing it, and the user owns it (ADR 0016). `derbent init --preset <name>`
  writes it to the default config path only when no file is there, and `--print` prints it and writes
  nothing.
- **init.** `derbent init [--cli <list>] [--preset <name>] [--yes] [--dry-run]` sets up each CLI it finds
  (its command on `PATH`, or for Antigravity CLI the directory `~/.gemini/config`), or the ones `--cli`
  names. For each it adds Derbent's MCP entry through the CLI's own `mcp add` command where there is one
  (Claude Code at user scope, Codex, Copilot CLI) and by editing `~/.gemini/config/mcp_config.json` for
  Antigravity CLI, and the pre-tool hook by editing `~/.claude/settings.json`, `~/.codex/config.toml`,
  `~/.copilot/hooks/derbent.json` or `~/.gemini/config/hooks.json`, with `--agent` set to the CLI's name
  in both and a timeout of 120 seconds where the CLI's default is 30. `CLAUDE_CONFIG_DIR`, `CODEX_HOME`
  and `COPILOT_HOME` move these files as they move the CLIs' own (ADR 0015). A CLI named with `--cli`
  whose command is not on `PATH` still gets its hook, and init prints its `mcp add` for the user to run.
- The binary is written as the absolute path of the running `derbent`, symbolic links resolved, with
  forward slashes: in exec form for Claude Code, whose hook shell is Git Bash or PowerShell depending on
  what is installed; as `& "<path>"` in Copilot CLI's PowerShell field; and elsewhere in double quotes
  when it holds a character a shell reads. A path that holds `"`, `$`, `` ` ``, `%`, `!`, `&`, `^`, `|`,
  `<`, `>` or a control character is refused: some shell reads the first five even inside double quotes,
  and a CLI installed as a `.cmd` shim, as npm installs them, runs through cmd.exe, which reads `&`, `^`,
  `|`, `<` and `>` in an argument Go quotes only when it holds a space.
- Claude Code before 2.1.139 starts an exec-form hook without its args, so the hook would decide
  nothing. init reads `claude --version` and skips an older Claude Code, a pre-release of 2.1.139
  included, saying to upgrade it; when the version cannot be read or `claude` is not on `PATH`, it goes
  on with a note. For Codex it says that the new hook runs only after the user trusts it in `/hooks`.
- init never replaces or removes an entry: an MCP entry named `derbent`, or a hook that runs
  `derbent gate` in any form, leaves that part as it is. It looks for such a hook in every user-level file
  the CLI reads hooks from, though it writes only the one above: Codex's `hooks.json` beside
  `config.toml`, and Antigravity CLI's `~/.gemini/antigravity-cli/settings.json` beside `hooks.json`. A
  file it only looks in may hold comments the CLI accepts; when it does not parse as JSON, init goes on
  and notes that it could not check it. A gate hook whose matcher covers only some tools, such as `Bash`,
  gates only those: init adds no second hook beside it, changes nothing for that CLI, and says to widen
  the matcher.
- It prints every change (the file, the copy it will make, the exact text added or the command it will
  run), escaped, and asks once unless `--yes`; `--dry-run` prints and changes nothing. A file that exists
  is copied to `<file>.derbent-backup-<UTC time>` before its first change. When a change fails, init names
  the copy of its file and the changes already made, each with its copy, so the user can finish or undo
  them. JSON is decoded, extended and written back with two-space indentation, so key order may change.
  Codex's TOML is never re-encoded: the new tables are appended as text once both the file and the
  result parse. A file that does not parse is left as it is, and its CLI fails with the file and the
  error. New files get 0600 and new directories 0700. init starts no session and talks to no model; only
  the CLIs' own `mcp add` commands do whatever they do.
- **doctor.** `derbent doctor [--cli <list>] [--config path]` reads the same files and Derbent's own. Per
  CLI it checks every hook that runs `derbent gate`, in each user-level file the CLI reads hooks from:
  that there is one, since without it the built-in tools are not gated, and only one, since two decide
  every call twice, and that its matcher covers every tool. It checks that the MCP entry the hook skips
  (`derbent`, or the hook's `--server`) runs `derbent mcp`, for Claude Code the current directory's
  local-scope entry over the user one, as Claude Code picks it, and that no entry under another name
  runs `derbent mcp`, since the hook would decide and record its calls to Derbent's own tools twice. It
  checks that each program the entry and the hooks start is on `PATH` or is a regular file, executable
  outside Windows; that the hook's timeout, as set or the CLI's default (600 seconds for Claude Code and
  Codex, 30 for Copilot CLI and Antigravity CLI), is above `[approvals] timeout` in the config the hook
  loads, its own `--config` or else the default config, whatever doctor's `--config` names; that both use
  the same `--agent`; that `--cli` is given when the agent is not the CLI's name; for a Claude Code hook
  in exec form, that `claude --version` is 2.1.139 or later; and for Codex, that every `${env:NAME}` in
  the config its MCP entry loads, chosen the same way, is in the entry's `env_vars` or `env`.
- For Derbent, doctor checks that its config, the default one or the one its `--config` names, exists and
  loads as the hook loads it. With `--config`, the default config is checked as well once a hook, or
  Codex's MCP entry, without a `--config` of its own is found to load it. It checks that the database
  can be written and is one Derbent can use: `store.Check` makes the checks the gate makes when it opens
  the file, so another program's file or a newer schema is a problem. A database with a `-wal` beside it
  is live, or was left so, and is opened read-only, reading the `-wal`, so pages a writer has not yet
  checkpointed are not taken for corruption; any other is opened immutable, which takes no lock and adds
  no file. Where the database does not exist yet, doctor creates one file in the nearest directory on its
  path and removes it at once, to check that the database can be created there; that is the only write
  it makes. It times three starts of each hook binary with `version` and reports the middle one as a
  note, with what it costs when it is over 500 ms; a start that fails or takes over 30 seconds is a
  problem. What it cannot check is a note too: the Codex trust step, a file it only looks in that does
  not parse, a Claude Code version it cannot read or a `claude` not on `PATH`. Each line is `ok`,
  `problem` with its fix, or `note`, escaped, and the exit status is 1 when there is a problem.

## Terminal UI

Bubble Tea. Calls waiting for the user sit at the top, one line each, with the highlighted call's
arguments wrapped below it over about a third of the window, at least three lines, with enter for the
whole text. The preview escapes and wraps only what it can show, so megabytes of arguments do not slow
the screen. `enter` opens a detail view of the highlighted call: agent, tool, time left, rule, project
and the whole arguments, wrapped to the window and scrolled with up, down, page up, page down, home and
end; `a`, `A` and `d` work there too, and if the call stops waiting the view says so and takes no
decision. A run of more than eight space separators (ASCII, no-break, ideographic, em and the other
Unicode space separators) is shown as `␠×N`, so padding cannot push the rest out of sight. The preview
reads no further into the arguments than it shows, spaces included, and writes a run that goes on past
that as `␠×N+`. The terminal bell rings when a new call starts waiting. In a window too short for
everything, lines drop out of the middle of the main screen and the detail view, so neither is ever
taller than the window and the header and the status line stay on screen.

`a`, `A` and `d` act only on the highlighted call. After it leaves the list nothing is highlighted until
the user picks a call with up or down, and when calls arrive while nothing was waiting, the oldest is
highlighted at once. A call newly highlighted either way takes those keys only after 750 ms on screen,
measured with the UI's clock, so a key pressed for the call before it, or twice, cannot land on a call
the user has not seen. A call can be highlighted while the help, the detail view or the memory browser
hides the main screen, so the 750 ms start again whenever the main screen comes back. A call whose rule
could not read one of its arguments cannot be approved for the session, so its key legend leaves `A` out.

Below the waiting calls are the agents seen in the last hour and a live feed of receipts (time, agent,
tool, decision, what decided it, outcome, duration). `m` opens a memory browser that searches every
project and still shows how many calls are waiting and why a poll failed, `g` lists the session grants,
where `r` pressed twice within five seconds on the same grant revokes it, `v` runs verify, `/` filters
the feed by agent or tool name, `?` shows the keys, `q` quits. Quitting the UI changes nothing for
running agents: their calls that need an approval wait for the timeout and are denied.
While any downstream tool has changed since it was pinned, a line under the header says how many and to
run `derbent pins`.

Text from agents, tools and the database is drawn with control characters, bidirectional overrides and
invisible characters (zero-width characters, tag characters, line separators, variation selectors)
escaped, along with the runes that draw as nothing or as a blank without being any of those (the four
Hangul fillers, the combining grapheme joiner and the braille blank), and newlines and tabs written as
`\n` and `\t`, so a value cannot add lines of its own or hide text. Every screen line, the help
screen's included, is clipped to the window.

## State

`%LOCALAPPDATA%\derbent\derbent.db` on Windows, `$XDG_STATE_HOME/derbent/derbent.db` or
`~/.local/state/derbent/derbent.db` on Linux, and `~/Library/Application Support/derbent/` on
macOS, never inside a synced folder.
`modernc.org/sqlite` needs no cgo, so the Windows binary builds without a C toolchain, and it includes
the FTS5 that memory search uses (ADR 0008). WAL mode with a thirty-second busy timeout (ADR 0008).
Migrations are embedded, numbered and forward only, and the first process to open an older database
migrates it inside `BEGIN IMMEDIATE`.

## Security

- No network listener. Agents talk to the gate over stdio, and the gate talks to downstream servers over
  stdio or outbound HTTP.
- Agent names are labels, and the database is writable by the user's account. Receipts are a record of
  what passed through the gate, not protection against the user's own account or anything running as it.
- An agent that can run shell commands as the user can also run `derbent approve` or write the database.
  Approvals, and `args` rules on shell tools, guard against mistakes and against prompt injection that
  stays inside MCP; they are not a boundary against an agent that already has a shell.
- Secrets reach downstream servers through `${env:...}` references, and their values are masked in
  stored arguments (see Config), as is whatever the redaction patterns match. Hook receipts mask only
  the secrets set in the hook's own environment (see Built-in tools), so a secret held only by a CLI's
  MCP entry needs a `redact` pattern to be masked in them.
- Memory is text written by one agent and read by another, which makes it a path for instructions planted
  by one agent to reach the next. Search and read results mark each entry with its author and as notes,
  not instructions, and a rule can put `memory_write` behind `ask`.
- A handoff is text one agent writes for another to act on, which makes it the same kind of path. What
  `handoff_list` and `handoff_take` return is marked as tasks written by agents, information and not
  instructions, and a rule can put `handoff_create` or `handoff_take` behind `ask`. Handoffs are addressed
  by label, and a label is not authentication.
- The user's config sets the rules. A repository's `.derbent.toml` can only make them stricter (see
  Project rules), so an agent that edits or deletes it only takes the project back to the user's rules.
- Tool pins guard against a downstream server that changes a tool's definition after the gate first saw
  it. They cannot tell whether that first definition was honest.

## Evidence

- **Chain.** Tests edit, delete, reorder and insert rows, and `verify` names the first bad sequence
  number each time. Four helper processes, each calling `receipt.Log.Append` 2,500 times on one
  database, append 10,000 receipts at once, and the chain verifies with no gaps. They are test processes
  that append directly, not `derbent mcp` gates.
- **Rules.** Table tests over agent, tool and argument matching, and a test that a hidden tool is absent
  from the tool list and refused if called by name anyway.
- **Proof the checks can fail.** Redaction, rule evaluation and tool hiding each have a test that
  switches them off through a knob visible only to tests and asserts that the matching test then fails.
- **Approvals across processes.** A gate waits on `ask`, a second process approves, and the call goes
  through. The timeout path denies.
- **Approval races.** Tests in one process race a decision against the deadline, and two decisions
  against each other, and find one winner in every round.
- **Grants follow the rule.** On the hook path, and on the MCP path with `memory_write` rules since the
  MCP gate refuses `native__` names, `A` on a call one ask rule holds lets the next such call through
  while a call another ask rule holds on the same tool still asks. On the hook path the grant holds
  when the rules below it are reordered or edited, and the call asks again when the granted rule is
  edited, when a rule above it is narrowed, and when `ask git push*--force*` and `ask git push*` swap
  places after an `A` on a plain push. Rule tests check a sample of edits: rule 2's fingerprint changes
  when its agent, tool, action, an `args` pattern, an `args` name or the number of `args` changes, when
  the rule above it is edited or removed, when the two swap places, and when a rule is added above; it
  stays the same when a rule below it is edited, removed or added, when the rules below are reordered,
  and when a rule with eight `args` is compiled 65 times, so Go reads its args in many orders. One pair
  of rule lists, built so that without the length written before each rule's form they would hash the
  same bytes, gets two different fingerprints. A store test migrates a database from before grants were
  keyed on the rule and finds its grants dropped. UI and command tests check that an approval with no
  rule key is approved once by `A` and refused by `derbent approve --session`. On both paths, after `A`
  on a string `git push` (a deploy note on the MCP path), a call whose argument is an array still asks
  every time, and `A` on it writes no grant.
- **Grants listed and revoked.** Tests list and revoke grants with `derbent grants`, `derbent revoke` and
  the UI's `g` and `r`, escape stored text in rows and JSON lines, and show on the MCP gate and on the
  hook, the latter through the real binary, that the call after a revoke asks again.
- **Budgets.** Rule tests check which budgets apply and when each is used up, with calls inside and
  outside the window, other tools and other agents, and the wait until the next call, which with two
  budgets used up is the longer one. A receipt test
  counts the calls in a window that starts in the middle of a second. Gate tests show a budget refusing
  the call after its limit on both paths, counting calls from other gates of the same agent, refusing an
  asked call without writing an approval, and leaving a deny to its rule.
- **Pins.** A golden test fixes the canonical form of a definition, and another shows it does not depend
  on key order or spacing. Store tests pin on first use, record a change, drop it when the server goes
  back, accept it only with the recorded change's whole hash and never a prefix of it, leave a recorded
  change alone on a recheck, and pin each tool once when checks race. Gate tests withhold a changed tool
  and refuse its calls with `pin`, serve it again after an accept while the gate runs, keep the watchers
  of two gates that hold different changes from replacing the recorded one, serve a server with
  `pin = false` unpinned, keep the pin of a tool that disappears, pin a tool the rules hide and refuse it
  by its rule when it changed. End-to-end tests start two gates on one database with the echo test
  server's description changed between them, and check `derbent pins`, `pins show`, whose accept line
  carries the quoted `--db`, `pins accept`, which refuses another change's hash and a prefix of the
  change's own, and `derbent config check`, also on a database from before the pins table.
- **Project rules.** Rule and config tests compile project rules without a catch-all, refuse any other
  key, parse a file with a byte order mark and CRLF line endings, and read the file again only when its
  size or modification time changes. Gate tests show a project rule making a call ask or deny, never
  loosening one, the user's rule named on a tie, an invalid file denying every call, an edit taking effect
  on the next call, a grant keyed on both lists asking again after an edit to either, and a denied tool
  still listed. End-to-end tests run `derbent gate` under a `.derbent.toml` and approve its call from
  another process. Config tests read only a regular file of at most 64 KiB, refusing a directory, a
  larger file and a symbolic link, and on Linux and macOS a FIFO, without blocking, also one that takes
  the file's place after the check; a unit test refuses a file other than the one checked, as a link
  swapped in would be. They follow neither a `.git` file over 64 KiB nor a FIFO, and give a directory
  that does not exist yet, below a symbolic link to a repository, the repository's own key and root.
  End-to-end tests show the hook asking under a linked worktree's own `.derbent.toml` while the main
  checkout has none, and denying under the file of a worktree of a bare repository.
- **Setup.** Preset tests load each preset through the parser `derbent config check` uses and decide one
  list of sample calls under each, with each CLI's tool names and argument keys. Golden tests fix the
  command and the text init adds for each CLI, for a binary path that holds a space; unit tests cover the
  quoting and the paths refused, the TOML append that keeps every byte of the file, the hook forms counted
  as `derbent gate`, the matchers counted as every tool, the other user-level hook files, Claude Code's
  local scope, the version compare, and the variables that move each CLI's files. End-to-end tests run
  init and doctor against a scratch home, with fake `claude`, `codex` and `copilot` commands, the test
  binary re-executed, that log their arguments. They check each file's result and its copy, that a
  second run changes nothing, that an existing entry is left alone, a hook in Codex's `hooks.json` or
  Antigravity CLI's `settings.json` included, that a file that does not parse is left byte for byte,
  that `--dry-run` and any answer but `y` or `yes` write nothing, that an old Claude Code and a hook that
  gates only some tools are skipped with the reason, and that a failed change names what was already
  made. doctor passes a setup init made, with the hook's start time as a note, and names each of 24
  planted problems, alone, with its fix; the two that need Unix file modes run only on Linux and macOS.
  With `doctor --config` naming another file, a hook and Codex's MCP entry without a `--config` of their
  own are checked against the default config, which they load. Store tests show `store.Check` refusing
  a newer schema, another program's SQLite file and a text file, leaving a database byte for byte with no
  file beside it, and passing 200 checks of a database another connection is writing and checkpointing.
- **Export.** Receipt tests show `Row.Sum` giving, for a row with text outside ASCII, the hash that the
  digest it replaced gave, a row keeping stored time text that Go would print shorter, such as `.120`,
  and the export check passing a whole chain, reporting a filtered export's gaps as runs, and naming the
  line with an edited field, a broken link, a number that does not rise, a receipt 1 whose `prev_hash` is
  not the genesis hash or a later receipt whose `prev_hash` is. End-to-end tests export more than 100
  receipts with `derbent receipts --json --limit 0` and check with `derbent verify --file`, from a file
  and from standard input, with a kept head, a wrong one and none: a filtered export, where only the last
  run is tied to the kept head, a line taken out of the middle (a gap, not a failure), an edited and a
  reordered line, an unknown field, a field in capitals, a repeated field, a zero-valued field left out,
  a null field, text after the object, an empty file, a UTF-8 byte order mark, UTF-16 with a byte order
  mark, CRLF line endings, a line of more than a mebibyte, and a cancel that stops the check at the next
  line.
- **Explain.** Every case in the rule tests also runs `Explain` and checks that it decides as `Decide`
  does, and a rule test checks the steps: each condition with the value it read, missing or not a string,
  and nothing read past the first match. A budget test checks the count and the wait of every budget that
  applies. End-to-end tests run `derbent explain` before each of a series of hook calls, allowed, denied
  and refused by a budget, and find its verdict in the receipt the call then gets, and a rule's deny in
  the hook's answer. They check a project rule that makes a call stricter, a session grant, a rule matched
  on an argument it could not read, which no grant covers, a budget that refuses an ask first, a changed
  pin, a changed pin a rule hides, an unchanged pin, `pin = false`, a name no server owns that a rule
  hides, a name the gate does not serve or cannot, arguments that are not an object or not JSON, a project
  rules file that cannot be read, a database from before grants and pins, a missing config file, `[]` for
  every empty list, escaping, and that the database is read without a byte changed and never created. The
  calls the gate refuses before any rule are shown with no rule read.
- **Handoffs.** Store tests create, list, take and finish handoffs, refuse a `to` no agent label can be and
  a title, body, note or tags over their limits, refuse a take of a handoff that is not open or not
  addressed to the agent and a finish by another agent, and race eight agents to take one handoff, with
  one winner. Gate tests pass a handoff from one agent to another through the MCP tools, with a receipt for
  each call, hide a tool a rule denies, and mark what they return as information. The hook skips the four
  tools through the real binary, and `derbent handoffs` lists them escaped, also on a database from before
  the handoffs table.
- **Rule suggestions.** Suggestion tests group answers by agent and tool, suggest an allow or a deny at the
  threshold and not below it or with mixed answers, leave out calls a project's rules asked about, and
  never widen: a `*` or `?` in the commands cuts the prefix before it; a prefix of white space only, a
  command that is not a string, a shell call without a command and a tool name with a wildcard give none;
  and a prefix never ends inside a word or a UTF-8 character. A snippet whose tool name holds quotes, a
  backslash, a newline and a bidirectional override reads back through the config parser as written and
  decides as suggested. `derbent suggest` is tested as text and JSON lines, escaped, and on a database
  from before migration 0006, and a UI test shows the status line after the fifth approval and not after
  the fourth.
- **Config and database paths.** A `--config` that does not exist makes the hook deny the call and
  `derbent mcp` and `derbent config check` fail, naming the path; without `--config` a missing default
  file allows every call and `derbent mcp` says so on stderr. A test copies a database and its `-wal`
  while a writer holds them, runs `verify`, `receipts` and `pending` on the copy, and finds both files
  unchanged. `pending` and `grants`, as rows and as JSON lines, read databases built from the real
  migrations at schemas 3 to 5, from before the approvals recorded which list asked. `approve`, `deny`,
  the UI, `derbent mcp` and `derbent gate` refuse another program's SQLite file and leave it unchanged,
  and a store test opens one with `store.Open` and finds it refused.
- **Escaping.** A test feeds the UI escape sequences, a clipboard write, a bell, a bidirectional
  override and invisible characters in the agent, tool and argument fields, and asserts that none reaches
  the main screen, the detail view or the memory browser raw and no line is wider than the window.
  Another does the same for the project on the waiting rows. Tests of `derbent receipts` and
  `derbent pending` check that stored control characters, the project's included, come out escaped in
  the table and in JSON lines, and that a JSON line still decodes to the stored text. `derbent approve`,
  `derbent deny` and `derbent config check` escape the names they print, a downstream server's tool
  names included.
- **MCP behaviour.** The SDK's client drives the gate end to end: initialize, tool listing, calls,
  forwarded `list_changed`, and a downstream server killed in the middle of a session.
- **Hook.** Golden standard input for each of the four CLIs, written from each CLI's documented format
  rather than captured from a session, golden answers in each answer format (Codex shares Claude
  Code's), and tests of which names count as the gate's own for each CLI, including a foreign entry
  whose name starts with `derbent` and a Claude Code call whose `mcp_server` is another entry. Tests run
  `derbent gate` as its own process: a call a rule allows gets no output, a denied call gets `deny`, the
  gate's own tools pass with no decision and no receipt, an `ask` is approved from another process,
  unusable input, a bad agent name, a broken config and input over 16 MiB are denied, an unknown `--cli`
  exits with status 2, and a config whose server secrets are unset still loads. Tests that run it inside
  the test process show that a hook whose context ends while it waits, as on a signal on Unix, withdraws
  its approval, and that a hook whose answer cannot be written returns the error that exits
  with status 2. A store test holds the write lock and opens a current database without waiting for
  it. The README's coverage table records what a real session showed.
- **Overhead.** One benchmark calls an echo MCP server directly and through the gate with an allow rule;
  another runs `derbent gate` as a CLI would, with an allowed call, against starting the same binary and
  exiting. Both report p50, p99 and calls per second, compared with `benchstat` over ten runs, on GitHub's
  Windows and Linux runners. The results, from one workflow run, live under `docs/benchmark-results/`
  with the runners named, and the README states only numbers in those files.
- **Demo.** [demo.md](demo.md) is the transcript of a recorded run with two Claude Code sessions under
  the agent names `claude` and `reviewer`: `claude` writes a decision to memory, `reviewer` finds it and
  asks to write a follow-up note with `memory_write`, an `ask` rule holds the call until `derbent approve`
  approves it from another shell, and `derbent verify` ends clean. The cross-vendor version, in which
  Codex finds the note, asks to create a GitHub issue and the user approves it from the UI, is
  described there as a scenario and was not recorded: Codex was left out by the owner's choice, and no
  GitHub repository or issue was approved.

## Repository layout

```
derbent/
├── cmd/derbent/                  wiring: subcommands, config, signals; end-to-end tests and benchmarks
├── internal/config/              TOML decoding and validation, default paths, projects
├── internal/rule/                matching, decisions, rule fingerprints
├── internal/gate/                the MCP server facing agents, tool listing, forwarding, hook decisions
├── internal/downstream/          MCP clients for stdio and HTTP servers, restarts
├── internal/memory/              memory over FTS5
├── internal/handoff/             handoffs: tasks agents leave for each other
├── internal/pin/                 tool pins: canonical definitions and the pins table
├── internal/preset/              the rule presets derbent init writes
├── internal/setup/               the CLIs' config files: where they are, reading them, the changes init makes
├── internal/receipt/             appending, verify, listing
├── internal/redact/              masking secrets in stored arguments
├── internal/approval/            pending approvals, polling, session grants
├── internal/suggest/             rule suggestions from the user's answers
├── internal/hook/                pre-tool hook protocols of the four CLIs, with golden files in testdata/
├── internal/store/               SQLite, migrations
├── internal/tui/                 Bubble Tea UI
├── internal/visible/             escaping text before it reaches a terminal
├── scripts/release.sh            reproducible release builds
├── .github/workflows/            ci, bench and release
├── docs/adr/  docs/benchmark-results/  docs/design.md  docs/demo.md
├── .golangci.yml  go.mod  go.sum
└── README.md  CHANGELOG.md  CONTRIBUTING.md  SECURITY.md  LICENSE
```

## Toolchain and gates

- Go 1.27.1, verified on go.dev on 2026-09-26. `go.mod` carries `go 1.27` and `toolchain go1.27.1`.
- Direct dependencies, as `go.mod` pins them: the MCP Go SDK v1.8.0 (Apache-2.0, with older parts still
  MIT while the project relicenses), `modernc.org/sqlite` v1.59.0 (BSD-3-Clause), `BurntSushi/toml`
  v1.6.0 (MIT), Bubble Tea v2.0.10, Lip Gloss v2.0.6 and `charmbracelet/x/ansi` v0.11.8 (MIT),
  `golang.org/x/sys` v0.47.0 (BSD-3-Clause) for the Windows call that names a directory past its
  junctions, and goleak v1.3.0 (MIT) for the tests.
- `gofumpt` and `golangci-lint` v2 as build gates, with at least `errcheck`, `govet`, `staticcheck`,
  `noctx`, `errorlint`, `gosec`, `exhaustive`, `sqlclosecheck` and `rowserrcheck`.
- `go mod tidy` leaves `go.mod` and `go.sum` unchanged, and `go test ./... -race -count=1` passes with
  `goleak` in every package that starts goroutines.
- A licence check reads the licence each module in the build graph ships and fails on anything outside
  an allow list kept in the repository.
- GitHub Actions runs every gate on `windows-latest` and `ubuntu-latest`, and builds on `macos-latest`.
- `scripts/release.sh` builds Windows, Linux and macOS binaries for amd64 and arm64 with `-trimpath`, CGO
  off and no build id, each twice, the second time from an empty build cache, and fails unless every pair
  is byte-identical; it then writes `SHA256SUMS`. CI's `reproducible` job runs it on every push to main
  and every pull request, and the release workflow runs it on the tag, without a restored cache, before
  it publishes. The release job needs a job that first runs vet, the tests with `-race` and the linter
  on the tag, with read-only access to the repository; only the release job can write. A clean checkout of a tag, built with go1.27.1 and no `GOFLAGS`, `GOAMD64` or `GOARM64`
  overrides, gives the published checksums.

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
| 0010 | Derbent is written in Go. |
| 0011 | A session grant covers only the calls the same rule asks about under the same rules above it, keyed on a fingerprint of that rule and every rule above it. |
| 0012 | Budgets count receipts, every matching budget applies, and a used-up budget refuses without asking. |
| 0013 | Downstream tools are pinned on first use, and a changed tool is withheld until the user accepts it. |
| 0014 | Project rules in `.derbent.toml` can only tighten the user's rules and never change tool listings. |
| 0015 | Setup adds MCP entries through each CLI's own mcp add where there is one and edits hook files, never replacing an entry, with copies first and the binary's absolute path. |
| 0016 | Presets are files written once and owned by the user, never a mode Derbent keeps. |
| 0017 | Receipt exports carry every hashed field as stored, and derbent verify --file checks them without the database, reporting gaps. |

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
5. **Proof and release.** The overhead benchmark, the demo transcript, README, ADRs finished,
   reproducible binaries. The owner moved `v1.0.0` after milestones 6 and 7.
6. **Control.** Session grants listed and revoked, budgets, tool pins and project rules. Exit: a real
   Claude Code session shows a budget refusing the next Bash call after its limit, a `.derbent.toml` in a
   scratch repository making a call ask that the user's rules allow, approved with `derbent approve`, and
   a downstream test server whose tool description changed between two gate starts withheld and served
   again after `derbent pins accept`.
7. **Workflow.** Verifiable receipt export, `derbent explain`, handoffs and rule suggestions. Exit: two
   real Claude Code sessions in which `claude` creates a handoff for `reviewer`, which lists and takes it
   and marks it done; `derbent explain` for one of those calls matching what the receipts show;
   `derbent receipts --json` exported and checked with `derbent verify --file`; and `derbent suggest` on a
   database with five approvals of the same command prefix printing the expected snippet.

How the exit checks went: milestone 1 as planned, its tests running in CI on Windows and Linux, and
milestone 2 as planned, with the GitHub MCP server answering a real Claude Code session through the
gate. Milestone 3's check ran with a real Claude Code session whose held call was approved with
`derbent approve` from another process, not with Codex approved from the UI: the owner left Codex out,
and the UI's keys are covered by tests instead. Milestone 4's coverage table has one real session,
Claude Code's; Codex was left out, Copilot CLI's monthly quota ran out before any tool call, and
Antigravity CLI was not installed, so those three rows come from each CLI's documentation.
Milestone 5's demo ran with two real Claude Code sessions under two agent names, the held call approved
with `derbent approve` from another shell, and is published as a transcript in demo.md, not as a
recording of Codex approved from the UI.

Milestone 6's check ran on 2026-09-28 with real Claude Code 2.1.283 sessions (`claude -p` on the owner's
subscription with no API key, and scratch config, settings and databases). In the two Bash parts the
hook came from the scratch repository's `.claude/settings.json`; the pins part ran through `derbent mcp`
alone. A budget of two Bash calls an hour let `echo one` and
`echo two` through and refused `echo three` before it ran, with the receipt
`native__Bash deny budget:1 refused`. A `.derbent.toml` in a scratch repository made `git status` ask
under a config that allows everything: `derbent pending` showed it as `project rule 1`,
`derbent approve` approved it once from another process, and its receipt says `user:1`. The echo test
server's tool, whose description changed between two gate starts, was left out of the second session's
tools, and the model said it had no such tool. After `derbent pins accept` with the whole hash that
`derbent pins show` printed, a third session listed the tool again and got `echo:hello` back. The changed
description was a harmless sentence, not an instruction, since the third session reads it.

Setup's check ran on 2026-09-28 on the owner's Windows 11 machine against a scratch home, with `HOME`,
`USERPROFILE`, `APPDATA` and `LOCALAPPDATA` pointing there; a probe entry first showed that Claude Code
and Copilot CLI read their MCP entries from it.
`derbent init --cli claude,copilot,antigravity --preset balanced --yes` set up Claude Code 2.1.283 and
Copilot CLI 1.0.88 through their own `mcp add` and Antigravity CLI through its files, and wrote the
balanced preset. `derbent doctor` reported no problem and exited with status 0, with the hook's start at
103 ms, the middle of three runs. Before-and-after hashes of the owner's Claude Code `settings.json`,
Copilot CLI `mcp-config.json` and Codex `config.toml` and `hooks.json` matched, and the owner's
`~/.claude.json` got no Derbent entry. A real `claude -p` session, run with the owner's real home and
only `APPDATA` and `LOCALAPPDATA` pointing at the scratch home, on the owner's subscription with no API
key, loading only the hook settings and the MCP entry init wrote, had `git push origin main` held by the
balanced preset's rule 48 (`*git *push*` on `command`) and denied with `derbent deny`: Claude Code
blocked the call and gave the model Derbent's reason, `derbent: the user denied native__Bash`, and the
receipt says `native__Bash deny user:1 refused`. Codex and Antigravity CLI were not installed, so Codex
is covered by tests with a fake and no Antigravity CLI session ran.

## Later

Not in v1, in rough order of value:

1. **Secret broker.** Downstream tokens live in the operating system's credential store (Windows
   Credential Manager, macOS Keychain, Secret Service), so neither the agents nor the config file ever
   hold them.
2. **Approvals away from the desk.** A push notification with approve and deny actions. It needs a relay
   or a listener, so it comes with its own security design.
3. **Timeline.** Receipts grouped into sessions per agent, and export to OpenTelemetry.
4. **Better memory.** Local embeddings, expiry, and flagging notes from two agents that contradict
   each other.
5. **Skill suggestions.** A tool sequence that repeats across sessions offered as a draft skill.
6. **More of MCP.** Resources and prompts from downstream servers, and approvals shown inside the
   agent's own UI through elicitation.

## Known limits and risks

- Only calls that pass through the gate are seen: tools a CLI never shows its hook (Codex's hosted web
  search), and MCP servers configured in a CLI whose hook does not report MCP calls, are outside it.
- A CLI that times out on its hook lets the call through its own permission flow (documented by Claude
  Code, assumed for the others). The hook timeout in each CLI's configuration must stay above
  `approvals.timeout`.
- For CLIs whose hook input does not name the MCP server (Codex, Copilot CLI, Antigravity CLI, and Claude
  Code before 2.1.274), a foreign MCP entry whose name and tool join, under the CLI's naming, into
  Derbent's prefix followed by a name the gate serves looks like Derbent's own: the hook skips its calls,
  so they get no decision and no receipt. The name can collide with a configured server, such as an entry
  called `derbent__github` in Codex, or with a memory or handoff tool, such as an entry called
  `derbent_memory` with a tool called `write` under Antigravity CLI's assumed naming.
- What exit status 2 does in Copilot CLI and Antigravity CLI is not documented. A hook that exits with
  status 2 there might not block the call: `--agent copilot-work` without `--cli`, for example, is an
  unknown protocol and exits 2. Pass `--cli` whenever the agent label is not the CLI's name.
- On Windows a CLI stops a hook with `TerminateProcess`, not a signal, so a hook stopped while it waits
  cannot withdraw its approval. The approval stays pending until its deadline and can still be approved,
  though no hook is left to act on it; an `A` there still grants the later calls.
- Only Claude Code's hook has been checked in a real session. The Codex, Copilot CLI and Antigravity CLI
  adapters follow each CLI's documentation, and Antigravity CLI's name for Derbent's tools is assumed.
- Every agent session starts its own downstream servers. A server that keeps state in memory does not
  share it between agents.
- The chain shows edits made without rewriting everything after them. A full rewrite, or deleting the
  newest receipts, is caught only by comparing the head hash with a copy kept elsewhere.
- Argument globs match strings, not meaning. `git push*` does not match `cd repo && git push`, so an
  `args` rule on a shell tool is a convenience, not a boundary.
- An agent that can run shell commands as the user can run `derbent approve` or write the database
  itself. Approvals and `args` rules on shell tools guard against mistakes and prompt injection that stay
  inside MCP, not against an agent that already has a shell.
- Approvals depend on the user watching. Unattended, `ask` means denied after the timeout.
- A session grant lasts for the session unless it is revoked with `derbent revoke` or `r` in the UI. On
  the hook path, which reads the config on every call, it stops applying while the granted rule or a
  rule above it differs, and applies again if the edit is undone, since grants are kept and looked up by
  the rule's fingerprint. A `derbent mcp` gate reads the config only when it starts, so an edit does not
  affect its grants (ADR 0011).
- A budget is approximate under concurrency: calls in flight at the same moment can pass it by at most
  their number.
- Pins trust the first definition a gate sees. A tool that is hostile from its first listing is pinned as
  it is; `derbent config check` shows a new server's tools before an agent uses them. That includes a
  tool that an update adds to a server already pinned: it is pinned and served at once, mid-session too,
  with no check before an agent reads it. Allowing a server's tools by name, such as
  `github__create_issue`, and denying `github__*` after them keeps a new tool out of the agents' lists.
- The project rules file is kept by size and modification time. An edit that keeps both, which only a
  file system with a coarse clock allows within one tick, is not seen until the file changes again.
- Every built-in tool call starts a `derbent gate` process. On GitHub's Windows runner an allowed hook call
  took 83.03 ms at p50, 46.91 ms of it for starting the binary; on Linux, 7.381 ms
  (`docs/benchmark-results/`).
- An endpoint scanner may slow the start of an unsigned binary. One measurement on a managed Windows 11
  machine with Microsoft Defender for Endpoint put `derbent version` (15 MB, unsigned, run from the temp
  directory) at 1.8 seconds at p50, while a 1.2 MB unsigned binary and signed programs started in tens of
  milliseconds. In setup's exit check, `derbent doctor` measured 103 ms for the hook binary's start. Why
  the two differ is not known yet. `derbent doctor` times the start on the user's own machine.
- init and doctor follow each CLI's file layout and `mcp add` syntax as documented on 2026-09-28. A CLI
  release that moves them breaks init for that CLI until Derbent follows; doctor names what it cannot
  find.
- CI runs the tests on Windows and Linux; on macOS it only builds.
- A receipt export shows a line unchanged only when its run ends at a kept head, and cannot show that
  nothing was left out (see Receipts). A stored field that is not valid UTF-8, which only unmasked
  arguments from a client that writes such bytes or a project path that is not UTF-8 can hold, cannot
  pass through JSON unchanged, so its line fails the check while the database verifies. Windows
  PowerShell 5.1 re-encodes a redirected command's output through the console's code page, which can
  change text outside ASCII; cmd, Git Bash and PowerShell 7.4 or later keep the bytes.
- `derbent explain` starts no servers, so it cannot tell whether a downstream server offers a tool or
  which definition it sends now; it reports the pin as the database records it, and cannot know whether a
  running gate withholds the tool because it could not check its pin.
- A handoff is addressed by agent label, and any agent started as `reviewer` can take the reviewer's
  handoffs. A handoff whose agent stops after taking it stays taken, since there is no release in v1.
- A suggested prefix pattern matches text, so `git *` also matches `git status && rm -rf build`, and one
  denial of any command stops an `allow` for the whole tool.
