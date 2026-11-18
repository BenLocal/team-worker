-- +goose Up
CREATE TABLE chat_jobs (
    id TEXT PRIMARY KEY,
    account TEXT NOT NULL,
    binding_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    payload TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    ready_at INTEGER NOT NULL DEFAULT 0,
    result TEXT NOT NULL DEFAULT ''
);
CREATE INDEX chat_jobs_pending ON chat_jobs(account, state, ready_at);
CREATE TABLE chat_cursors (
    binding_id TEXT PRIMARY KEY,
    cursor TEXT NOT NULL
);
PRAGMA user_version = 25;
