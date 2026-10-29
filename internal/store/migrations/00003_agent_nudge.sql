-- +goose Up
ALTER TABLE agents ADD COLUMN nudge INTEGER NOT NULL DEFAULT 0;

PRAGMA user_version = 3;
