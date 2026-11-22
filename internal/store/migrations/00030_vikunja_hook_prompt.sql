-- +goose Up
ALTER TABLE vikunja_hooks ADD COLUMN leader_prompt TEXT NOT NULL DEFAULT '';

PRAGMA user_version = 30;
