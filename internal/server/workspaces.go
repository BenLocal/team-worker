package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
	"github.com/benlocal/team-worker/internal/workspace"
)

// Agents' git worktrees live on the daemon's machine, so the git work for
// them happens here, whichever transport the client used.

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.ListWorkspaces(r.Context(), r.PathValue("team"), r.URL.Query().Get("agent"))
	respond(w, out, err)
}

func (s *Server) saveWorkspace(w http.ResponseWriter, r *http.Request) {
	var in model.Workspace
	if !decode(w, r, &in) {
		return
	}
	in.TeamName = r.PathValue("team")
	out, err := s.store.SaveWorkspace(r.Context(), in)
	respond(w, out, err)
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	deleted, err := s.store.DeleteWorkspace(r.Context(), r.PathValue("team"), r.PathValue("agent"), r.URL.Query().Get("repo"))
	respond(w, map[string]bool{"deleted": deleted}, err)
}

// agentWorkspaces returns an agent's worktrees, or the one of repo: its
// root, a path inside it, or its directory name.
func (s *Server) agentWorkspaces(ctx context.Context, team, agent, repo string) ([]model.Workspace, error) {
	all, err := s.store.ListWorkspaces(ctx, team, agent)
	if err != nil {
		return nil, err
	}
	if repo == "" {
		if len(all) == 0 {
			return nil, &store.Error{Kind: store.KindNotFound, Msg: fmt.Sprintf("No workspace found for '%s'", agent)}
		}
		return all, nil
	}
	root, _ := workspace.MainRoot(ctx, repo)
	for _, ws := range all {
		if ws.RepoRoot == repo || ws.RepoRoot == root || filepath.Base(ws.RepoRoot) == repo {
			return []model.Workspace{ws}, nil
		}
	}
	return nil, &store.Error{Kind: store.KindNotFound, Msg: fmt.Sprintf("No workspace of %s found for '%s'", repo, agent)}
}

func repoLabel(ws model.Workspace) string { return filepath.Base(ws.RepoRoot) }

// checkpointMessage is ClawTeam's default checkpoint commit message.
func checkpointMessage(agent, what string) string {
	return fmt.Sprintf("[clawteam] %s: %s @ %s", what, agent, time.Now().Format("20060102-150405"))
}

func (s *Server) workspaceStatus(w http.ResponseWriter, r *http.Request) {
	team, agent := r.PathValue("team"), r.PathValue("agent")
	all, err := s.agentWorkspaces(r.Context(), team, agent, r.URL.Query().Get("repo"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	op := model.WorkspaceOp{Status: "ok", Team: team, Agent: agent}
	for _, ws := range all {
		stat, err := workspace.DiffStat(r.Context(), ws.WorktreePath)
		if err != nil {
			respond(w, nil, err)
			return
		}
		op.Repos = append(op.Repos, model.WorkspaceRepo{Repo: ws.RepoRoot, Branch: ws.BranchName, Path: ws.WorktreePath, Output: stat})
	}
	respond(w, op, nil)
}

func (s *Server) checkpointWorkspace(w http.ResponseWriter, r *http.Request) {
	team, agent := r.PathValue("team"), r.PathValue("agent")
	var in model.WorkspaceRequest
	if !decode(w, r, &in) {
		return
	}
	all, err := s.agentWorkspaces(r.Context(), team, agent, in.Repo)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if in.Message == "" {
		in.Message = checkpointMessage(agent, "checkpoint")
	}
	op := model.WorkspaceOp{Status: "no_changes", Team: team, Agent: agent}
	for _, ws := range all {
		committed, err := workspace.Checkpoint(r.Context(), ws.WorktreePath, in.Message)
		if err != nil {
			respond(w, nil, err)
			return
		}
		status := "no_changes"
		if committed {
			status, op.Status = "checkpoint_created", "checkpoint_created"
		}
		op.Repos = append(op.Repos, model.WorkspaceRepo{Repo: ws.RepoRoot, Branch: ws.BranchName, Path: ws.WorktreePath, Status: status})
	}
	respond(w, op, nil)
}

// mergeWorkspace merges an agent's branches into their base branches, all
// or none: see workspace.Merge. Each worktree is checkpointed first, and
// after a merge the worktrees and branches go unless NoCleanup is set.
func (s *Server) mergeWorkspace(w http.ResponseWriter, r *http.Request) {
	team, agent := r.PathValue("team"), r.PathValue("agent")
	var in model.WorkspaceRequest
	if !decode(w, r, &in) {
		return
	}
	all, err := s.agentWorkspaces(r.Context(), team, agent, in.Repo)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var items []workspace.MergeItem
	for _, ws := range all {
		if _, err := workspace.Checkpoint(r.Context(), ws.WorktreePath, checkpointMessage(agent, "pre-merge checkpoint")); err != nil {
			respond(w, nil, err)
			return
		}
		target := in.Target
		if target == "" {
			target = ws.BaseBranch
		}
		items = append(items, workspace.MergeItem{Root: ws.RepoRoot, Branch: ws.BranchName, Target: target, Label: repoLabel(ws)})
	}
	output, ok, err := workspace.Merge(r.Context(), items)
	if err != nil {
		respond(w, nil, err)
		return
	}
	op := model.WorkspaceOp{Status: "merged", Team: team, Agent: agent, Output: output}
	if !ok {
		op.Status = "merge_failed"
		respond(w, op, nil)
		return
	}
	s.log.Info("workspace merged", "team", team, "agent", agent, "repos", len(items))
	if !in.NoCleanup {
		for i, ws := range all {
			op.Repos = append(op.Repos, s.removeWorkspace(r.Context(), ws, items[i].Target, false))
		}
	}
	respond(w, op, nil)
}

// removeWorkspace deletes a worktree and its record. The branch goes too
// when target already contains it; otherwise it is kept so no commit is
// lost. checkpoint commits what is uncommitted first.
func (s *Server) removeWorkspace(ctx context.Context, ws model.Workspace, target string, checkpoint bool) model.WorkspaceRepo {
	res := model.WorkspaceRepo{Repo: ws.RepoRoot, Branch: ws.BranchName, Path: ws.WorktreePath, Status: "removed"}
	var notes []string
	if checkpoint {
		if _, err := os.Stat(ws.WorktreePath); err == nil {
			if _, err := workspace.Checkpoint(ctx, ws.WorktreePath, checkpointMessage(ws.AgentName, "final checkpoint")); err != nil {
				notes = append(notes, "checkpoint failed: "+err.Error())
			}
		}
	}
	if err := workspace.Remove(ctx, ws.RepoRoot, ws.WorktreePath); err != nil {
		res.Status, res.Output = "failed", err.Error()
		return res
	}
	if gone, err := workspace.DeleteBranchIfMerged(ctx, ws.RepoRoot, ws.BranchName, target); err != nil {
		notes = append(notes, "branch: "+err.Error())
	} else if !gone {
		notes = append(notes, fmt.Sprintf("branch %s kept: it has commits %s does not", ws.BranchName, target))
	}
	if _, err := s.store.DeleteWorkspace(ctx, ws.TeamName, ws.AgentName, ws.RepoRoot); err != nil {
		notes = append(notes, "record: "+err.Error())
	}
	// A multi-repository workspace directory is empty once its last
	// worktree is gone.
	os.Remove(filepath.Dir(ws.WorktreePath))
	res.Output = strings.Join(notes, "; ")
	return res
}

func (s *Server) cleanupWorkspaces(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	var in model.WorkspaceRequest
	if !decode(w, r, &in) {
		return
	}
	var all []model.Workspace
	var err error
	if in.Agent != "" {
		all, err = s.agentWorkspaces(r.Context(), team, in.Agent, in.Repo)
	} else {
		all, err = s.store.ListWorkspaces(r.Context(), team, "")
	}
	if err != nil {
		respond(w, nil, err)
		return
	}
	op := model.WorkspaceOp{Status: "cleaned", Team: team, Agent: in.Agent}
	for _, ws := range all {
		res := s.removeWorkspace(r.Context(), ws, ws.BaseBranch, true)
		if res.Status == "removed" {
			op.Count++
		}
		op.Repos = append(op.Repos, res)
	}
	respond(w, op, nil)
}

// cleanupTeamWorkspaces removes a team's worktrees before the team is
// deleted, keeping branches with unmerged work. Failures are logged: they
// do not keep the team from going.
func (s *Server) cleanupTeamWorkspaces(ctx context.Context, team string) {
	all, err := s.store.ListWorkspaces(ctx, team, "")
	if err != nil {
		return
	}
	for _, ws := range all {
		res := s.removeWorkspace(ctx, ws, ws.BaseBranch, true)
		if res.Status != "removed" || res.Output != "" {
			s.log.Warn("workspace cleanup", "team", team, "agent", ws.AgentName, "repo", ws.RepoRoot,
				"status", res.Status, "detail", res.Output)
		}
	}
}

// importWorkspaces records the worktrees in a ClawTeam workspace registry,
// by default ~/.clawteam/workspaces/<team>/workspace-registry.json on this
// machine, so a team moved from ClawTeam keeps its agents' worktrees.
// Entries whose worktree is gone are skipped.
func (s *Server) importWorkspaces(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	var in model.WorkspaceRequest
	if !decode(w, r, &in) {
		return
	}
	file := in.File
	if file == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			respond(w, nil, err)
			return
		}
		file = filepath.Join(home, ".clawteam", "workspaces", team, "workspace-registry.json")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		respond(w, nil, &store.Error{Kind: store.KindNotFound, Msg: "read ClawTeam workspace registry: " + err.Error()})
		return
	}
	var registry struct {
		Workspaces []model.Workspace `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		respond(w, nil, &store.Error{Kind: store.KindInvalid, Msg: fmt.Sprintf("parse %s: %v", file, err)})
		return
	}
	op := model.WorkspaceOp{Status: "imported", Team: team}
	for _, ws := range registry.Workspaces {
		res := model.WorkspaceRepo{Repo: ws.RepoRoot, Branch: ws.BranchName, Path: ws.WorktreePath, Status: "imported"}
		root, err := workspace.MainRoot(r.Context(), ws.RepoRoot)
		if err != nil || !workspace.IsWorktreeOf(r.Context(), ws.WorktreePath, root) {
			res.Status, res.Output = "skipped", "not a worktree of "+ws.RepoRoot
			op.Repos = append(op.Repos, res)
			continue
		}
		ws.TeamName, ws.RepoRoot = team, root
		if _, err := s.store.SaveWorkspace(r.Context(), ws); err != nil {
			respond(w, nil, err)
			return
		}
		op.Count++
		op.Repos = append(op.Repos, res)
	}
	respond(w, op, nil)
}

// repoRoots checks that each path is in a git repository on this machine
// and returns the repositories' main worktrees, without duplicates.
func repoRoots(ctx context.Context, paths []string) ([]string, error) {
	var roots []string
	seen := map[string]bool{}
	for _, p := range paths {
		root, err := workspace.MainRoot(ctx, p)
		if err != nil {
			return nil, &store.Error{Kind: store.KindInvalid, Msg: err.Error()}
		}
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	return roots, nil
}

// setTeamRepos replaces the repositories spawn gives the team's agents
// worktrees of when it is given none; an empty list clears them.
func (s *Server) setTeamRepos(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repos []string `json:"repos"`
	}
	if !decode(w, r, &in) {
		return
	}
	roots, err := repoRoots(r.Context(), in.Repos)
	if err != nil {
		respond(w, nil, err)
		return
	}
	team, err := s.store.SetTeamRepos(r.Context(), r.PathValue("team"), roots)
	respond(w, team, err)
}
