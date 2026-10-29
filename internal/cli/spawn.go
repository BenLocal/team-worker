package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/spawn"
	"github.com/benlocal/team-worker/internal/store"
)

type spawnRequest struct {
	Team      string
	Name      string
	AgentID   string
	AgentType string
	Task      string
	Backend   string
	Command   []string
	Cwd       string
	Env       map[string]string
	SkipPerms bool
	Replace   bool
}

type spawnResult struct {
	Status    string   `json:"status"`
	Backend   string   `json:"backend"`
	AgentName string   `json:"agentName"`
	AgentID   string   `json:"agentId"`
	Target    string   `json:"target"`
	PID       int      `json:"pid"`
	Workspace string   `json:"workspace"`
	Command   []string `json:"command"`
}

// spawnAgent registers the agent in its team (creating the team with the
// agent as leader when needed), prepares its workspace and prompt, starts
// it and records the process with the daemon.
func (a *app) spawnAgent(ctx context.Context, req spawnRequest) (spawnResult, error) {
	c := a.client()
	id := config.IdentityFromEnv()
	if req.AgentType == "" {
		req.AgentType = "general-purpose"
	}
	if req.AgentID == "" {
		req.AgentID = store.NewAgentID()
	}

	team, err := c.GetTeam(ctx, req.Team)
	if client.IsNotFound(err) {
		team, err = c.CreateTeam(ctx, store.CreateTeamInput{
			Name: req.Team, Description: "Created by team-worker spawn", LeaderName: req.Name,
			LeaderID: req.AgentID, User: id.User, LeaderAgentType: req.AgentType,
		})
	}
	if err != nil {
		return spawnResult{}, err
	}
	member := findMember(team, req.Name, id.User)
	if member == nil {
		m, err := c.AddMember(ctx, req.Team, store.AddMemberInput{
			Name: req.Name, AgentID: req.AgentID, AgentType: req.AgentType, User: id.User})
		if err != nil {
			return spawnResult{}, err
		}
		member = &m
	}
	req.AgentID = member.AgentID
	leader := leaderOf(team)
	if leader == "" {
		leader = req.Name
	}

	if existing, err := c.GetAgent(ctx, req.Team, req.Name); err == nil && isRunning(existing) {
		if !req.Replace {
			return spawnResult{}, fmt.Errorf(
				"Agent '%s' is already running in team '%s'. Use --replace to stop it and spawn a new instance.",
				req.Name, req.Team)
		}
		if err := proc.Stop(existing); err != nil {
			return spawnResult{}, fmt.Errorf("stop running agent '%s': %w", req.Name, err)
		}
		if _, err := c.AgentExited(ctx, req.Team, req.Name); err != nil {
			return spawnResult{}, err
		}
	} else if err != nil && !client.IsNotFound(err) {
		return spawnResult{}, err
	}

	dataDir := config.DataDir()
	cwd := req.Cwd
	if cwd == "" {
		cwd = config.WorkspaceDir(dataDir, req.Team, req.Name)
	}
	if cwd, err = filepath.Abs(cwd); err != nil {
		return spawnResult{}, err
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		return spawnResult{}, err
	}

	teamPrompt, err := c.TeamPrompt(ctx, req.Team)
	if err != nil {
		return spawnResult{}, err
	}
	prompt := spawn.BuildPrompt(spawn.PromptParams{
		AgentName: req.Name, AgentID: req.AgentID, AgentType: req.AgentType, Team: req.Team,
		Leader: leader, User: id.User, Workspace: cwd, TeamPrompt: teamPrompt, Task: req.Task,
	})

	env := map[string]string{
		"TEAM_WORKER_AGENT_ID":      req.AgentID,
		"TEAM_WORKER_AGENT_NAME":    req.Name,
		"TEAM_WORKER_AGENT_TYPE":    req.AgentType,
		"TEAM_WORKER_TEAM_NAME":     req.Team,
		"TEAM_WORKER_AGENT_LEADER":  boolEnv(req.Name == leader),
		"TEAM_WORKER_DATA_DIR":      dataDir,
		"TEAM_WORKER_SOCKET":        config.SocketPath(dataDir),
		"TEAM_WORKER_WORKSPACE_DIR": cwd,
	}
	if id.User != "" {
		env["TEAM_WORKER_USER"] = id.User
	}
	for k, v := range req.Env {
		env[k] = v
	}

	res, err := spawn.Launch(spawn.Options{
		Backend: req.Backend, Command: req.Command, Team: req.Team, Agent: req.Name, Prompt: prompt,
		Cwd: cwd, SkipPermissions: req.SkipPerms, IsLeader: req.Name == leader, Env: env,
		WorkerBin: workerBinary(), RunDir: filepath.Join(dataDir, "run"),
		LogPath: config.AgentLogPath(dataDir, req.Team, req.Name),
	})
	if err != nil {
		return spawnResult{}, err
	}
	agent := model.Agent{Team: req.Team, Name: req.Name, AgentID: req.AgentID, Backend: res.Backend,
		Target: res.Target, PID: res.PID, Command: req.Command, Cwd: cwd}
	if _, err := c.RegisterAgent(ctx, agent); err != nil {
		proc.Stop(agent)
		return spawnResult{}, err
	}
	res.FinishStartup()
	return spawnResult{Status: "spawned", Backend: res.Backend, AgentName: req.Name, AgentID: req.AgentID,
		Target: res.Target, PID: res.PID, Workspace: cwd, Command: req.Command}, nil
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

// workerBinary is the absolute path of this CLI, used for the exit hook.
func workerBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

func parseEnv(pairs []string) (map[string]string, error) {
	env := map[string]string{}
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --env %q: use KEY=VALUE", kv)
		}
		env[k] = v
	}
	return env, nil
}

func (a *app) spawnCmd() *cobra.Command {
	var req spawnRequest
	var envPairs []string
	var noSkip bool
	cmd := &cobra.Command{
		Use:   "spawn [tmux|subprocess] [-- COMMAND ARGS...]",
		Short: "Start an agent with its identity, workspace and task",
		Long: "Start an agent CLI (default: claude) for a team member. The agent gets the TEAM_WORKER_*\n" +
			"identity variables, its own workspace directory and a first prompt with its task and the\n" +
			"coordination protocol. Put the agent command after --, e.g.\n" +
			"  team-worker spawn tmux --team demo -n alice --task \"...\" -- codex --model gpt-5",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req.Backend = "tmux"
			before := args
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				before, req.Command = args[:dash], args[dash:]
			}
			switch len(before) {
			case 0:
			case 1:
				req.Backend = before[0]
			default:
				return a.fail(fmt.Errorf("unexpected arguments %q; put the agent command after --", before[1:]))
			}
			if req.Backend != "tmux" && req.Backend != "subprocess" {
				return a.fail(fmt.Errorf("unknown backend '%s': use tmux or subprocess "+
					"(put the agent command after --)", req.Backend))
			}
			if len(req.Command) == 0 {
				req.Command = []string{"claude"}
			}
			if req.Team == "" {
				req.Team = config.IdentityFromEnv().TeamName
			}
			if req.Team == "" {
				return a.fail(fmt.Errorf("--team is required"))
			}
			if req.Name == "" {
				req.Name = "agent-" + store.NewAgentID()[:6]
			}
			var err error
			if req.Env, err = parseEnv(envPairs); err != nil {
				return a.fail(err)
			}
			req.SkipPerms = !noSkip
			res, err := a.spawnAgent(cmd.Context(), req)
			if err != nil {
				return a.fail(err)
			}
			a.emit(res, func(w io.Writer) { printSpawned(w, req.Team, res) })
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&req.Team, "team", "t", "", "Team name (default: TEAM_WORKER_TEAM_NAME)")
	f.StringVarP(&req.Name, "agent-name", "n", "", "Agent name (default: random)")
	f.StringVar(&req.AgentType, "agent-type", "general-purpose", "Agent type")
	f.StringVar(&req.Task, "task", "", "Task for the agent's first prompt")
	f.StringVar(&req.Cwd, "cwd", "", "Working directory (default: <data-dir>/workspaces/<team>/<agent>)")
	f.StringArrayVar(&envPairs, "env", nil, "Extra environment variable KEY=VALUE (repeatable)")
	f.BoolVar(&noSkip, "no-skip-permissions", false, "Keep the agent CLI's tool approval prompts")
	f.BoolVar(&req.Replace, "replace", false, "Stop a running agent with the same name first")
	return cmd
}

func printSpawned(w io.Writer, team string, res spawnResult) {
	fmt.Fprintf(w, "OK Spawned '%s' (%s, id: %s)\n", res.AgentName, res.Backend, res.AgentID)
	fmt.Fprintf(w, "  Workspace: %s\n", res.Workspace)
	if res.Backend == "tmux" {
		fmt.Fprintf(w, "  Watch: tmux attach -t %s\n", spawn.SessionName(team))
	} else {
		fmt.Fprintf(w, "  Log: %s\n", res.Target)
	}
}
