-- A call a rule sends to the user. A row moves out of 'pending' when the user approves or denies it,
-- when its gate expires it at the deadline, or when the agent gives up and it is withdrawn. A gate
-- that stops while a row waits leaves it pending forever; nothing offers or decides it once its
-- deadline has passed, so it is simply ignored from then on. Times are Unix milliseconds so that
-- deadlines compare as numbers.
CREATE TABLE approvals (
    id          INTEGER PRIMARY KEY,
    created_ms  INTEGER NOT NULL,
    deadline_ms INTEGER NOT NULL,
    project     TEXT    NOT NULL,
    agent       TEXT    NOT NULL,
    session     TEXT    NOT NULL,
    tool        TEXT    NOT NULL,
    args        TEXT    NOT NULL,
    rule        INTEGER NOT NULL,
    state       TEXT    NOT NULL DEFAULT 'pending'
                CHECK (state IN ('pending', 'approved', 'denied', 'expired', 'withdrawn')),
    decided_ms  INTEGER
);

CREATE INDEX approvals_pending ON approvals (id) WHERE state = 'pending';

-- "Approve for this session": later calls to the tool from the same agent session need no approval.
CREATE TABLE grants (
    agent       TEXT    NOT NULL,
    session     TEXT    NOT NULL,
    tool        TEXT    NOT NULL,
    approval_id INTEGER NOT NULL REFERENCES approvals (id),
    PRIMARY KEY (agent, session, tool)
);

-- Receipt times are UTC RFC 3339 text; a bound written as YYYY-MM-DDTHH:MM:SS compares correctly
-- against them as text, so time filters can use this index.
CREATE INDEX receipts_at ON receipts (at);
