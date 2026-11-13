-- +goose Up
CREATE TABLE checkpoints (
 team TEXT NOT NULL,
 id TEXT NOT NULL,
 request_key TEXT NOT NULL,
 task_id TEXT NOT NULL,
 status TEXT NOT NULL,
 next_at INTEGER,
 body TEXT NOT NULL,
 PRIMARY KEY (team, id),
 UNIQUE (team, request_key),
 FOREIGN KEY (team, task_id) REFERENCES tasks(team, id) ON DELETE CASCADE
);
CREATE INDEX checkpoints_due ON checkpoints(next_at) WHERE next_at IS NOT NULL;
CREATE INDEX checkpoints_task ON checkpoints(team, task_id);
PRAGMA user_version = 21;
