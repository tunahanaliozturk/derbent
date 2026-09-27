-- A session grant covers only the calls that the same rule sends to the user (ADR 0011). rule_key is the
-- fingerprint of the rule that asked (rule.Set.Key); an approval written before this migration has
-- none, and an empty key never matches a grant.
ALTER TABLE approvals ADD COLUMN rule_key TEXT NOT NULL DEFAULT '';

-- The grants written before this migration are dropped, not carried over: they do not say which rule
-- asked, and a grant lasts one agent session anyway. The next call such a grant covered asks again.
DROP TABLE grants;

CREATE TABLE grants (
    agent       TEXT    NOT NULL,
    session     TEXT    NOT NULL,
    tool        TEXT    NOT NULL,
    rule_key    TEXT    NOT NULL,
    approval_id INTEGER NOT NULL REFERENCES approvals (id),
    PRIMARY KEY (agent, session, tool, rule_key)
);
