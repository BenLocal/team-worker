-- +goose Up
-- Decision requests ("request create"): a member, normally the leader, asks
-- the operator to decide something. options is a JSON array of
-- {key, label}. auto_attempted marks a request the AI auto reviewer has
-- looked at, so it never reviews one twice.
CREATE TABLE decision_requests (
    team TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    id TEXT NOT NULL,
    from_name TEXT NOT NULL,
    summary TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    options TEXT NOT NULL DEFAULT '[]',
    recommended TEXT NOT NULL DEFAULT '',
    human_only INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending',
    choice TEXT NOT NULL DEFAULT '',
    reply TEXT NOT NULL DEFAULT '',
    decided_by TEXT NOT NULL DEFAULT '',
    auto_reason TEXT NOT NULL DEFAULT '',
    auto_note TEXT NOT NULL DEFAULT '',
    auto_attempted INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    decided_at TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (team, id)
);
CREATE INDEX decision_requests_status ON decision_requests(team, status);

-- Per-team AI auto review of decision requests; model is passed to the
-- reviewer command, empty for its default.
ALTER TABLE teams ADD COLUMN auto_review INTEGER NOT NULL DEFAULT 0;
ALTER TABLE teams ADD COLUMN auto_review_model TEXT NOT NULL DEFAULT '';

PRAGMA user_version = 17;
