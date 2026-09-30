# 0017. Receipt exports carry every hashed field and are checked without the database

Date: 2026-09-29
Status: accepted

## Context

`derbent verify` checks the chain inside the database. Someone who hands receipts to another person, or
keeps them off the machine, needs a form that can be checked without the database and without trusting
whoever holds it. `derbent receipts --json` printed JSON lines, but left out `prev_hash` and
`args_sha256`, and printed the time as Go formats it after reading it back, which is not always the text
the hash was taken over. People also export part of the chain: one agent's calls, or the last day.

## Decision

- An export line carries every field the hash covers exactly as stored, and the hash. The time is the
  stored RFC 3339 text with its fraction, not a time printed again after parsing. JSON escaping is as it
  was and decodes back to the stored text. The line is the `receipt.Row` that the database check reads,
  so the two cannot drift apart.
- `derbent verify --file` recomputes each line's hash with the function the database check uses, and
  checks each line's `prev_hash` against the line before it when their sequence numbers follow on. A gap
  in the numbers is allowed and reported where it is. A number that does not rise, a receipt 1 whose
  `prev_hash` is not the genesis hash, and a later receipt whose `prev_hash` is, fail. It reports the
  anchor (the first line's `prev_hash`) and the head (the last line's `hash`), and `--head` compares the
  head with a hash the user kept. Its summary says what a kept head covers: with more than one run, only
  the last run is tied to it, and without `--head` nothing ties the export to the database.
- It reads UTF-8 with or without a byte order mark and UTF-16 with one, LF or CRLF line endings and lines
  of any length. A line must be one JSON object that holds each field once, under its exact name, none of
  them null, and nothing else: a field the hash does not cover, a field given twice, left out or null,
  and text after the object are refused. Go's decoder alone matches names in any case, keeps the last of
  two copies, reads a missing or null field as zero and stops at the end of the object, so a line could
  otherwise show a reader one value while the hash covers another.
- `derbent receipts --limit 0` lists every receipt, so a whole chain can be exported.

## Consequences

- A line's hash matching its fields shows that the line was not changed only when its run ends at a kept
  head. The hash takes no key, so anyone holding an export can edit a line and compute its hash again: a
  run cut off by a gap, and any export checked without a kept head, shows only that its lines agree with
  each other. An export of the whole chain whose head is the head `derbent verify` printed, kept
  elsewhere, is one run that ends at that head, so it shows every line unchanged and nothing after its
  last line left out.
- It does not prove that nothing was left out: a line removed from the middle looks like a filter's gap,
  lines removed from the start look like an export that starts later, and lines removed from the end go
  unseen without a kept head.
- A stored field that is not valid UTF-8 cannot pass through JSON unchanged, so its line fails the check
  while the database verifies. Only arguments stored without masking, from a client that sends such
  bytes, and a project path that is not UTF-8 can hold them.
- Windows PowerShell 5.1 re-encodes a native command's output through the console's code page when it
  redirects it, so text outside ASCII can change on its way to the file and fail its line. cmd, Git Bash
  and PowerShell 7.4 or later keep the bytes, and docs/receipts.md says so.
- The field names are now an interface. A later field the hash covers makes a new export format, which an
  older verify refuses.
