-- +goose Up
-- Daily window for the AI auto review of decision requests: from and to
-- are "HH:MM", both empty for all day; to before from crosses midnight.
-- time_zone is an IANA name, empty for the daemon's local time.
ALTER TABLE teams ADD COLUMN auto_review_from TEXT NOT NULL DEFAULT '';
ALTER TABLE teams ADD COLUMN auto_review_to TEXT NOT NULL DEFAULT '';
ALTER TABLE teams ADD COLUMN auto_review_time_zone TEXT NOT NULL DEFAULT '';

PRAGMA user_version = 18;
