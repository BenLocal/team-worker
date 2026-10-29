-- +goose Up
CREATE TABLE agents (
	team       TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	agent_id   TEXT NOT NULL,
	backend    TEXT NOT NULL,
	target     TEXT NOT NULL DEFAULT '',
	pid        INTEGER NOT NULL DEFAULT 0,
	command    TEXT NOT NULL DEFAULT '[]',
	cwd        TEXT NOT NULL DEFAULT '',
	status     TEXT NOT NULL,
	started_at TEXT NOT NULL,
	exited_at  TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (team, name)
);
CREATE INDEX agents_status ON agents(status);

PRAGMA user_version = 2;
