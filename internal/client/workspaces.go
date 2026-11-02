package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/benlocal/team-worker/internal/model"
)

// ListWorkspaces returns a team's git worktrees, or one agent's.
func (c *Client) ListWorkspaces(ctx context.Context, team, agent string) ([]model.Workspace, error) {
	var out []model.Workspace
	return out, c.do(ctx, http.MethodGet, teamPath(team, "workspaces"), url.Values{"agent": {agent}}, nil, &out)
}

// SaveWorkspace records an agent's worktree; the daemon's spawn uses it.
func (c *Client) SaveWorkspace(ctx context.Context, ws model.Workspace) (model.Workspace, error) {
	var out model.Workspace
	return out, c.do(ctx, http.MethodPut, teamPath(ws.TeamName, "workspaces"), nil, ws, &out)
}

// DeleteWorkspace forgets an agent's worktree of one repository without
// touching it on disk.
func (c *Client) DeleteWorkspace(ctx context.Context, team, agent, repoRoot string) error {
	return c.do(ctx, http.MethodDelete, teamPath(team, "workspaces", agent), url.Values{"repo": {repoRoot}}, nil, nil)
}

// WorkspaceStatus shows the uncommitted changes in an agent's worktrees.
func (c *Client) WorkspaceStatus(ctx context.Context, team, agent string, in model.WorkspaceRequest) (model.WorkspaceOp, error) {
	var out model.WorkspaceOp
	return out, c.do(ctx, http.MethodGet, teamPath(team, "workspaces", agent, "status"), url.Values{"repo": {in.Repo}}, nil, &out)
}

// CheckpointWorkspace commits everything in an agent's worktrees.
func (c *Client) CheckpointWorkspace(ctx context.Context, team, agent string, in model.WorkspaceRequest) (model.WorkspaceOp, error) {
	var out model.WorkspaceOp
	return out, c.do(ctx, http.MethodPost, teamPath(team, "workspaces", agent, "checkpoint"), nil, in, &out)
}

// MergeWorkspace merges an agent's branches into their base branches, or
// into in.Target. A refused merge is a result with status merge_failed.
func (c *Client) MergeWorkspace(ctx context.Context, team, agent string, in model.WorkspaceRequest) (model.WorkspaceOp, error) {
	var out model.WorkspaceOp
	return out, c.do(ctx, http.MethodPost, teamPath(team, "workspaces", agent, "merge"), nil, in, &out)
}

// CleanupWorkspaces removes an agent's worktrees, or the whole team's when
// in.Agent is empty.
func (c *Client) CleanupWorkspaces(ctx context.Context, team string, in model.WorkspaceRequest) (model.WorkspaceOp, error) {
	var out model.WorkspaceOp
	return out, c.do(ctx, http.MethodPost, teamPath(team, "workspaces", "cleanup"), nil, in, &out)
}

// ImportWorkspaces records the worktrees of a ClawTeam workspace registry
// on the daemon's machine (in.File, default ClawTeam's for the team).
func (c *Client) ImportWorkspaces(ctx context.Context, team string, in model.WorkspaceRequest) (model.WorkspaceOp, error) {
	var out model.WorkspaceOp
	return out, c.do(ctx, http.MethodPost, teamPath(team, "workspaces", "import"), nil, in, &out)
}

// SetTeamRepos replaces the team's default repositories for worktrees.
func (c *Client) SetTeamRepos(ctx context.Context, team string, repos []string) (model.Team, error) {
	var out model.Team
	body := map[string][]string{"repos": repos}
	if repos == nil {
		body["repos"] = []string{}
	}
	return out, c.do(ctx, http.MethodPut, teamPath(team, "repos"), nil, body, &out)
}
