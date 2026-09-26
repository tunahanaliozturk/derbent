# Portcullis

An agentic gate for coding agents. Claude Code, Codex, GitHub Copilot CLI and Antigravity CLI connect to
Portcullis as one MCP server, and your other MCP servers sit behind it. Every tool call passes through the
gate, which gives the agents one shared memory, writes a hash-chained receipt for each call, holds risky
calls until you approve them, and decides which agent sees which tool.

It runs locally on Windows, macOS and Linux as a single binary. There is no daemon: a SQLite file is the
only shared state.

## Status

Early. The design is written and the first milestone is being built. Nothing here is usable yet.

- [Design](docs/design.md): what it does, how, what it does not do, and how each claim will be tested.

## Licence

Apache 2.0. See [LICENSE](LICENSE).
