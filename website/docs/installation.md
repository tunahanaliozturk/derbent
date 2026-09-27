---
title: Installation
sidebar_position: 2
---

# Installation

Derbent is one binary for Windows, macOS and Linux. It needs nothing else on the machine.

## Prebuilt binaries

[[release download link]]

## With Go

Needs Go 1.27.

```bash
go install github.com/tunahanaliozturk/derbent/cmd/derbent@latest
```

`go install` puts `derbent` in Go's bin directory (`go env GOBIN`, or `$(go env GOPATH)/bin` when that is
empty). That directory has to be on your `PATH`, because the agent CLIs start `derbent` by name.

Check it:

```bash
derbent version
```

A build from source prints `dev`.

## Where Derbent keeps its files

| | Config | Database |
|---|---|---|
| Windows | `%AppData%\derbent\config.toml` | `%LocalAppData%\derbent\derbent.db` |
| macOS | `~/Library/Application Support/derbent/config.toml` | `~/Library/Application Support/derbent/derbent.db` |
| Linux | `$XDG_CONFIG_HOME/derbent/config.toml`, else `~/.config/derbent/config.toml` | `$XDG_STATE_HOME/derbent/derbent.db`, else `~/.local/state/derbent/derbent.db` |

- The config file is optional. Without it, every call is allowed. See [Configuration](./configuration.md).
- The database is created the first time a gate or a hook needs it.
- Commands take `--config` and `--db` to use other paths (see the [CLI reference](./cli.md)). Keep the
  database out of synced folders such as OneDrive, Dropbox or iCloud Drive: every gate process relies on
  SQLite's file locking, which a synced folder cannot guarantee.

Next: [connect your agents](./connect-agents.md).
