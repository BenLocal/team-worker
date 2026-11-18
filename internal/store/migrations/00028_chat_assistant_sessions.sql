-- +goose Up
ALTER TABLE chat_requests ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';

CREATE TABLE chat_assistant_sessions (
    session_key TEXT PRIMARY KEY,
    codex_thread_id TEXT NOT NULL
);

PRAGMA user_version = 28;
