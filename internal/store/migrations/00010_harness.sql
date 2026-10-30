-- +goose Up
CREATE TABLE harness_runs (
    team TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    id TEXT NOT NULL,
    goal TEXT NOT NULL,
    cli TEXT NOT NULL DEFAULT 'claude',
    agent_count INTEGER NOT NULL DEFAULT 3,
    current_phase TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'running',
    attempt INTEGER NOT NULL DEFAULT 1,
    max_attempts INTEGER NOT NULL DEFAULT 2,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (team, id)
);
-- A team runs at most one harness at a time.
CREATE UNIQUE INDEX harness_one_running ON harness_runs(team) WHERE status = 'running';

CREATE TABLE harness_artifacts (
    team TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    run_id TEXT NOT NULL,
    name TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (team, run_id, name),
    FOREIGN KEY (team, run_id) REFERENCES harness_runs(team, id) ON DELETE CASCADE
);

CREATE TABLE harness_history (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    team TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    run_id TEXT NOT NULL,
    phase TEXT NOT NULL,
    action TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    at TEXT NOT NULL,
    FOREIGN KEY (team, run_id) REFERENCES harness_runs(team, id) ON DELETE CASCADE
);
CREATE INDEX harness_history_run ON harness_history(team, run_id, seq);

CREATE TABLE harness_agents (
    team TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    run_id TEXT NOT NULL,
    name TEXT NOT NULL,
    role TEXT NOT NULL,
    phase TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (team, run_id, name),
    FOREIGN KEY (team, run_id) REFERENCES harness_runs(team, id) ON DELETE CASCADE
);

PRAGMA user_version = 10;
