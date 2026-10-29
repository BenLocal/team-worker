-- +goose Up
CREATE TABLE teams (
	name          TEXT PRIMARY KEY,
	description   TEXT NOT NULL DEFAULT '',
	lead_agent_id TEXT NOT NULL DEFAULT '',
	prompt        TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL
);
CREATE TABLE members (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	team       TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
	name       TEXT NOT NULL,
	user_name  TEXT NOT NULL DEFAULT '',
	agent_id   TEXT NOT NULL,
	agent_type TEXT NOT NULL,
	joined_at  TEXT NOT NULL,
	UNIQUE (team, name, user_name)
);
CREATE TABLE tasks (
	team        TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
	id          TEXT NOT NULL,
	subject     TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL,
	priority    TEXT NOT NULL,
	owner       TEXT NOT NULL DEFAULT '',
	locked_by   TEXT NOT NULL DEFAULT '',
	locked_at   TEXT NOT NULL DEFAULT '',
	blocks      TEXT NOT NULL DEFAULT '[]',
	started_at  TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL,
	metadata    TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (team, id)
);
CREATE INDEX tasks_owner_status ON tasks(team, owner, status);
CREATE TABLE task_deps (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	team       TEXT NOT NULL,
	task_id    TEXT NOT NULL,
	blocked_by TEXT NOT NULL,
	UNIQUE (team, task_id, blocked_by),
	FOREIGN KEY (team, task_id) REFERENCES tasks(team, id) ON DELETE CASCADE
);
CREATE INDEX task_deps_blocked_by ON task_deps(team, blocked_by);
CREATE TABLE messages (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	team       TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
	inbox      TEXT NOT NULL,
	body       TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX messages_inbox ON messages(team, inbox, id);
CREATE TABLE events (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	team       TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
	kind       TEXT NOT NULL,
	body       TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX events_team_kind ON events(team, kind, id);

PRAGMA user_version = 1;
