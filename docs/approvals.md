# Approvals and the UI

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

Editing or reordering the rules never widens a grant ([ADR 0011](adr/0011-grants-follow-the-rule.md)).
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
([ADR 0005](adr/0005-approval-timeout.md)). If you raise it, raise `tool_timeout_sec` for the
`derbent` server in Codex's config too.
