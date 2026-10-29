-- +goose Up
ALTER TABLE members ADD COLUMN prompt TEXT NOT NULL DEFAULT '';

PRAGMA user_version = 4;
