-- +goose Up
ALTER TABLE agents ADD COLUMN run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN stop_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN exit_code INTEGER NOT NULL DEFAULT 0;

CREATE TABLE sessions (
    team TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    agent TEXT NOT NULL,
    user_name TEXT NOT NULL DEFAULT '',
    data TEXT NOT NULL,
    PRIMARY KEY (team, agent, user_name)
);

PRAGMA user_version = 5;
