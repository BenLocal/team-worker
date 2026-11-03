-- +goose Up
-- Saved copies of a team's state for `team snapshot` and `team restore`.
-- meta holds the counts that `team snapshots` lists; bundle holds the rows
-- of each saved table as JSON. There is no foreign key to teams, so a
-- snapshot outlives `team cleanup` and can bring the team back.
CREATE TABLE snapshots (
    team TEXT NOT NULL,
    id TEXT NOT NULL,
    tag TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    meta TEXT NOT NULL,
    bundle TEXT NOT NULL,
    PRIMARY KEY (team, id)
);
PRAGMA user_version = 15;
