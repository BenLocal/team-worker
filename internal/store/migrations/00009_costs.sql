-- +goose Up
CREATE TABLE cost_events (
    team TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    id TEXT NOT NULL,
    agent TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_cents REAL NOT NULL DEFAULT 0,
    source TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    reported_at TEXT NOT NULL,
    PRIMARY KEY (team, id)
);
-- Automatic captures keep one running total per native session.
CREATE UNIQUE INDEX cost_events_session ON cost_events(team, agent, session_id) WHERE source = 'session';

ALTER TABLE teams ADD COLUMN budget_cents REAL NOT NULL DEFAULT 0;
ALTER TABLE teams ADD COLUMN budget_enforce INTEGER NOT NULL DEFAULT 0;
ALTER TABLE teams ADD COLUMN budget_alerted INTEGER NOT NULL DEFAULT 0;

PRAGMA user_version = 9;
