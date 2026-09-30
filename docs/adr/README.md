# Decisions

The [design](../design.md) is the contract for the build: goals, non-goals, how the parts fit, and how
each claim is tested. Every decision someone could reasonably have made differently has an ADR:

| ADR | Decision |
|---|---|
| [0001](0001-no-daemon.md) | No daemon: gate processes and the UI share state only through SQLite. |
| [0002](0002-stdio-and-agent-labels.md) | Agents connect over stdio, and the agent name is a label from each CLI's config. |
| [0003](0003-first-match-rules.md) | Rules are first-match, and a plain deny hides the tool. |
| [0004](0004-hash-chained-receipts.md) | Receipts are hash-chained, and results are kept as size and hash only. |
| [0005](0005-approval-timeout.md) | Approvals time out below the clients' tool timeouts, and a timeout denies. |
| [0006](0006-pre-tool-hooks.md) | Built-in tools are gated through each CLI's pre-tool hook. |
| [0007](0007-full-text-memory.md) | Memory search is SQLite FTS5, without embeddings. |
| [0008](0008-sqlite-without-cgo.md) | SQLite through modernc.org/sqlite, with no cgo. |
| [0009](0009-supervised-downstream-servers.md) | Downstream servers are supervised from the moment the gate starts. |
| [0010](0010-go.md) | Derbent is written in Go. |
| [0011](0011-grants-follow-the-rule.md) | A session grant covers only the calls the same rule asks about under the same rules above it. |
| [0012](0012-budgets.md) | Budgets count receipts, every matching budget applies, and a used-up budget refuses without asking. |
| [0013](0013-tool-pins.md) | Downstream tools are pinned on first use, and a changed tool is withheld until you accept it. |
| [0014](0014-project-rules.md) | Project rules can only tighten your rules and never change tool listings. |
| [0015](0015-setup-writes-cli-configs.md) | Setup adds MCP entries through each CLI's own `mcp add` and edits hook files, never replacing an entry, with copies first. |
| [0016](0016-presets-are-files.md) | Presets are files written once and owned by you, never a mode Derbent keeps. |
| [0017](0017-verifiable-receipt-export.md) | Receipt exports carry every hashed field as stored, and `derbent verify --file` checks them without the database. |
| [0018](0018-handoffs.md) | Handoffs are Derbent tools addressed by agent label or `*`, open then taken then done. |
| [0019](0019-rule-suggestions.md) | Rule suggestions are printed from your answers and never written; a shell gets its exact command, or a prefix with an ask for each operator, and a prefix that names a shell or launcher is refused. |
