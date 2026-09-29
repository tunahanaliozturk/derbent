-- A handoff is a task one agent leaves for another (ADR 0018), in a project, addressed to an agent label
-- or to '*' for any agent. It is open until an agent it is addressed to takes it, then taken by that
-- agent, then done when that agent finishes it, with a note; nothing moves it back. The agents and gate
-- sessions involved are kept, as memory entries keep theirs. Times are Unix milliseconds, 0 until the
-- step happens.
CREATE TABLE handoffs (
    id            INTEGER PRIMARY KEY,
    project       TEXT    NOT NULL,
    from_agent    TEXT    NOT NULL,
    from_session  TEXT    NOT NULL,
    to_agent      TEXT    NOT NULL,
    title         TEXT    NOT NULL,
    body          TEXT    NOT NULL,
    tags          TEXT    NOT NULL,
    state         TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'taken', 'done')),
    taken_by      TEXT    NOT NULL DEFAULT '',
    taken_session TEXT    NOT NULL DEFAULT '',
    note          TEXT    NOT NULL DEFAULT '',
    created_ms    INTEGER NOT NULL,
    taken_ms      INTEGER NOT NULL DEFAULT 0,
    done_ms       INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX handoffs_project_state ON handoffs (project, state);
