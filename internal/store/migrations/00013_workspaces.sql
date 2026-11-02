-- +goose Up
-- Agents' git worktrees, one row per agent and repository. ClawTeam keeps
-- them in a JSON file per team; a table keeps concurrent spawns from losing
-- entries and goes with the team.
CREATE TABLE workspaces (
    team TEXT NOT NULL REFERENCES teams(name) ON DELETE CASCADE,
    agent_name TEXT NOT NULL,
    agent_id TEXT NOT NULL DEFAULT '',
    repo_root TEXT NOT NULL,
    branch_name TEXT NOT NULL,
    worktree_path TEXT NOT NULL,
    base_branch TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    PRIMARY KEY (team, agent_name, repo_root)
);
PRAGMA user_version = 13;
