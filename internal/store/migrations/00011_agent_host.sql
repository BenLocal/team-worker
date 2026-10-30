-- +goose Up
-- The machine an agent's process runs on. Agents a remote CLI starts over
-- HTTP run on that client, where the server cannot probe or stop them; an
-- empty host means the server's own machine, as for agents recorded earlier.
ALTER TABLE agents ADD COLUMN host TEXT NOT NULL DEFAULT '';
PRAGMA user_version = 11;
