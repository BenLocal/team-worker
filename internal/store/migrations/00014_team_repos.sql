-- +goose Up
-- The git repositories a team's agents get worktrees of when spawn names
-- none, as a JSON array of the daemon machine's paths.
ALTER TABLE teams ADD COLUMN repos TEXT NOT NULL DEFAULT '[]';
PRAGMA user_version = 14;
