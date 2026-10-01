# 0019. Rules are suggested from the user's answers, never written

Date: 2026-09-29
Status: accepted

## Context

After approving the same agent's calls to the same tool again and again, a user wants a rule that stops the
asking. Every answer is in the approvals table, so Derbent could write that rule itself. But the config is
the user's, often edited by hand, and its order is the policy. And one approval too many could turn into a
rule that lets far more through than the user ever looked at, a shell most of all.

## Decision

- `derbent suggest` prints TOML snippets with the counts behind them, and the UI says when a tool the user
  just approved has reached the count. Derbent never edits the config.
- Answers are grouped by agent and tool. At least N approvals and no denial suggest an `allow`; at least N
  denials and no approval a `deny`. N is 5, and `--min` changes it.
- A tool whose calls carry a `command`, or Antigravity CLI's `CommandLine`, is a shell, and so is a tool
  named as one of the CLIs' shells, `native__Bash`, `native__bash`, `native__PowerShell`,
  `native__powershell`, `native__Monitor` or `native__run_command`, whatever its calls carry. A shell gets
  no suggestion when a call's command is missing, not a string, or behind a key the rules would not read as
  it: one redaction masked, or `command` or `CommandLine` in other case. Without the names, a shell whose
  calls carry `{}` or `null` would look like any other tool and get an `allow` for all of it. A shell known
  by name must carry the key it runs, `CommandLine` for `native__run_command` and `command` for the others;
  a call that carries only the other key gives no suggestion, since a rule on an argument the tool does
  not run constrains nothing. Copilot CLI's `native__write_bash` and `native__write_powershell` type text
  into a shell that is already running, and never get a suggestion.
- When every answered command is the same, with no `*`, `?` or `[redacted]` in it, the snippet matches
  that command and nothing else, with no `*`. This is narrower than the design first said, a prefix
  followed by `*`: the user approved that command, not what could follow it. Otherwise the snippet matches
  the longest start the commands share, cut before any `*` or `?` and before `[redacted]`, and back to where
  a word ends, followed by `*`.
- Either way there is no suggestion when the command or the start:
  - holds an operator: `;`, `&`, `|`, a backtick, `(` (which covers `$(`, `<(`, `>(` and PowerShell's
    `( )`, which runs what it holds), `<`, `>` or a line break;
  - ends in `$` or `\`, which would join what the `*` adds to its last word;
  - has fewer than two words after any leading `NAME=value`: `git *` runs any command through
    `git -c alias.x='!cmd' x`, and `find *`, `make *`, `npm *`, `docker *`, `rsync *` and `tar *` have
    options that do the same;
  - has a word, in any place, that names a shell, an interpreter or a launcher, such as `sh`, `bash`,
    `pwsh`, `cmd`, `python`, `node`, `sudo`, `env`, `xargs`, `nice`, `timeout`, `ssh`, `npx`,
    `Start-Process` or `iex` (the whole list is in `internal/suggest`). A word is read as a shell finds the
    program: without the quotes around it or a leading `\`, without its directory, in any case, without the
    trailing dots and spaces Windows drops, without `.exe`, `.cmd`, `.bat` or `.com`, and without a
    version, so `C:\Windows\System32\cmd.exe`, `cmd.exe.`,
    `/bin/sh` and `python3.12` all count, and once more with every `\` dropped, since `s\h` runs `sh`; the
    first word is also read as cmd.exe reads it, without any `@`, `,` or `=` before it and up to the first
    `/`, `,` or `=`, so `cmd/c` and `@cmd` count (and so does `script/test`, a safe false refusal);
  - has a word that could name any program: one that still holds `$` or `%` (a variable), a backtick, a
    quote inside it, `[` (a glob), `{` (a brace expansion), `^` (cmd's escape), `~` before a digit (a
    Windows 8.3 short name such as `POWERS~1.EXE`), or one of the curly quotes U+2018 to U+201E, which
    PowerShell reads as quotes even inside a command name.
- A shell's `allow` for a start comes after an `ask` for the same agent and tool for each operator:
  `<start>*;*`, `<start>*&*` and so on. `*` matches any run of characters, line breaks included, so each ask
  catches its operator anywhere after the start, and a command that starts as approved but chains, pipes,
  redirects or substitutes another one still asks. A comment above the tables says what is left: the
  `allow` still matches any options after the start, such as `--force`, and calls it matches no longer
  reach the rules at and below the rule it goes above. It also says to answer the asks with `a`, once,
  not `A`: a session grant covers every later call the same rule asks about (ADR 0011), so an `A` on the
  `;` ask would let every chained command after the start through for the session. An exact command
  needs no asks. An `allow` for a
  tool that is not a shell lets every call through, whatever its arguments, and its comment says so.
- A group whose agent is not an agent label, or whose tool name is empty or holds a `*` or `?`, gets no
  suggestion: no rule could name it as it is.
- A snippet says where to put it: above the rule that asked, from the approvals, so first match reaches it.
  Calls a project's rules asked about are left out, since a rule in the user's config cannot loosen them.
- Agent labels, tool names and prefixes come from agents and the database, so every string in a snippet is
  a TOML basic string with quotes, backslashes, control characters and invisible or reordering characters
  escaped. It reads back as it was and cannot end early, add a line or start a table.

## Consequences

- The user reads every rule before it takes effect, and places it: a rule appended at the end would never
  be reached under first match.
- A rule matches text, and the checks above are lists, which cannot be complete. A command's own options
  can run another program (`go test -exec`, `git -C dir -c alias...`), a shell can spell a program in ways
  the lists do not know, and the asks cover the operators of sh, bash, PowerShell and cmd named above, not
  every shell's. So suggestions refuse the forms named here and no more, and an `allow` for a start lets
  through any options after it. A snippet is a starting point to read and narrow, not a proof that the
  calls are safe.
- Suggestions count every answered approval, including ones given by any process that can run
  `derbent approve` or write the database, and an agent with a shell can be such a process. So a snippet
  says what the approvals table holds, not only what the user chose: read it before pasting it.
- A shell's snippet for a start is ten tables long: nine asks and the `allow`. They are kept together, so
  the asks always come first.
- The hook reads the config on every call, so a pasted rule for a `native__` tool decides the next call. A
  running `derbent mcp` gate decides with the config it read when it started, so `derbent suggest` says to
  restart the CLI after pasting a rule for any other tool.
- The rule a snippet says to put it above is the number each approval recorded when it was asked. After
  the config's rules are reordered it can point at the wrong rule, so the user checks it against the
  config as it is now.
- Five is a guess at "the same thing again": fewer would suggest rules from chance, more would keep the
  user approving the same call for longer. `--min` lets a user choose.
- The group is the agent and the tool, so one denial of any command stops an `allow` for the whole tool,
  and a user who approves many unrelated commands of one shell gets no suggestion when they share no start.
- A key that holds the redaction mask stops suggestions for any tool whose calls carry it, shell or not,
  since a command could hide behind it. So a redaction pattern that matches a common key name, such as
  `path`, which masks `file_path` to `file_[redacted]`, stops suggestions for every tool with that key.
