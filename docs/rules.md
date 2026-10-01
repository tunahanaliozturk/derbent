# Rules

Rules decide every call. This page covers your rules, the presets, `derbent explain`, project rules,
budgets and rule suggestions.

## Your rules

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

Rules are tried in order and the first match wins ([ADR 0003](adr/0003-first-match-rules.md)).
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

### See how a call would be decided

To see how a call would be decided before it happens, ask Derbent:

```bash
derbent explain --agent claude --tool native__Bash --args '{"command":"git push origin main"}'
```

It prints each rule in order and why it matches or not, down to the first match; then the project's rules
from the `.derbent.toml` of the checkout you are in, or of `--project`; every budget that applies, with its
count; a downstream tool's pin; with `--session`, whether a session grant covers the call; and last the
verdict and what decides it, `ask (rule:4)` for this call under the rules above. It changes nothing and
starts no servers, and `--json` prints one object. Built-in tools go by the name the hook gives them,
`native__<tool>`.

Windows PowerShell 5.1 drops the double quotes inside `--args`. Writing each as `\"` keeps them, but when
a value holds a space, as `git push origin main` does, PowerShell 5.1 then splits the argument at it. Put
`--%` before `--args`, as the last flag, and PowerShell passes the rest of the line as written:

```powershell
derbent explain --agent claude --tool native__Bash --% --args "{\"command\":\"git push origin main\"}"
```

### Presets

Three presets give a first config instead of a blank page. `derbent init --preset <name>` writes one to
the config path when no file is there, and `--print` prints it instead:

- `watch` allows and records every call, for the first days, to see what your agents do.
- `balanced` allows reading, and asks before pushing, `--force`, `git reset --hard`, deleting files
  (`git clean -f` and `xargs rm` too), running a download (`curl ... | sh`, `$(curl ...)`), the listed
  subcommands of terraform, tofu, pulumi, kubectl and helm, such as `terraform apply` and
  `kubectl delete`, writing a `.env` file with a file tool or a shell redirect, `~/.ssh` in a path or a
  command, and every tool of a GitHub server behind Derbent that is not a known read. Its shell rules
  match text, so a command in a form they do not list, such as `"git" push`, gets through until Derbent
  parses shell commands; the file's own comments say what each pattern catches and misses.
- `strict` allows reading, the built-in tools that keep notes and plans or ask you a question (such as
  Claude Code's `TodoWrite`, `ExitPlanMode` and `AskUserQuestion`), Derbent's memory and handoff tools
  (`handoff_create`, `handoff_list`, `handoff_take` and `handoff_done`) and the twelve known read tools of
  a GitHub server behind Derbent, and asks about everything else, every shell command included.

Derbent writes the preset once and never changes it; it is yours to edit
([ADR 0016](adr/0016-presets-are-files.md)).

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
([ADR 0014](adr/0014-project-rules.md)). Receipts say `project:1` when a project rule decided, and the
UI and `derbent pending` say `project rule 1`. An edit takes effect on the next call. A file with any other
key, or a rule Derbent cannot read, denies every call in that project until it is fixed, and so does a
`.derbent.toml` that is a symbolic link, is not a regular file, or is larger than 64 KiB. Keys count only
as written, so `ACTION` or `[[Rule]]` is another key. Project rules decide calls only and never change
which tools an agent sees. An agent that can edit the repository can edit or delete the file, which only
takes the project back to your own rules.

When you approve a built-in tool call that only the project asked about, and your own rules allow it, the
hook answers as your rules alone would: with no decision, so the CLI's own permission settings still
apply. A call your own rules ask about gets `allow` once you approve it, as without a project file.

## Rule suggestions

When you keep approving the same thing, Derbent can say which rule would stop the asking:

```bash
derbent suggest               # --min 3 to need fewer answers, --json for JSON lines
```

It groups your answers by agent and tool, and leaves out calls a project's rules asked about. Five
approvals and no denial give an `allow`, five denials and no approval a `deny`, each as `[[rule]]` tables
with the counts behind them and the rule to put them above, so first match reaches them. For a shell tool,
when every command you approved was the same, the `allow` matches that command alone. Otherwise it matches
the start your commands share, cut back to a whole word, and comes after an `ask` for each operator that
could add another command after that start (shortened here: there are nine):

```toml
# claude's native__Bash: approved 5 times and never denied.
# Put it above rule 1 in your config, so first match reaches it before rule 1, which asked.
# The allow at the end also matches any options after the prefix, such as --force.
# The asks above it stop chained, piped, redirected and substituted commands.
# Answer those asks with a (once), not A: a session grant lets later calls matching that ask through.
# Calls the allow matches no longer reach the rules at and below rule 1.
[[rule]]
agent  = "claude"
tool   = "native__Bash"
args   = { command = "go test *;*" }
action = "ask"
# ... the same ask for &, |, `, (, <, >, \n and \r ...
[[rule]]
agent  = "claude"
tool   = "native__Bash"
args   = { command = "go test *" }
action = "allow"
```

A shell gets no suggestion when a command cannot be read or is not under the key that shell runs
(`CommandLine` for Antigravity CLI's `native__run_command`, `command` for the others), or when the command
or the start holds an operator such as `&&`, `;`, `|`, `>` or `$(`, has only one word (`git *` runs any
command through `git -c alias.x='!cmd' x`), or names a shell, an interpreter or a launcher in any of its
words, such as `sh`, `python`, `sudo`, `env`, `xargs`, `nice` or `Start-Process`, however it is spelled
(`/bin/sh`, `CMD.EXE`, `cmd.exe.`, `POWERS~1.EXE`, `python3.12`, `cmd/c` or `@cmd` as cmd.exe reads
them, or with PowerShell's curly quotes inside it). Because cmd.exe ends a name at `/`, a first word that starts with a launcher's name and
a `/`, such as `script/test` or `env/bin/pytest`, is refused too; write that rule by hand if you want it.
These are text checks, and their lists cannot be complete: an option of an ordinary program can run
another one, as `go test -exec` does, and the `allow` lets any options through.
Read a snippet before you paste it, and narrow it.

An `allow` for a tool that is not a shell lets every call through, whatever its arguments, and its comment
says so. That can be far more than you approved: five approvals of `native__Write` to a `.env` file, which
the balanced preset asks about, suggest an `allow` for `native__Write` above that rule, and pasted there it
also lets a write to `~/.ssh/authorized_keys` through.

Suggestions count every answered approval, including ones given by any process that can run
`derbent approve` or write the database, and an agent with a shell can be such a process. A snippet shows
what the approvals table holds, so read it before you paste it. A `[receipts] redact` pattern that matches
an argument's name masks the name as well, and a tool whose calls carry a masked name gets no suggestion,
shell or not: a pattern such as `path` stops suggestions for every tool that takes a `file_path`.

Derbent never edits your config. The hook reads it on every call, so a rule for a `native__` tool decides
the next call, but a running `derbent mcp` gate decides with the config it read when it started: after you
paste a rule for any other tool, restart the CLI. After you approve a call in the UI, when that agent's
answers for the tool now make an `allow`, the status line ends with
`claude's native__Bash approved 5 times: run derbent suggest for a rule`
([ADR 0019](adr/0019-rule-suggestions.md)).

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
([ADR 0012](adr/0012-budgets.md)).
Calls made at the same moment can pass a budget by at most the number of calls in flight at once.
