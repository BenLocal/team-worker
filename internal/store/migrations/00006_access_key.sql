-- +goose Up
CREATE TABLE settings (
    name TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
PRAGMA user_version = 6;
