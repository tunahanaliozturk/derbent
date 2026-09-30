# Install and set up

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
## Set up

```bash
derbent init --preset balanced
derbent doctor
```

`derbent init` finds Claude Code, Codex, GitHub Copilot CLI and Antigravity CLI and gives each the two
entries it needs: Derbent as an MCP server, and `derbent gate` as its pre-tool hook, both with `--agent`
set to the CLI's name. It shows every change and asks once before making any, and copies each file it
changes to `<file>.derbent-backup-<time>` first. It never replaces an entry you already have: an MCP
entry named `derbent`, or a hook that runs `derbent gate`, stays as it is, and when your `derbent gate`
hook matches only some tools, such as `Bash`, init changes nothing for that CLI and says to widen the
matcher. `--cli claude,codex` picks the CLIs, `--dry-run` only shows the changes, and `--yes` skips the
question. `--preset balanced` also writes a first config when you have none (see [Rules](rules.md)).
`derbent doctor` then reads each CLI's entries, Derbent's config and its database, names each problem
with its fix, exits with status 1 when it found one, and leaves every file as it was
([ADR 0015](adr/0015-setup-writes-cli-configs.md)).

The entries point at the absolute path of the `derbent` you ran. init refuses a path that holds a
character some shell reads specially, such as `$`, `%` or `&`, and if you move the binary later, correct
the path in each entry, or remove the entries and run init again; `derbent doctor` names a path that is
gone. Claude Code's hook is written in exec form, which needs Claude Code 2.1.139 or later: init skips an
older one and says to upgrade it. Codex asks you once, in `/hooks`, to trust a new hook before it runs it.

### By hand

Each CLI needs two entries: Derbent as an MCP server, and `derbent gate` as its pre-tool hook. `--agent`
is the name your rules match, so give both entries the same one. Agent names are lower-case letters,
digits, dashes and underscores.

**Claude Code**

```bash
claude mcp add --scope user derbent -- derbent mcp --agent claude
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
entries for the other three follow each CLI's documentation (see [Built-in tools](built-in-tools.md)).

Without a config file every call is allowed, and `derbent mcp` says so on stderr when it starts. Every
call is recorded:

```bash
derbent verify
```

prints the number of receipts, the hash of the last one, and whether the chain is intact.
