-- +goose Up
CREATE TABLE chat_result_assets (
    job_id TEXT NOT NULL REFERENCES chat_jobs(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    data BLOB,
    PRIMARY KEY(job_id, position)
);

-- +goose StatementBegin
CREATE TRIGGER chat_result_assets_cleanup AFTER UPDATE OF state ON chat_jobs
WHEN NEW.state NOT IN ('pending', 'sending')
BEGIN
    DELETE FROM chat_result_assets WHERE job_id=NEW.id;
END;
-- +goose StatementEnd
PRAGMA user_version = 27;
