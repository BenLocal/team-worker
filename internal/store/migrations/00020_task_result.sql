-- +goose Up
ALTER TABLE tasks ADD COLUMN result TEXT NOT NULL DEFAULT 'null';

PRAGMA user_version = 20;
