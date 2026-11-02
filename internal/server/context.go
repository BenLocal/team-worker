package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
	"github.com/benlocal/team-worker/internal/workspace"
)

// ClawTeam's context commands: what the agents' worktrees change, read
// from git on the daemon's machine.

// agentWork is one agent's work in one repository.
type agentWork struct {
	ws   model.Workspace
	head string
	// base is where the agent's own work starts: its base branch (or that
	// branch's upstream when it is ahead), or the last commit it checked
	// out rather than made.
	base string
}

// teamWork returns the team's worktrees with the commits they are at,
// limited to repo when it is set, and whether they span several
// repositories, in which case files are named "<repo>/<path>".
func (s *Server) teamWork(ctx context.Context, team, repo string) ([]agentWork, bool, error) {
	all, err := s.store.ListWorkspaces(ctx, team, "")
	if err != nil {
		return nil, false, err
	}
	root := ""
	if repo != "" {
		root, _ = workspace.MainRoot(ctx, repo)
	}
	var works []agentWork
	roots := map[string]bool{}
	for _, ws := range all {
		if repo != "" && ws.RepoRoot != repo && ws.RepoRoot != root && filepath.Base(ws.RepoRoot) != repo {
			continue
		}
		roots[ws.RepoRoot] = true
		head := workspace.Head(ctx, ws.RepoRoot, ws.WorktreePath, ws.BranchName)
		base := workspace.CurrentBase(ctx, ws.RepoRoot, ws.BaseBranch)
		works = append(works, agentWork{ws: ws, head: head, base: workspace.OwnStart(ctx, ws.RepoRoot, ws.WorktreePath, base, head)})
	}
	return works, len(roots) > 1, nil
}

func fileName(w agentWork, file string, multi bool) string {
	if multi {
		return repoLabel(w.ws) + "/" + file
	}
	return file
}

// agentDiff adds up an agent's work across its repositories.
func agentDiff(ctx context.Context, works []agentWork, agent string, multi bool) (model.AgentDiff, error) {
	d := model.AgentDiff{FilesChanged: []string{}}
	var stats []string
	for _, w := range works {
		if w.ws.AgentName != agent {
			continue
		}
		diff, err := workspace.BranchDiff(ctx, w.ws.RepoRoot, w.base, w.head)
		if err != nil {
			return d, err
		}
		for _, f := range diff.Files {
			d.FilesChanged = append(d.FilesChanged, fileName(w, f, multi))
		}
		d.Insertions += diff.Insertions
		d.Deletions += diff.Deletions
		d.CommitCount += diff.CommitCount
		d.UncommittedFiles += workspace.Uncommitted(ctx, w.ws.WorktreePath)
		if diff.Stat != "" {
			if multi {
				diff.Stat = repoLabel(w.ws) + ":\n" + diff.Stat
			}
			stats = append(stats, diff.Stat)
		}
	}
	d.DiffStat = strings.Join(stats, "\n")
	d.Summary = fmt.Sprintf("%s: %d file(s), +%d/-%d, %d commit(s)", agent, len(d.FilesChanged), d.Insertions, d.Deletions, d.CommitCount)
	return d, nil
}

// fileOwner is an agent that changed a file, with its work in that file's
// repository.
type fileOwner struct {
	agent string
	work  agentWork
	file  string // path inside the repository
}

// fileOwners maps each changed file to the agents that changed it, in the
// order the worktrees were created.
func fileOwners(ctx context.Context, works []agentWork, multi bool) (map[string][]fileOwner, error) {
	owners := map[string][]fileOwner{}
	for _, w := range works {
		diff, err := workspace.BranchDiff(ctx, w.ws.RepoRoot, w.base, w.head)
		if err != nil {
			return nil, err
		}
		for _, f := range diff.Files {
			name := fileName(w, f, multi)
			if !slices.ContainsFunc(owners[name], func(o fileOwner) bool { return o.agent == w.ws.AgentName }) {
				owners[name] = append(owners[name], fileOwner{agent: w.ws.AgentName, work: w, file: f})
			}
		}
	}
	return owners, nil
}

func ownerNames(list []fileOwner) []string {
	names := make([]string, len(list))
	for i, o := range list {
		names[i] = o.agent
	}
	return names
}

// overlaps lists the files several agents changed, high severity first.
func overlaps(ctx context.Context, works []agentWork, multi bool) ([]model.FileOverlap, error) {
	owners, err := fileOwners(ctx, works, multi)
	if err != nil {
		return nil, err
	}
	out := []model.FileOverlap{}
	for name, list := range owners {
		if len(list) < 2 {
			continue
		}
		lines := make([]map[int]bool, len(list))
		for i, o := range list {
			if lines[i], err = workspace.ChangedLines(ctx, o.work.ws.RepoRoot, o.work.base, o.work.head, o.file); err != nil {
				return nil, err
			}
		}
		severity := "medium"
	pairs:
		for i := range lines {
			for j := i + 1; j < len(lines); j++ {
				for n := range lines[i] {
					if lines[j][n] {
						severity = "high"
						break pairs
					}
				}
			}
		}
		out = append(out, model.FileOverlap{File: name, Agents: ownerNames(list), Severity: severity})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == "high"
		}
		return out[i].File < out[j].File
	})
	return out, nil
}

// crossLog merges the agents' commits, newest first.
func crossLog(ctx context.Context, works []agentWork, multi bool, limit int) ([]model.AgentCommit, error) {
	out := []model.AgentCommit{}
	for _, w := range works {
		commits, err := workspace.Log(ctx, w.ws.RepoRoot, w.base, w.head)
		if err != nil {
			return nil, err
		}
		for _, c := range commits {
			files := []string{}
			for _, f := range c.Files {
				files = append(files, fileName(w, f, multi))
			}
			out = append(out, model.AgentCommit{Agent: w.ws.AgentName, Hash: c.Hash, Message: c.Message, Timestamp: c.Timestamp, Files: files})
		}
	}
	at := func(c model.AgentCommit) time.Time {
		t, _ := time.Parse(time.RFC3339, c.Timestamp)
		return t
	}
	sort.SliceStable(out, func(i, j int) bool { return at(out[i]).After(at(out[j])) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// noContext is ClawTeam's text when there is nothing to inject.
const noContext = "No cross-agent context to inject — working in isolation."

// injectContext is ClawTeam's context block for an agent: files others
// also changed, their recent commits on those files, and the work of the
// agents whose tasks the agent's tasks wait for.
func (s *Server) injectContext(ctx context.Context, team, agent, repo string) (string, error) {
	works, multi, err := s.teamWork(ctx, team, repo)
	if err != nil {
		return "", err
	}
	target, err := agentDiff(ctx, works, agent, multi)
	if err != nil {
		return "", err
	}
	mine := map[string]bool{}
	for _, f := range target.FilesChanged {
		mine[f] = true
	}
	var sections []string

	owners, err := fileOwners(ctx, works, multi)
	if err != nil {
		return "", err
	}
	var warnings []string
	for name, list := range owners {
		if !mine[name] || len(list) < 2 {
			continue
		}
		var others []string
		for _, o := range list {
			if o.agent != agent {
				others = append(others, o.agent)
			}
		}
		if len(others) > 0 {
			warnings = append(warnings, fmt.Sprintf("- `%s` also modified by: %s", name, strings.Join(others, ", ")))
		}
	}
	if len(warnings) > 0 {
		sort.Strings(warnings)
		sections = append(sections, "## File Overlap Warnings\n"+strings.Join(warnings, "\n"))
	}

	log, err := crossLog(ctx, works, multi, 20)
	if err != nil {
		return "", err
	}
	var related []string
	for _, c := range log {
		if c.Agent == agent {
			continue
		}
		var common []string
		for _, f := range c.Files {
			if mine[f] {
				common = append(common, f)
			}
		}
		if len(common) > 0 {
			related = append(related, fmt.Sprintf("- [%s] %s %s (files: %s)", c.Agent, c.Hash[:min(len(c.Hash), 8)], c.Message, strings.Join(common, ", ")))
		}
	}
	if len(related) > 0 {
		sections = append(sections, "## Recent Related Changes\n"+strings.Join(related, "\n"))
	}

	tasks, err := s.store.ListTasks(ctx, team, store.TaskFilter{})
	if err != nil {
		return "", err
	}
	waitsFor := map[string]bool{}
	for _, t := range tasks {
		if t.Owner == agent {
			for _, id := range t.BlockedBy {
				waitsFor[id] = true
			}
		}
	}
	upstream := map[string]bool{}
	for _, t := range tasks {
		if waitsFor[t.ID] && t.Owner != "" && t.Owner != agent {
			upstream[t.Owner] = true
		}
	}
	if len(upstream) > 0 {
		names := make([]string, 0, len(upstream))
		for name := range upstream {
			names = append(names, name)
		}
		sort.Strings(names)
		lines := []string{"## Upstream Dependency Changes"}
		for _, name := range names {
			d, err := agentDiff(ctx, works, name, multi)
			if err != nil {
				return "", err
			}
			lines = append(lines, "- "+name+": "+d.Summary)
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	if len(sections) == 0 {
		return noContext, nil
	}
	return "# Git Context for " + agent + "\n" + strings.Join(sections, "\n\n"), nil
}

func (s *Server) contextDiff(w http.ResponseWriter, r *http.Request) {
	team, agent, repo := r.PathValue("team"), r.PathValue("agent"), r.URL.Query().Get("repo")
	if _, err := s.agentWorkspaces(r.Context(), team, agent, repo); err != nil {
		respond(w, nil, err)
		return
	}
	works, multi, err := s.teamWork(r.Context(), team, repo)
	if err != nil {
		respond(w, nil, err)
		return
	}
	d, err := agentDiff(r.Context(), works, agent, multi)
	respond(w, d, err)
}

func (s *Server) contextFiles(w http.ResponseWriter, r *http.Request) {
	works, multi, err := s.teamWork(r.Context(), r.PathValue("team"), r.URL.Query().Get("repo"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	owners, err := fileOwners(r.Context(), works, multi)
	out := map[string][]string{}
	for name, list := range owners {
		out[name] = ownerNames(list)
	}
	respond(w, out, err)
}

func (s *Server) contextConflicts(w http.ResponseWriter, r *http.Request) {
	works, multi, err := s.teamWork(r.Context(), r.PathValue("team"), r.URL.Query().Get("repo"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	out, err := overlaps(r.Context(), works, multi)
	respond(w, out, err)
}

func (s *Server) contextLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}
	works, multi, err := s.teamWork(r.Context(), r.PathValue("team"), q.Get("repo"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	out, err := crossLog(r.Context(), works, multi, limit)
	respond(w, out, err)
}

func (s *Server) contextInject(w http.ResponseWriter, r *http.Request) {
	text, err := s.injectContext(r.Context(), r.PathValue("team"), r.PathValue("agent"), r.URL.Query().Get("repo"))
	respond(w, map[string]string{"context": text}, err)
}
