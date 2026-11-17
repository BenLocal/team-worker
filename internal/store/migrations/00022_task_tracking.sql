-- +goose Up
ALTER TABLE tasks ADD COLUMN tracking TEXT NOT NULL DEFAULT '{}';

PRAGMA user_version = 22;
