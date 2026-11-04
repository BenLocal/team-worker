-- +goose Up
-- User event hooks (`team-worker event hook`): shell commands the daemon
-- runs when an event of the given type happens. An empty team matches
-- every team.
CREATE TABLE event_hooks (
    id TEXT PRIMARY KEY,
    event TEXT NOT NULL,
    command TEXT NOT NULL,
    team TEXT NOT NULL DEFAULT '',
    priority INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);
-- Events waiting for their hooks. A write records its events in the same
-- transaction, only when a hook matches; the daemon runs the hooks and then
-- deletes the row, so a restart in between runs them again. There is no
-- foreign key to teams: TeamShutdown is recorded as the team is deleted.
CREATE TABLE event_outbox (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    team TEXT NOT NULL,
    type TEXT NOT NULL,
    body TEXT NOT NULL,
    created_at TEXT NOT NULL
);
PRAGMA user_version = 16;
