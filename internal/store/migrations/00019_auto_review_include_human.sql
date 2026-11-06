-- +goose Up
-- Lets the AI auto review also look at human-only decision requests, which
-- it may then only approve or leave to the operator.
ALTER TABLE teams ADD COLUMN auto_review_include_human INTEGER NOT NULL DEFAULT 0;

PRAGMA user_version = 19;
