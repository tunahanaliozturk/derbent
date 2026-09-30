# Receipts and verify

Each call through the gate appends one receipt: its sequence number, time, project, agent, gate session,
tool, arguments after masking, the SHA-256 of the arguments before masking, the decision and what made it
(a rule, the gate itself, or an approval), the outcome, the size and SHA-256 of the result, the duration,
and the hash of the receipt before it ([ADR 0004](adr/0004-hash-chained-receipts.md)). What a tool
returned is kept as size and hash only.

```bash
derbent verify                              # count, head hash, and whether the chain is intact
derbent receipts --agent codex --since 1h   # also --tool 'github__*', --project, --limit
derbent receipts --json                     # JSON lines
```

When a receipt has been edited, removed, moved or inserted, `derbent verify` names the first one where the
chain breaks and exits with an error. It opens the database read-only and never creates it. Someone able
to write the database could still rewrite the whole chain, or delete the newest receipts, and the chain
would verify. Keep a copy of the head hash somewhere else if you want to be able to tell later that
neither happened.

To hand receipts to someone else, or keep them off this machine, export them and check the export without
the database ([ADR 0017](adr/0017-verifiable-receipt-export.md)):

```bash
derbent verify                                    # note the head it prints
derbent receipts --json --limit 0 > receipts.jsonl
derbent verify --file receipts.jsonl --head <the head verify printed>
```

Each line carries every field the hash covers, exactly as stored, so `verify --file` recomputes each
line's hash, checks that each line's `prev_hash` is the hash of the line before it where their sequence
numbers follow on, and prints the runs of sequence numbers, any gaps, the anchor (the hash of the receipt
before the first line) and the head. The first line that fails is named, and the exit status is 1. `-`
reads the export from standard input. A filtered export, such as one with `--agent codex`, has gaps,
which are reported, not failed. With `--head`, the export must end at the receipt whose hash you kept, so
a receipt written between the two commands fails the check: run them while no agent is working, or run
both again. When the export has more than one run, only the last one is tied to the kept head, and the
`tied:` line says so.

The hash takes no key, so anyone holding an export can edit a line and compute its hash again. Without
`--head` the check shows only that the lines agree with each other; with it, that the last run is what the
database held when you kept the hash. It cannot show that nothing was taken out of the middle, which looks
like a filter's gap. Windows PowerShell 5.1's `>` writes UTF-16, which `verify --file` reads, but it also
re-encodes the output through the console's code page, which can change text outside ASCII and fail those
lines; export from cmd, Git Bash or PowerShell 7.4 or later, which keep the bytes.

A stored field that is not valid UTF-8 cannot pass through JSON unchanged, so its line fails
`verify --file` while the database verifies, and the error says so.
