-- A call that a project's .derbent.toml sent to the user (ADR 0014) names the rule that asked by its
-- number in that file, so the approval records which list the number belongs to: 1 for the project's.
ALTER TABLE approvals ADD COLUMN project_rule INTEGER NOT NULL DEFAULT 0;
