-- +goose Up
CREATE TABLE chat_requests (
    id TEXT PRIMARY KEY,
    account TEXT NOT NULL,
    message_id TEXT NOT NULL,
    sender TEXT NOT NULL,
    conversation TEXT NOT NULL,
    is_group INTEGER NOT NULL,
    thread INTEGER NOT NULL,
    body TEXT NOT NULL,
    state TEXT NOT NULL,
    team TEXT NOT NULL DEFAULT '',
    binding_id TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    UNIQUE(account, message_id)
);
CREATE INDEX chat_requests_state ON chat_requests(account,state,created_at);
CREATE INDEX chat_requests_sender ON chat_requests(account,sender,conversation,state);
CREATE TABLE chat_followups (
    account TEXT NOT NULL,
    message_id TEXT NOT NULL,
    PRIMARY KEY(account,message_id)
);
ALTER TABLE chat_jobs ADD COLUMN request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE chat_jobs ADD COLUMN queued_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX chat_jobs_request ON chat_jobs(request_id,state);
PRAGMA user_version = 26;
