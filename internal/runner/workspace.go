package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/workspace"
)

// plannedWorkspace is a worktree spawn gives the agent: one recorded
// earlier and still in place, or one to create.
type plannedWorkspace struct {
	model.Workspace
	reuse bool
}

// planWorkspaces decides the agent's working directory and git worktrees,
// as ClawTeam does: with the workspace mode auto (the default), a known
// repository gets a worktree on branch clawteam/<team>/<agent>; always
// requires one, never and --no-workspace turn it off. A repository is
// known from --repo, or from a local client's working directory. An
// explicit --cwd is used as it is.
func planWorkspaces(ctx context.Context, c *client.Client, dataDir string, req Request) ([]plannedWorkspace, string, error) {
	dir := config.WorkspaceDir(dataDir, req.Team, req.Name)
	if req.Cwd != "" {
		if len(req.Repos) > 0 {
			return nil, "", fmt.Errorf("use --repo or --cwd, not both")
		}
		if req.Workspace != nil && *req.Workspace {
			return nil, "", fmt.Errorf("--workspace places the agent in its worktree; drop --cwd")
		}
		cwd, err := filepath.Abs(req.Cwd)
		return nil, cwd, err
	}
	mode, err := config.WorkspaceMode()
	if err != nil {
		return nil, "", err
	}
	if (req.Workspace != nil && !*req.Workspace) || (req.Workspace == nil && mode == "never") {
		// Without a worktree, a single repository is worked in directly.
		switch len(req.Repos) {
		case 0:
			return nil, dir, nil
		case 1:
			cwd, err := filepath.Abs(req.Repos[0])
			return nil, cwd, err
		}
		return nil, "", fmt.Errorf("without a workspace, give at most one --repo")
	}
	required := (req.Workspace != nil && *req.Workspace) || mode == "always"
	var roots []string
	seen := map[string]bool{}
	for _, repo := range req.Repos {
		root, err := workspace.Toplevel(ctx, repo)
		if err != nil {
			return nil, "", err
		}
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	if len(roots) == 0 && req.CallerDir != "" {
		if root, err := workspace.Toplevel(ctx, req.CallerDir); err == nil {
			roots = append(roots, root)
		}
	}
	if len(roots) == 0 {
		if required {
			return nil, "", fmt.Errorf("a workspace needs a git repository: pass --repo, or run spawn inside one")
		}
		return nil, dir, nil
	}
	existing, err := c.ListWorkspaces(ctx, req.Team, req.Name)
	if err != nil && !client.IsNotFound(err) {
		return nil, "", err
	}
	recorded := map[string]model.Workspace{}
	for _, ws := range existing {
		recorded[ws.RepoRoot] = ws
	}
	branch := workspace.BranchName(req.Team, req.Name)
	paths := workspace.Paths(dir, roots)
	planned := make([]plannedWorkspace, len(roots))
	for i, root := range roots {
		if ws, ok := recorded[root]; ok && ws.WorktreePath == paths[i] && workspace.IsWorktreeOf(ctx, ws.WorktreePath, root) {
			ws.AgentID = req.AgentID
			planned[i] = plannedWorkspace{Workspace: ws, reuse: true}
			continue
		}
		base, err := workspace.CurrentBranch(ctx, root)
		if err != nil {
			return nil, "", err
		}
		planned[i] = plannedWorkspace{Workspace: model.Workspace{AgentName: req.Name, AgentID: req.AgentID,
			TeamName: req.Team, BranchName: branch, WorktreePath: paths[i], RepoRoot: root, BaseBranch: base}}
	}
	return planned, dir, nil
}

// createWorkspaces creates the planned worktrees that are not in place yet
// and records them all. The returned function undoes what it created, for
// a spawn that fails afterwards; reused worktrees are left alone.
func createWorkspaces(ctx context.Context, c *client.Client, planned []plannedWorkspace) (func(), error) {
	type created struct {
		ws        model.Workspace
		newBranch bool
	}
	var done []created
	rollback := func() {
		// Best effort: the spawn already failed, and what cannot be undone is
		// left for workspace cleanup. The spawn's context may be gone.
		bg := context.WithoutCancel(ctx)
		for i := len(done) - 1; i >= 0; i-- {
			d := done[i]
			workspace.Remove(bg, d.ws.RepoRoot, d.ws.WorktreePath)
			if d.newBranch {
				workspace.DeleteBranch(bg, d.ws.RepoRoot, d.ws.BranchName)
			}
			c.DeleteWorkspace(bg, d.ws.TeamName, d.ws.AgentName, d.ws.RepoRoot)
		}
	}
	if len(planned) > 1 {
		// Several repositories share the workspace directory as their parent.
		if err := os.MkdirAll(filepath.Dir(planned[0].WorktreePath), 0o755); err != nil {
			return rollback, err
		}
	}
	for _, p := range planned {
		if !p.reuse {
			newBranch, err := workspace.Create(ctx, p.RepoRoot, p.WorktreePath, p.BranchName, p.BaseBranch)
			if err != nil {
				rollback()
				return func() {}, err
			}
			done = append(done, created{ws: p.Workspace, newBranch: newBranch})
		}
		if _, err := c.SaveWorkspace(ctx, p.Workspace); err != nil {
			rollback()
			return func() {}, err
		}
	}
	return rollback, nil
}

func workspacesOf(planned []plannedWorkspace) []model.Workspace {
	var out []model.Workspace
	for _, p := range planned {
		out = append(out, p.Workspace)
	}
	return out
}
