-- +goose Up
CREATE TABLE join_requests (
    team          TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    id            TEXT NOT NULL,
    proposed_name TEXT NOT NULL,
    user_name     TEXT NOT NULL DEFAULT '',
    capabilities  TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL,
    assigned_name TEXT NOT NULL DEFAULT '',
    agent_id      TEXT NOT NULL DEFAULT '',
    reason        TEXT NOT NULL DEFAULT '',
    decided_by    TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    decided_at    TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (team, id)
);
CREATE INDEX join_requests_status ON join_requests(team, status);

PRAGMA user_version = 8;
