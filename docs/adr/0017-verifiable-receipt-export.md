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
  in the numbers is allowed and reported where it is. A number that does not rise, and a receipt 1 whose
  `prev_hash` is not the genesis hash, fail. It reports the anchor (the first line's `prev_hash`) and the
  head (the last line's `hash`), and `--head` compares the head with a hash the user kept.
- It reads UTF-8 with or without a byte order mark and UTF-16 with one, any line ending and lines of any
  length, and refuses a line with a field the hash does not cover.
- `derbent receipts --limit 0` lists every receipt, so a whole chain can be exported.

## Consequences

- An export proves that each line is intact, that the lines of each run follow one another in the chain,
  and with a kept head that the export reaches that head. An export of the whole chain whose head is the
  head `derbent verify` printed also shows that nothing after its last line was left out.
- It does not prove that nothing was left out: a line removed from the middle looks like a filter's gap,
  and lines removed at either end go unseen without a kept head.
- A stored field that is not valid UTF-8 cannot pass through JSON unchanged, so its line fails the check
  while the database verifies. Only arguments stored without masking, from a client that sends such
  bytes, and a project path that is not UTF-8 can hold them.
- Windows PowerShell 5.1 re-encodes a native command's output through the console's code page when it
  redirects it, so text outside ASCII can change on its way to the file and fail its line. cmd, Git Bash
  and PowerShell 7.4 or later keep the bytes, and the README says so.
- The field names are now an interface. A later field the hash covers makes a new export format, which an
  older verify refuses.
