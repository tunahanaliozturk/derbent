CREATE TABLE receipts (
    seq           INTEGER PRIMARY KEY,
    at            TEXT    NOT NULL,
    project       TEXT    NOT NULL,
    agent         TEXT    NOT NULL,
    session       TEXT    NOT NULL,
    tool          TEXT    NOT NULL,
    args          TEXT    NOT NULL,
    args_sha256   TEXT    NOT NULL,
    decision      TEXT    NOT NULL,
    decided_by    TEXT    NOT NULL,
    outcome       TEXT    NOT NULL,
    result_size   INTEGER NOT NULL,
    result_sha256 TEXT    NOT NULL,
    duration_ms   INTEGER NOT NULL,
    prev_hash     TEXT    NOT NULL,
    hash          TEXT    NOT NULL
);

CREATE INDEX receipts_agent_tool ON receipts (agent, tool);

CREATE TABLE memories (
    id            INTEGER PRIMARY KEY,
    project       TEXT    NOT NULL,
    author        TEXT    NOT NULL,
    session       TEXT    NOT NULL,
    title         TEXT    NOT NULL,
    body          TEXT    NOT NULL,
    tags          TEXT    NOT NULL,
    at            TEXT    NOT NULL,
    superseded_by INTEGER REFERENCES memories (id)
);

CREATE INDEX memories_current ON memories (project) WHERE superseded_by IS NULL;

CREATE VIRTUAL TABLE memories_fts USING fts5 (
    title, body, tags,
    content = 'memories',
    content_rowid = 'id',
    tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts (rowid, title, body, tags) VALUES (new.id, new.title, new.body, new.tags);
END;

CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts (memories_fts, rowid, title, body, tags)
    VALUES ('delete', old.id, old.title, old.body, old.tags);
END;

CREATE TRIGGER memories_au AFTER UPDATE OF title, body, tags ON memories BEGIN
    INSERT INTO memories_fts (memories_fts, rowid, title, body, tags)
    VALUES ('delete', old.id, old.title, old.body, old.tags);
    INSERT INTO memories_fts (rowid, title, body, tags) VALUES (new.id, new.title, new.body, new.tags);
END;
