-- A budget counts the calls one agent had let through within a window (ADR 0012), so every call it
-- applies to reads that agent's receipts from a time on. This index keeps that read to the window.
CREATE INDEX receipts_agent_at ON receipts (agent, at);
