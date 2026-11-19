-- +goose Up
CREATE TABLE vikunja_hooks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    team TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    fingerprint BLOB NOT NULL,
    body TEXT NOT NULL,
    delivered INTEGER NOT NULL DEFAULT 0,
    UNIQUE (team, fingerprint)
);
CREATE INDEX vikunja_hooks_pending ON vikunja_hooks(id) WHERE delivered=0;

PRAGMA user_version = 29;
