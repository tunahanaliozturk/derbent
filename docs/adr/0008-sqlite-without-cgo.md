# 0008. SQLite through modernc.org/sqlite, with no cgo

Date: 2026-09-26
Status: accepted

## Context

Portcullis is one binary that has to build and run on Windows without extra tools, and every gate
process and the UI share state through one database file. The usual Go SQLite driver,
`mattn/go-sqlite3`, wraps the C library and needs cgo, which means a C compiler on every machine that
builds the binary and a harder cross-compile.

## Decision

Use `modernc.org/sqlite`, a translation of SQLite to Go. Its build includes FTS5, which the memory
tools need. Pragmas (WAL, a thirty-second busy timeout, `synchronous = NORMAL`, foreign keys) are set in
the DSN so every pooled connection gets them. Writes that read before they write run inside
`BEGIN IMMEDIATE`, which takes the write lock up front. SQLite's busy handler is not fair: while other
processes keep taking the lock, a waiting writer can miss it for seconds. CI caught a five-second timeout
running out with four processes appending under the race detector, so the timeout is thirty seconds,
still below the tool call timeouts of the agent CLIs.

## Consequences

`CGO_ENABLED=0` builds work everywhere and are reproducible. The translated driver is slower than the C
one; the overhead benchmark in milestone 5 measures the gate as a whole, driver included.
`synchronous = NORMAL` in WAL mode survives a process crash but can lose the last transactions on power
loss, which is acceptable for a local record of agent activity and is stated in the operations notes.
