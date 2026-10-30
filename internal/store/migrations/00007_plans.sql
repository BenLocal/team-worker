-- +goose Up
CREATE TABLE plans (
    team TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    id TEXT NOT NULL,
    agent TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    feedback TEXT NOT NULL DEFAULT '',
    decided_by TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    decided_at TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (team, id)
);
CREATE INDEX plans_agent ON plans(team, agent, status);

ALTER TABLE members ADD COLUMN plan_required INTEGER NOT NULL DEFAULT 0;

PRAGMA user_version = 7;
