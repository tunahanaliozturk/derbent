# 0004. Receipts are hash-chained, and results are kept as size and hash only

Date: 2026-09-26
Status: accepted

## Context

Receipts are the answer to "what did the agents do". They are only worth something if a receipt that
was edited or removed afterwards can be found. Tool results can be large and can hold private data, so
keeping them in full would make the database both big and sensitive.

## Decision

Each receipt stores the hash of the one before it, and its own hash is SHA-256 over its stored fields
in a fixed order, each prefixed with its length. Appends run inside `BEGIN IMMEDIATE`, which serialises
gate processes appending at the same moment, so the chain never forks. `derbent verify` walks the
chain and names the first sequence number whose position, previous hash or own hash is wrong, and it
prints the head hash.

A result is stored as its size and SHA-256, taken over bytes that anyone holding what the agent received
can rebuild: a JSON object with the result's `content`, `structuredContent` and `isError` fields, object
keys sorted, numbers as written, and no HTML escaping. The `_meta` and `resultType` fields the SDK adds
on the way out are left out. For a protocol error the hashed bytes are the error text. Arguments are
stored in full (redacted from milestone 2 on) together with the SHA-256 of the original arguments.

When a receipt cannot be written, the agent gets an error instead of the result, and the error says
whether the tool ran. A tool that ran is reported as "ran but could not be recorded; do not repeat it
without checking its effect", so the agent does not simply retry it. The same line goes to stderr.

## Consequences

An edit, deletion, insertion or reordering in the middle of the chain is found. Two things are not
found by the chain alone: a tail cut off after the last receipt someone checked, and a chain rewritten
as a whole by someone with write access to the database. Comparing the head hash with a copy kept
elsewhere catches both, and the README says so. A tool that ran but whose receipt failed can leave its
effect behind, which the agent is told about through the error.

A test recomputes the hash of every kind of result from what an MCP client received and compares it
with the receipt.
