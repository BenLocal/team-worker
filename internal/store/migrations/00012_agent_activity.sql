-- +goose Up
-- What a running agent is doing, as its CLI hooks last reported: working,
-- waiting at its prompt or blocked on a question to the user. Empty means
-- no hook has reported, e.g. a CLI without hooks; the daemon then falls back
-- to reading the screen before it types a reminder.
ALTER TABLE agents ADD COLUMN activity TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN activity_at TEXT NOT NULL DEFAULT '';
PRAGMA user_version = 12;
