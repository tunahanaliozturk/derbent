# Changelog

Notable changes to Derbent, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

The first release.

### Added

- `derbent mcp --agent <name>`: one MCP server for Claude Code, Codex, GitHub Copilot CLI and Antigravity
  CLI, with the memory tools `memory_write`, `memory_search` and `memory_read`. Notes belong to a project
  (its git root), any agent can write them, any agent can find them with full-text search, and a note can
  supersede an older one.
- A hash-chained receipt for every call: which agent called which tool with which arguments (masked),
  what decided it, the outcome, and the size and hash of the result. `derbent verify` walks the chain,
  names the first receipt where it breaks, and prints the head hash to keep elsewhere. `derbent receipts`
  lists receipts by agent, tool, project and time, as a table or as JSON lines.
- Rules in `config.toml` that `allow`, `deny` or `ask` by agent, tool and argument, where the first match
  wins. A tool that no call can get through is not listed to that agent.
- Your other MCP servers behind the gate, over stdio or HTTP, with their tools named `<server>__<tool>`.
  A server that stops is started again with a backoff of up to a minute. `derbent config check` validates
  the config, says how many rules and budgets it holds, starts each server once and prints the tools it
  would give the agents.
- Redaction: secrets that reach a server through `${env:...}`, and anything the `[receipts] redact`
  patterns match, are masked in stored arguments.
- Approvals: an `ask` rule holds the call until you decide, or denies it when `[approvals] timeout` (50
  seconds unless you set it) runs out. `derbent` opens a terminal UI with the waiting calls, the agents
  seen in the last hour, a live receipt feed, a memory browser and verify. `derbent approve [--session]
  <id>` and `derbent deny <id>` decide from any shell and say what they decided, and `derbent pending`
  lists the waiting calls with the rule that asked, the project and their whole arguments. The UI shows
  the rule and the project too.
- The UI's detail view: `enter` shows a waiting call's whole arguments, escaped and scrollable, and `a`,
  `A` and `d` work there too.
- `A` approves the tool's calls for the rest of the agent's session and needs a second press within 5
  seconds. A newly highlighted call takes `a`, `A` or `d` only after 750 ms on screen, so a key meant for
  the call before it cannot land on it.
- Session grants follow the rule that asked and the rules above it
  ([ADR 0011](https://github.com/tunahanaliozturk/derbent/blob/v1.0.0/docs/adr/0011-grants-follow-the-rule.md)):
  an `A` on a `git push` does not let through a `terraform apply` that another rule asks about, and
  editing or reordering the rules never widens a grant.
- `derbent grants` lists the session grants, and `derbent revoke <id>` or `derbent revoke --all` takes them
  back; the next call a revoked grant covered asks again. In the UI, `g` lists them and `r` pressed twice
  revokes one.
- Budgets: `[[budget]]` limits how many calls an agent may have let through to some tools within a sliding
  window, and a call over it is refused without asking you
  ([ADR 0012](https://github.com/tunahanaliozturk/derbent/blob/v1.0.0/docs/adr/0012-budgets.md)).
- Tool pins: each downstream tool's definition is pinned the first time a gate sees it, and a tool whose
  definition changed is withheld until `derbent pins accept <server>__<tool> <sha256>` accepts it by the
  whole hash of the new definition; another hash or a shortened one is refused. `derbent pins` and
  `derbent pins show` list and explain the changes, `pins show` ends with the accept command to copy,
  `pin = false` opts a server out, and `derbent config check` shows each tool's pin state
  ([ADR 0013](https://github.com/tunahanaliozturk/derbent/blob/v1.0.0/docs/adr/0013-tool-pins.md)).
- Project rules: a `.derbent.toml` at the root of a checkout, a linked worktree's included, can make
  calls ask or deny, never allow what your rules refuse ([ADR 0014](https://github.com/tunahanaliozturk/derbent/blob/v1.0.0/docs/adr/0014-project-rules.md)).
- `derbent gate`: a pre-tool hook for Claude Code, Codex, Copilot CLI and Antigravity CLI, so built-in
  tools such as the shell and file edits pass the same rules, approvals and receipts as MCP tools, named
  `native__<tool>`.
- The hook loads the config without failing on an unset `${env:NAME}`, since it starts no servers and
  needs none of their secrets.
- The hook skips a UTF-8 byte order mark at the start of its input, which a .NET program sends through
  `Process.StandardInput` when the console input encoding is UTF-8, instead of denying every call.
- Release binaries for Windows, Linux and macOS on amd64 and arm64, with `SHA256SUMS`. Each binary is
  built twice and published only when both builds are identical, and a clean checkout of the tag rebuilds
  the same bytes with Go 1.27.1 (see the README's Check a release).
- Benchmarks of what the gate adds to an MCP call and what a hook call costs, with results from GitHub's
  Linux and Windows runners in
  [docs/benchmark-results](https://github.com/tunahanaliozturk/derbent/blob/v1.0.0/docs/benchmark-results/README.md).
- `derbent init` sets Claude Code, Codex, GitHub Copilot CLI and Antigravity CLI up with Derbent's MCP
  entry and pre-tool hook: through each CLI's own `mcp add` where it has one, and by editing the hook
  files, with the binary's absolute path. It shows every change, asks once, copies each file first, and
  never replaces an entry you have, a `derbent gate` hook in Codex's `hooks.json` or Antigravity CLI's
  `settings.json` included; a gate hook that matches only some tools is left for you to widen, with no
  second hook added. It skips a Claude Code older than 2.1.139, which the exec-form hook needs, refuses a
  binary path that a shell would read specially, and when a change fails, names the changes already
  made and their copies. `--dry-run` shows the changes and makes none
  ([ADR 0015](https://github.com/tunahanaliozturk/derbent/blob/v1.0.0/docs/adr/0015-setup-writes-cli-configs.md)).
- Rule presets: `derbent init --preset watch|balanced|strict` writes a commented first config when you
  have none, and `--print` shows one. Derbent never changes it afterwards
  ([ADR 0016](https://github.com/tunahanaliozturk/derbent/blob/v1.0.0/docs/adr/0016-presets-are-files.md)).
- `derbent doctor` checks each CLI's hooks and MCP entries against each other and against Derbent's
  config: every `derbent gate` hook (two in one CLI is a problem) and its matcher, names, `--agent`,
  `--cli`, `--server`, a hook's own `--config`, the programs they start, hook timeouts, Codex's
  `env_vars`, Claude Code's local-scope entry and, for an exec-form hook, its version. It checks the
  config, and that the database is Derbent's, no newer than the binary, and can be written or created.
  It times the hook's start, names each problem with its fix, and changes no file.
- `derbent receipts --json` lines carry every field the hash covers, the stored time text, `args_sha256`
  and `prev_hash` included, and `--limit 0` lists every receipt. `derbent verify --file <path>`, or `-`
  for standard input, checks such an export without the database: each line's hash, the links within each
  run of sequence numbers, and with `--head` the head against a hash you kept. A filtered export's gaps
  are reported, not failed, and a file Windows PowerShell 5.1 wrote as UTF-16 is read
  ([ADR 0017](https://github.com/tunahanaliozturk/derbent/blob/v1.0.0/docs/adr/0017-verifiable-receipt-export.md)).
- `derbent explain --agent <label> --tool <name> [--args <json>]` shows how Derbent would decide a call:
  each rule in order and why it matches or not, the project's rules, every budget that applies with its
  count, the pin, with `--session` a session grant, and the verdict. It matches rules and combines your
  decision with the project's through the gate's own code, counts budgets with the gate's own read,
  starts no servers and changes nothing.
- Handoffs: `handoff_create`, `handoff_list`, `handoff_take` and `handoff_done` let one agent leave a task
  for another, addressed by agent label or `*`; one agent takes it, and only that agent finishes it.
  `derbent handoffs` lists them, and the balanced and strict presets allow the four tools
  ([ADR 0018](https://github.com/tunahanaliozturk/derbent/blob/v1.0.0/docs/adr/0018-handoffs.md)).
- `derbent suggest` prints the allow and deny rules your approvals and denials point to, with the counts
  behind each and the rule to put it above. For a shell it gives the one command you approved, or the
  start your commands share with an `ask` above it for each operator that could add another command, and
  it refuses a start of one word or one that names a shell, an interpreter or a launcher it knows; it is a
  text check, and its lists cannot be complete. An `allow` for any other tool says it lets every call
  through. It never edits your config, and the UI says when a call you approve makes a tool's answers
  reach an `allow`
  ([ADR 0019](https://github.com/tunahanaliozturk/derbent/blob/v1.0.0/docs/adr/0019-rule-suggestions.md)).

### Security

- The hook fails closed: when it cannot read the call, load the config, open the database or write the
  receipt, it denies the call and says why.
- A session grant never covers a call that its rule matched on an argument it could not read, such as a
  `command` sent as an array: each such call asks.
- A `--config` that names a file that does not exist is an error, never a config that allows every call:
  the hook denies the call, and `derbent mcp` and `derbent config check` stop, naming the path.
- `derbent verify`, `derbent receipts` and `derbent pending` open the database file read-only, so a copy
  kept as evidence stays byte for byte as it was, its `-wal` file included (SQLite may add an `-shm`
  file, and an empty `-wal` beside a copy that has none). Every command given another program's SQLite
  file with `--db`, `derbent mcp` and `derbent gate` included, refuses it and leaves it as it was,
  instead of adding Derbent's tables to it.
- A `url` server must use HTTPS, except on localhost, and redirects are refused, so its headers never
  reach another host.
- Secrets come from `${env:...}` only through a server's `env` or `headers`, never in `command` or `url`,
  where they could show up in process listings and error messages.
- The UI, `derbent receipts`, `derbent pending`, `derbent approve`, `derbent deny` and
  `derbent config check` escape control characters, bidirectional overrides and invisible characters in
  text from agents and tools before it reaches your terminal.
- The MCP gate refuses a `tools/call` request it cannot read instead of passing it on unchecked.
- A downstream tool whose definition changed after a gate first saw it is left out of the agents' tool
  lists, so they never read the changed description, and calls to it are refused.
- A `.derbent.toml` that cannot be read or holds anything but rules denies every call in its project, and
  so does one that is a symbolic link, is not a regular file, or is larger than 64 KiB, so it can never
  hold the hook or fill its memory.
- What `handoff_list` and `handoff_take` return is marked as tasks written by agents, information and not
  instructions, and a handoff can only be addressed to a label an agent can have, or `*`.
- `derbent explain`, `derbent handoffs`, `derbent suggest` and `derbent verify --file` escape text from
  agents, tools and files before it reaches your terminal, and a suggested rule's strings are quoted so
  that they cannot end early.

[1.0.0]: https://github.com/tunahanaliozturk/derbent/releases/tag/v1.0.0
