# 0007. Memory search is SQLite FTS5, without embeddings

Date: 2026-09-26
Status: accepted

## Context

Agents need to find notes other agents left: decisions, conventions, findings. Semantic search with
embeddings finds notes that use different words for the same idea, but it needs a model, either a
local one shipped with the binary or a hosted one that would receive every note.

## Decision

Notes are indexed with SQLite FTS5 using the `unicode61` tokenizer with `remove_diacritics 2`, and
ranked with bm25. Free text from the agent is split into words, each word is quoted, and the words are
joined with OR, so no input can be an FTS5 syntax error. Text with no words at all is refused with a
validation error. Superseded notes are kept, readable by id, and left out of search.

## Consequences

Search works offline, in the same file as everything else, and is predictable. It only finds notes that
share words with the query. Diacritics fold (`şema` is found by `sema`, `Ödeme` by `odeme`), but letters
that are distinct in their language stay distinct: Turkish dotless `ı` is not `i`, so `karari` does not
find `kararı`. Local embeddings are on the list for later.
