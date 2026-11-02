package store

import (
	"context"
	"database/sql"

	"github.com/benlocal/team-worker/internal/model"
)

const workspaceColumns = "agent_name, agent_id, team, branch_name, worktree_path, repo_root, base_branch, created_at"

// SaveWorkspace records an agent's worktree of one repository, replacing an
// earlier record for the same agent and repository.
func (s *Store) SaveWorkspace(ctx context.Context, ws model.Workspace) (model.Workspace, error) {
	if err := validate(ws.AgentName, "agent name"); err != nil {
		return model.Workspace{}, err
	}
	if ws.RepoRoot == "" || ws.WorktreePath == "" || ws.BranchName == "" {
		return model.Workspace{}, invalidf("a workspace needs its repository, worktree path and branch")
	}
	if ws.CreatedAt == "" {
		ws.CreatedAt = s.timestamp()
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, ws.TeamName); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO workspaces (`+workspaceColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (team, agent_name, repo_root) DO UPDATE SET agent_id = excluded.agent_id,
			  branch_name = excluded.branch_name, worktree_path = excluded.worktree_path,
			  base_branch = excluded.base_branch, created_at = excluded.created_at`,
			ws.AgentName, ws.AgentID, ws.TeamName, ws.BranchName, ws.WorktreePath, ws.RepoRoot, ws.BaseBranch, ws.CreatedAt)
		return err
	})
	return ws, err
}

// ListWorkspaces returns a team's worktrees, or one agent's when agent is
// set, in the order they were created.
func (s *Store) ListWorkspaces(ctx context.Context, team, agent string) ([]model.Workspace, error) {
	if err := requireTeam(ctx, s.db, team); err != nil {
		return nil, err
	}
	query := "SELECT " + workspaceColumns + " FROM workspaces WHERE team = ?"
	args := []any{team}
	if agent != "" {
		query += " AND agent_name = ?"
		args = append(args, agent)
	}
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY created_at, agent_name, repo_root", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Workspace{}
	for rows.Next() {
		var ws model.Workspace
		if err := rows.Scan(&ws.AgentName, &ws.AgentID, &ws.TeamName, &ws.BranchName, &ws.WorktreePath,
			&ws.RepoRoot, &ws.BaseBranch, &ws.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

// DeleteWorkspace forgets an agent's worktree of one repository.
func (s *Store) DeleteWorkspace(ctx context.Context, team, agent, repoRoot string) (bool, error) {
	var deleted bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM workspaces WHERE team = ? AND agent_name = ? AND repo_root = ?",
			team, agent, repoRoot)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		deleted = n > 0
		return err
	})
	return deleted, err
}
