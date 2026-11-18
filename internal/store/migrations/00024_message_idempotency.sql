-- +goose Up
CREATE TABLE message_idempotency (
    team TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    key TEXT NOT NULL,
    request TEXT NOT NULL,
    response TEXT NOT NULL,
    PRIMARY KEY (team, key)
);
PRAGMA user_version = 24;
