package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/agent"
	"github.com/benlocal/team-worker/internal/agent/builtin"
	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/spawn"
	"github.com/benlocal/team-worker/internal/store"
)

type spawnRequest struct {
	RequiredEnvKeys []string
	RunID           string
	ResumeID        string
	KeepAlive       bool
	MaxRestarts     int
	RestartDelay    time.Duration
	Team            string
	Name            string
	AgentID         string
	AgentType       string
	Task            string
	TaskSet         bool
	Backend         string
	Command         []string
	Cwd             string
	// EnvLayers come from the template, the template agent and --env, in that order.
	EnvLayers []spawn.EnvLayer
	SkipPerms bool
	Replace   bool
	DryRun    bool
	// PlanRequired makes the member wait for an approved plan before working.
	PlanRequired bool
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
	Adapter   string   `json:"adapter"`
	// Dry runs also report the full start: final arguments, prompt and
	// where each environment variable comes from (never its value).
	Args   []string    `json:"args,omitempty"`
	Prompt string      `json:"prompt,omitempty"`
	Env    []envOrigin `json:"env,omitempty"`
}

type envOrigin struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
}

// spawnAgent registers the agent in its team (creating the team with the
// agent as leader when needed), prepares its workspace and prompt, starts
// it and records the process with the daemon. Everything that can fail is
// checked before the team is changed, and a dry run stops there.
func (a *app) spawnAgent(ctx context.Context, req spawnRequest) (spawnResult, error) {
	c := a.client()
	id := config.IdentityFromEnv()
	if req.AgentType == "" {
		req.AgentType = "general-purpose"
	}
	registry, err := builtin.Load(config.AgentsDir())
	if err != nil {
		return spawnResult{}, err
	}
	adapter := registry.For(req.Command)
	if req.MaxRestarts < 0 || req.RestartDelay < 0 || req.RestartDelay > time.Minute {
		return spawnResult{}, fmt.Errorf("restart limits must not be negative")
	}
	if req.RestartDelay == 0 {
		req.RestartDelay = time.Second
	}
	if req.KeepAlive && agent.SessionClient(req.Command) == "" {
		return spawnResult{}, fmt.Errorf("keepalive is not supported by this CLI")
	}
	req.RunID = store.NewAgentID()
	nativeCommand, clientName, nativeID, err := agent.StartSessionCommand(req.Command)
	if req.ResumeID != "" {
		nativeCommand, err = agent.ResumeCommand(req.Command, req.ResumeID, req.Backend == "tmux")
		nativeID = req.ResumeID
	}
	if err != nil {
		return spawnResult{}, err
	}

	team, err := c.GetTeam(ctx, req.Team)
	teamExists := err == nil
	if err != nil && !client.IsNotFound(err) {
		return spawnResult{}, err
	}
	if teamExists && !req.DryRun {
		if err := a.requireBudget(ctx, req.Team); err != nil {
			return spawnResult{}, err
		}
	}
	member := findMember(team, req.Name, id.User)
	if member != nil && !req.TaskSet && req.Task == "" {
		if req.Task, err = c.AgentPrompt(ctx, req.Team, req.Name, id.User); err != nil {
			return spawnResult{}, err
		}
	}
	if len(req.Task) > store.MaxAgentPromptLength {
		return spawnResult{}, fmt.Errorf("Agent instructions must be at most %d bytes", store.MaxAgentPromptLength)
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
			return spawnResult{}, err
		}
	}

	dataDir := config.DataDir()
	cwd := req.Cwd
	if cwd == "" {
		cwd = config.WorkspaceDir(dataDir, req.Team, req.Name)
	}
	if cwd, err = filepath.Abs(cwd); err != nil {
		return spawnResult{}, err
	}
	prompt := spawn.BuildPrompt(spawn.PromptParams{
		AgentName: req.Name, AgentID: req.AgentID, AgentType: req.AgentType, Team: req.Team,
		Leader: leader, User: id.User, Workspace: cwd, TeamPrompt: teamPrompt, Task: req.Task,
		PlanRequired: req.PlanRequired,
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
		"TEAM_WORKER_BIN":           workerBinary(),
	}
	if req.PlanRequired {
		identity["TEAM_WORKER_PLAN_MODE_REQUIRED"] = "1"
	}
	if id.User != "" {
		identity["TEAM_WORKER_USER"] = id.User
	}
	built, err := spawn.Build(spawn.Options{
		Backend: req.Backend, Command: nativeCommand, RunID: req.RunID, KeepAlive: req.KeepAlive, Adapter: adapter, Team: req.Team, Agent: req.Name,
		Prompt: prompt, Cwd: cwd, SkipPermissions: req.SkipPerms, IsLeader: req.Name == leader,
		EnvLayers: req.EnvLayers, Identity: identity, WorkerBin: workerBinary(),
		RunDir: filepath.Join(dataDir, "run"), LogPath: config.AgentLogPath(dataDir, req.Team, req.Name),
	})
	if err != nil {
		return spawnResult{}, err
	}
	result := spawnResult{Backend: req.Backend, AgentName: req.Name, AgentID: req.AgentID, Workspace: cwd,
		Command: req.Command, Adapter: adapter.Name() + " (" + adapter.Source() + ")"}
	if req.DryRun {
		result.Status = "dry-run"
		result.Args = hidePrompt(built.Plan.Args, prompt)
		result.Prompt = prompt
		for name, origin := range built.Env.Origin {
			result.Env = append(result.Env, envOrigin{Name: name, Origin: origin})
		}
		sort.Slice(result.Env, func(i, j int) bool { return result.Env[i].Name < result.Env[j].Name })
		return result, nil
	}

	if !teamExists {
		if _, err := c.CreateTeam(ctx, store.CreateTeamInput{
			Name: req.Team, Description: "Created by team-worker spawn", LeaderName: req.Name,
			LeaderID: req.AgentID, User: id.User, LeaderAgentType: req.AgentType,
		}); err != nil {
			return spawnResult{}, err
		}
	} else if member == nil {
		if _, err := c.AddMember(ctx, req.Team, store.AddMemberInput{
			Name: req.Name, AgentID: req.AgentID, AgentType: req.AgentType, User: id.User}); err != nil {
			return spawnResult{}, err
		}
	}
	// The flag is only ever turned on here, so a member added with
	// --plan-required keeps it when spawned again without the flag.
	if req.PlanRequired {
		if err := c.SetPlanRequired(ctx, req.Team, req.Name, id.User, true); err != nil {
			return spawnResult{}, err
		}
	}
	if existing, err := c.GetAgent(ctx, req.Team, req.Name); err == nil && isRunning(existing) {
		if !req.Replace {
			return spawnResult{}, fmt.Errorf(
				"Agent '%s' is already running in team '%s'. Use --replace to stop it and spawn a new instance.",
				req.Name, req.Team)
		}
		if err := reachable(c, existing); err != nil {
			return spawnResult{}, fmt.Errorf("%w (or stop it with team-worker agent stop first)", err)
		}
		if _, err := c.RequestAgentStop(ctx, req.Team, req.Name, existing.RunID); err != nil {
			return spawnResult{}, err
		}
		if err := proc.Stop(existing); err != nil {
			return spawnResult{}, fmt.Errorf("stop running agent '%s': %w", req.Name, err)
		}
		if _, err := c.AgentExitedRun(ctx, req.Team, req.Name, existing.RunID, 0); err != nil {
			return spawnResult{}, err
		}
	} else if err == nil {
		// A pane kept open after a failed start; the new instance replaces it.
		// Another machine's pane id would name an unrelated pane here.
		if proc.OnClient(existing, c.Remote()) {
			if err := proc.Stop(existing); err != nil {
				return spawnResult{}, err
			}
		}
		if _, err := c.AgentExitedRun(ctx, req.Team, req.Name, existing.RunID, existing.ExitCode); err != nil {
			return spawnResult{}, err
		}
	} else if !client.IsNotFound(err) {
		return spawnResult{}, err
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		return spawnResult{}, err
	}
	if req.TaskSet || req.Task != "" {
		if _, err := c.SetAgentPrompt(ctx, req.Team, req.Name, id.User, req.Task); err != nil {
			return spawnResult{}, err
		}
	}

	rec := model.Agent{Team: req.Team, Name: req.Name, AgentID: req.AgentID, RunID: req.RunID, Backend: req.Backend,
		Command: req.Command, Cwd: cwd, Nudge: req.Backend == "tmux" && adapter.Nudge(), Host: proc.Host()}
	if _, err := c.RegisterAgent(ctx, rec); err != nil {
		return spawnResult{}, err
	}
	if clientName != "" {
		keys := append([]string{}, req.RequiredEnvKeys...)
		for _, layer := range req.EnvLayers {
			for key := range layer.Set {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		session := model.Session{Team: req.Team, Agent: req.Name, User: id.User, RunID: req.RunID, Client: clientName,
			SessionID: nativeID, Source: "launch", Cwd: cwd, Command: req.Command, Backend: req.Backend,
			SkipPermissions: req.SkipPerms, KeepAlive: req.KeepAlive, MaxRestarts: req.MaxRestarts,
			RestartDelayMillis: req.RestartDelay.Milliseconds(), EnvKeys: keys}
		if _, err := c.SaveSession(ctx, session); err != nil {
			if _, cleanupErr := c.AgentExitedRun(ctx, req.Team, req.Name, req.RunID, -1); cleanupErr != nil {
				return spawnResult{}, fmt.Errorf("save session: %w; release reservation: %v", err, cleanupErr)
			}
			return spawnResult{}, err
		}
	}
	res, err := built.Start()
	if err != nil {
		if _, cleanupErr := c.AgentExitedRun(ctx, req.Team, req.Name, req.RunID, -1); cleanupErr != nil {
			return spawnResult{}, fmt.Errorf("start agent: %w; release reservation: %v", err, cleanupErr)
		}
		return spawnResult{}, err
	}
	rec.Target, rec.PID = res.Target, res.PID
	if _, err := c.RegisterAgent(ctx, rec); err != nil {
		if stopErr := proc.Stop(rec); stopErr != nil {
			return spawnResult{}, fmt.Errorf("publish agent: %w; stop child: %v", err, stopErr)
		}
		return spawnResult{}, err
	}
	current, err := c.GetAgent(ctx, req.Team, req.Name)
	if err != nil {
		return spawnResult{}, err
	}
	if current.RunID != req.RunID || current.StopRequested {
		if err := proc.Stop(rec); err != nil {
			return spawnResult{}, err
		}
		return spawnResult{}, fmt.Errorf("agent launch was stopped or replaced")
	}
	if !req.KeepAlive {
		res.FinishStartup()
	}
	result.Status, result.Target, result.PID = "spawned", res.Target, res.PID
	return result, nil
}

// hidePrompt shortens the prompt and hook settings arguments so dry-run
// output stays readable.
func hidePrompt(args []string, prompt string) []string {
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

// taskInput resolves --task and --task-file: the instructions and whether
// they were given at all, so an empty --task can still clear saved ones. A
// file keeps long instructions off the command line.
func taskInput(cmd *cobra.Command, task, file string) (string, bool, error) {
	if !cmd.Flags().Changed("task-file") {
		return task, cmd.Flags().Changed("task"), nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", false, fmt.Errorf("read --task-file: %w", err)
	}
	return strings.TrimRight(string(data), "\n"), true, nil
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

// envFlagLayer turns repeated --env KEY=VALUE flags into the last env layer.
func envFlagLayer(pairs []string) ([]spawn.EnvLayer, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	env := map[string]string{}
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --env %q: use KEY=VALUE", kv)
		}
		env[k] = v
	}
	return []spawn.EnvLayer{{Name: "--env", Set: env}}, nil
}

func (a *app) spawnCmd() *cobra.Command {
	var req spawnRequest
	var envPairs []string
	var noSkip bool
	var profileName, taskFile string
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
			if req.EnvLayers, err = envFlagLayer(envPairs); err != nil {
				return a.fail(err)
			}
			// The profile also supplies the command when none was given.
			if err := applyProfile(&req, profileName); err != nil {
				return a.fail(err)
			}
			req.SkipPerms = !noSkip
			if req.Task, req.TaskSet, err = taskInput(cmd, req.Task, taskFile); err != nil {
				return a.fail(err)
			}
			res, err := a.spawnAgent(cmd.Context(), req)
			if err != nil {
				return a.fail(err)
			}
			a.emit(res, func(w io.Writer) {
				if req.DryRun {
					printDryRun(w, res)
				} else {
					printSpawned(w, req.Team, res)
				}
			})
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&req.Team, "team", "t", "", "Team name (default: TEAM_WORKER_TEAM_NAME)")
	f.StringVarP(&req.Name, "agent-name", "n", "", "Agent name (default: random)")
	f.StringVar(&req.AgentType, "agent-type", "general-purpose", "Agent type")
	f.StringVar(&req.Task, "task", "", "Save agent instructions (default: reuse saved instructions; empty clears them)")
	f.StringVar(&taskFile, "task-file", "", "Read the agent instructions from this file instead of --task")
	cmd.MarkFlagsMutuallyExclusive("task", "task-file")
	f.StringVar(&req.Cwd, "cwd", "", "Working directory (default: <data-dir>/workspaces/<team>/<agent>)")
	f.StringArrayVar(&envPairs, "env", nil, "Extra environment variable KEY=VALUE (repeatable)")
	f.StringVar(&profileName, "profile", "", "Runtime profile (default: default_profile, else the only profile; none with an explicit command)")
	f.BoolVar(&noSkip, "no-skip-permissions", false, "Keep the agent CLI's tool approval prompts")
	f.BoolVar(&req.Replace, "replace", false, "Stop a running agent with the same name first")
	f.BoolVar(&req.DryRun, "dry-run", false, "Show the command, prompt and environment sources without starting")
	f.BoolVar(&req.KeepAlive, "keepalive", false, "Resume the exact session after unapproved exits")
	f.BoolVar(&req.PlanRequired, "plan-required", false, "Require an approved plan before the agent starts or completes tasks")
	f.IntVar(&req.MaxRestarts, "max-restarts", 3, "Maximum consecutive keepalive restarts")
	f.DurationVar(&req.RestartDelay, "restart-delay", time.Second, "Initial keepalive retry delay (doubles up to one minute)")
	return cmd
}

func printDryRun(w io.Writer, res spawnResult) {
	fmt.Fprintf(w, "Adapter:   %s\n", res.Adapter)
	fmt.Fprintf(w, "Backend:   %s\n", res.Backend)
	fmt.Fprintf(w, "Workspace: %s\n", res.Workspace)
	fmt.Fprintf(w, "Command:   %s\n", strings.Join(res.Args, " "))
	fmt.Fprintf(w, "Prompt:    %d chars (--json shows the text)\n\nEnvironment (values hidden):\n", len(res.Prompt))
	tw := newTable(w, "  NAME", "FROM")
	for _, e := range res.Env {
		row(tw, "  "+e.Name, e.Origin)
	}
	tw.Flush()
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
