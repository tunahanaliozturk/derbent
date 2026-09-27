-- A pin is the SHA-256 of a downstream tool's definition as a gate first received it (ADR 0013), keyed
-- on the server and the tool's own name and shared by every gate. new_sha256 and new_definition hold a
-- definition that differs from the pin, from when a gate saw it (changed_ms) until the user accepts it
-- with derbent pins accept or the server sends the pinned one again; they are empty otherwise.
-- Definitions are canonical JSON; times are Unix milliseconds.
CREATE TABLE pins (
    server         TEXT    NOT NULL,
    tool           TEXT    NOT NULL,
    sha256         TEXT    NOT NULL,
    definition     TEXT    NOT NULL,
    pinned_ms      INTEGER NOT NULL,
    new_sha256     TEXT    NOT NULL DEFAULT '',
    new_definition TEXT    NOT NULL DEFAULT '',
    changed_ms     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (server, tool)
);
