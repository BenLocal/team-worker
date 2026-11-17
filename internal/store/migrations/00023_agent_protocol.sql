-- +goose Up
ALTER TABLE agents ADD COLUMN protocol TEXT NOT NULL DEFAULT 'null';
PRAGMA user_version = 23;
