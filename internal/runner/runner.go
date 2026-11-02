// Package runner starts agents on the daemon's machine: spawn, resume and
// launch. team-workerd runs it for every client, over the socket or HTTP,
// so a CLI never starts agents itself.
package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/agent"
	"github.com/benlocal/team-worker/internal/agent/builtin"
	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/spawn"
	"github.com/benlocal/team-worker/internal/store"
)

// Env is what starting agents takes from the daemon: its API, called in
// process, its data directory and the team-worker CLI the agents' hooks and
// supervisors run.
type Env struct {
	Client    *client.Client
	DataDir   string
	WorkerBin string
}

// The request and result types are the API's, in model.
type (
	Request   = model.SpawnRequest
	Result    = model.SpawnResult
	EnvOrigin = model.EnvOrigin
)

// requireBudget refuses new work when the team's enforced budget is spent.
func requireBudget(ctx context.Context, c *client.Client, team string) error {
	b, err := c.Budget(ctx, team)
	if err != nil || !b.Exhausted {
		return err
	}
	return fmt.Errorf("team budget is exhausted (%s of %s); raise it with team-worker cost budget %s <dollars>",
		dollars(b.SpentCents), dollars(b.BudgetCents), team)
}

func dollars(cents float64) string { return fmt.Sprintf("$%.2f", cents/100) }

// Spawn registers the agent in its team (creating the team with the
// agent as leader when needed), prepares its workspace and prompt, starts
// it and records the process with the daemon. Everything that can fail is
// checked before the team is changed, and a dry run stops there.
func Spawn(ctx context.Context, e Env, req Request) (_ Result, err error) {
	c := e.Client
	if req.AgentType == "" {
		req.AgentType = "general-purpose"
	}
	registry, err := builtin.Load(config.AgentsDir())
	if err != nil {
		return Result{}, err
	}
	adapter := registry.For(req.Command)
	if req.MaxRestarts < 0 || req.RestartDelay < 0 || req.RestartDelay > time.Minute {
		return Result{}, fmt.Errorf("restart limits must not be negative")
	}
	if req.RestartDelay == 0 {
		req.RestartDelay = time.Second
	}
	if req.KeepAlive && agent.SessionClient(req.Command) == "" {
		return Result{}, fmt.Errorf("keepalive is not supported by this CLI")
	}
	req.RunID = store.NewAgentID()
	nativeCommand, clientName, nativeID, err := agent.StartSessionCommand(req.Command)
	if req.ResumeID != "" {
		nativeCommand, err = agent.ResumeCommand(req.Command, req.ResumeID, req.Backend == "tmux")
		nativeID = req.ResumeID
	}
	if err != nil {
		return Result{}, err
	}

	team, err := c.GetTeam(ctx, req.Team)
	teamExists := err == nil
	if err != nil && !client.IsNotFound(err) {
		return Result{}, err
	}
	if teamExists && !req.DryRun {
		if err := requireBudget(ctx, c, req.Team); err != nil {
			return Result{}, err
		}
	}
	member := findMember(team, req.Name, req.User)
	if member != nil && !req.TaskSet && req.Task == "" {
		if req.Task, err = c.AgentPrompt(ctx, req.Team, req.Name, req.User); err != nil {
			return Result{}, err
		}
	}
	if len(req.Task) > store.MaxAgentPromptLength {
		return Result{}, fmt.Errorf("Agent instructions must be at most %d bytes", store.MaxAgentPromptLength)
	}
	if member != nil {
		req.AgentID = member.AgentID
	} else if req.AgentID == "" {
		req.AgentID = store.NewAgentID()
	}
	leader := leaderOf(team)
	if leader == "" {
		leader = req.Name
	}
	teamPrompt := ""
	if teamExists {
		if teamPrompt, err = c.TeamPrompt(ctx, req.Team); err != nil {
			return Result{}, err
		}
	}

	dataDir := e.DataDir
	planned, cwd, err := planWorkspaces(ctx, c, dataDir, req, team.Repos)
	if err != nil {
		return Result{}, err
	}
	var worktrees []spawn.Worktree
	for _, p := range planned {
		worktrees = append(worktrees, spawn.Worktree{Path: p.WorktreePath, Repo: p.RepoRoot, Branch: p.BranchName, Base: p.BaseBranch})
	}
	// As in ClawTeam, an agent with worktrees starts knowing what the
	// others' work touches; the context is a courtesy, so it never fails a spawn.
	gitContext := ""
	if teamExists && len(planned) > 0 {
		if text, err := c.ContextInject(ctx, req.Team, req.Name, ""); err == nil && !strings.HasPrefix(text, "No cross-agent context") {
			gitContext = text
		}
	}
	prompt := spawn.BuildPrompt(spawn.PromptParams{
		AgentName: req.Name, AgentID: req.AgentID, AgentType: req.AgentType, Team: req.Team,
		Leader: leader, User: req.User, Workspace: cwd, Worktrees: worktrees, TeamPrompt: teamPrompt, Task: req.Task,
		Context: gitContext, PlanRequired: req.PlanRequired,
	})
	identity := map[string]string{
		"TEAM_WORKER_AGENT_ID":      req.AgentID,
		"TEAM_WORKER_RUN_ID":        req.RunID,
		"TEAM_WORKER_AGENT_NAME":    req.Name,
		"TEAM_WORKER_AGENT_TYPE":    req.AgentType,
		"TEAM_WORKER_TEAM_NAME":     req.Team,
		"TEAM_WORKER_AGENT_LEADER":  boolEnv(req.Name == leader),
		"TEAM_WORKER_DATA_DIR":      dataDir,
		"TEAM_WORKER_SOCKET":        config.SocketPath(dataDir),
		"TEAM_WORKER_WORKSPACE_DIR": cwd,
		"TEAM_WORKER_BIN":           e.WorkerBin,
	}
	if req.PlanRequired {
		identity["TEAM_WORKER_PLAN_MODE_REQUIRED"] = "1"
	}
	if req.User != "" {
		identity["TEAM_WORKER_USER"] = req.User
	}
	built, err := spawn.Build(spawn.Options{
		Backend: req.Backend, Command: nativeCommand, RunID: req.RunID, KeepAlive: req.KeepAlive, Adapter: adapter, Team: req.Team, Agent: req.Name,
		Prompt: prompt, Cwd: cwd, SkipPermissions: req.SkipPerms, IsLeader: req.Name == leader,
		EnvLayers: req.EnvLayers, Identity: identity, WorkerBin: e.WorkerBin,
		RunDir: filepath.Join(dataDir, "run"), LogPath: config.AgentLogPath(dataDir, req.Team, req.Name),
	})
	if err != nil {
		return Result{}, err
	}
	result := Result{Backend: req.Backend, AgentName: req.Name, AgentID: req.AgentID, Workspace: cwd,
		Command: req.Command, Adapter: adapter.Name() + " (" + adapter.Source() + ")", Workspaces: workspacesOf(planned)}
	if req.DryRun {
		result.Status = "dry-run"
		result.Args = HidePrompt(built.Plan.Args, prompt)
		result.Prompt = prompt
		for name, origin := range built.Env.Origin {
			result.Env = append(result.Env, EnvOrigin{Name: name, Origin: origin})
		}
		sort.Slice(result.Env, func(i, j int) bool { return result.Env[i].Name < result.Env[j].Name })
		return result, nil
	}

	if !teamExists {
		if _, err := c.CreateTeam(ctx, store.CreateTeamInput{
			Name: req.Team, Description: "Created by team-worker spawn", LeaderName: req.Name,
			LeaderID: req.AgentID, User: req.User, LeaderAgentType: req.AgentType,
		}); err != nil {
			return Result{}, err
		}
	} else if member == nil {
		if _, err := c.AddMember(ctx, req.Team, store.AddMemberInput{
			Name: req.Name, AgentID: req.AgentID, AgentType: req.AgentType, User: req.User}); err != nil {
			return Result{}, err
		}
	}
	// The flag is only ever turned on here, so a member added with
	// --plan-required keeps it when spawned again without the flag.
	if req.PlanRequired {
		if err := c.SetPlanRequired(ctx, req.Team, req.Name, req.User, true); err != nil {
			return Result{}, err
		}
	}
	if existing, err := c.GetAgent(ctx, req.Team, req.Name); err == nil && isRunning(existing) {
		if !req.Replace {
			return Result{}, fmt.Errorf(
				"Agent '%s' is already running in team '%s'. Use --replace to stop it and spawn a new instance.",
				req.Name, req.Team)
		}
		if _, err := c.RequestAgentStop(ctx, req.Team, req.Name, existing.RunID); err != nil {
			return Result{}, err
		}
		if err := proc.Stop(existing); err != nil {
			return Result{}, fmt.Errorf("stop running agent '%s': %w", req.Name, err)
		}
		if _, err := c.AgentExitedRun(ctx, req.Team, req.Name, existing.RunID, 0); err != nil {
			return Result{}, err
		}
	} else if err == nil {
		// A pane kept open after a failed start; the new instance replaces it.
		if err := proc.Stop(existing); err != nil {
			return Result{}, err
		}
		if _, err := c.AgentExitedRun(ctx, req.Team, req.Name, existing.RunID, existing.ExitCode); err != nil {
			return Result{}, err
		}
	} else if !client.IsNotFound(err) {
		return Result{}, err
	}
	rollback, err := createWorkspaces(ctx, c, planned)
	if err != nil {
		return Result{}, err
	}
	// Worktrees created for an agent that then fails to start go again.
	defer func() {
		if err != nil {
			rollback()
		}
	}()
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		return Result{}, err
	}
	if req.TaskSet || req.Task != "" {
		if _, err := c.SetAgentPrompt(ctx, req.Team, req.Name, req.User, req.Task); err != nil {
			return Result{}, err
		}
	}

	rec := model.Agent{Team: req.Team, Name: req.Name, AgentID: req.AgentID, RunID: req.RunID, Backend: req.Backend,
		Command: req.Command, Cwd: cwd, Nudge: req.Backend == "tmux" && adapter.Nudge(), Host: proc.Host()}
	if _, err := c.RegisterAgent(ctx, rec); err != nil {
		return Result{}, err
	}
	if clientName != "" {
		keys := append([]string{}, req.RequiredEnvKeys...)
		for _, layer := range req.EnvLayers {
			for key := range layer.Set {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		session := model.Session{Team: req.Team, Agent: req.Name, User: req.User, RunID: req.RunID, Client: clientName,
			SessionID: nativeID, Source: "launch", Cwd: cwd, Command: req.Command, Backend: req.Backend,
			SkipPermissions: req.SkipPerms, KeepAlive: req.KeepAlive, MaxRestarts: req.MaxRestarts,
			RestartDelayMillis: req.RestartDelay.Milliseconds(), EnvKeys: keys}
		if _, err := c.SaveSession(ctx, session); err != nil {
			if _, cleanupErr := c.AgentExitedRun(ctx, req.Team, req.Name, req.RunID, -1); cleanupErr != nil {
				return Result{}, fmt.Errorf("save session: %w; release reservation: %v", err, cleanupErr)
			}
			return Result{}, err
		}
	}
	res, err := built.Start()
	if err != nil {
		if _, cleanupErr := c.AgentExitedRun(ctx, req.Team, req.Name, req.RunID, -1); cleanupErr != nil {
			return Result{}, fmt.Errorf("start agent: %w; release reservation: %v", err, cleanupErr)
		}
		return Result{}, err
	}
	rec.Target, rec.PID = res.Target, res.PID
	if _, err := c.RegisterAgent(ctx, rec); err != nil {
		if stopErr := proc.Stop(rec); stopErr != nil {
			return Result{}, fmt.Errorf("publish agent: %w; stop child: %v", err, stopErr)
		}
		return Result{}, err
	}
	current, err := c.GetAgent(ctx, req.Team, req.Name)
	if err != nil {
		return Result{}, err
	}
	if current.RunID != req.RunID || current.StopRequested {
		if err := proc.Stop(rec); err != nil {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("agent launch was stopped or replaced")
	}
	if !req.KeepAlive {
		res.FinishStartup()
	}
	result.Status, result.Target, result.PID = "spawned", res.Target, res.PID
	return result, nil
}

// HidePrompt shortens the prompt and hook settings arguments so dry-run
// output stays readable.
func HidePrompt(args []string, prompt string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		switch {
		case a == prompt:
			a = fmt.Sprintf("<prompt: %d chars>", len(prompt))
		case strings.HasPrefix(a, `{"hooks":`):
			a = "<team-worker hooks: see `team-worker hook settings`>"
		}
		out[i] = a
	}
	return out
}

func findMember(t model.Team, name, user string) *model.Member {
	for i, m := range t.Members {
		if m.Name == name && m.User == user {
			return &t.Members[i]
		}
	}
	return nil
}

func leaderOf(t model.Team) string {
	for _, m := range t.Members {
		if m.AgentID == t.LeadAgentID {
			return m.Name
		}
	}
	if len(t.Members) > 0 {
		return t.Members[0].Name
	}
	return ""
}

// isRunning treats an agent as running unless a probe has shown it dead.
func isRunning(a model.Agent) bool {
	return a.Status == model.AgentRunning && (a.Alive == nil || *a.Alive)
}

func boolEnv(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
