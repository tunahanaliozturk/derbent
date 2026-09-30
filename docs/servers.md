# Downstream servers and tool pins

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
as in [Rules](rules.md), or start it with `--read-only` or fewer `--toolsets`. A `command` server
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
refuses calls to it and writes a warning to stderr ([ADR 0013](adr/0013-tool-pins.md)).

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
