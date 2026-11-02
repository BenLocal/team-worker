package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

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
func envFlagLayer(pairs []string) ([]model.EnvLayer, error) {
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
	return []model.EnvLayer{{Name: "--env", Set: env}}, nil
}

func (a *app) spawnCmd() *cobra.Command {
	var req model.SpawnRequest
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
			// The daemon applies the profile, which also supplies the command
			// when none was given.
			req.Profile, req.User = profileName, config.IdentityFromEnv().User
			req.SkipPerms = !noSkip
			if req.Task, req.TaskSet, err = taskInput(cmd, req.Task, taskFile); err != nil {
				return a.fail(err)
			}
			c := a.client()
			req.Workspace, req.CallerDir = workspaceFlags(cmd, c.Remote())
			res, err := c.SpawnAgent(cmd.Context(), req)
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
	addWorkspaceFlags(cmd, &req.Repos)
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

// addWorkspaceFlags adds ClawTeam's worktree flags: --workspace and
// --no-workspace override the daemon's workspace mode, --repo names the
// repositories to give the agent worktrees of.
func addWorkspaceFlags(cmd *cobra.Command, repos *[]string) {
	f := cmd.Flags()
	f.BoolP("workspace", "w", false, "Give the agent a git worktree (default: the daemon's workspace mode, auto)")
	f.Bool("no-workspace", false, "Do not give the agent a git worktree")
	cmd.MarkFlagsMutuallyExclusive("workspace", "no-workspace")
	f.StringArrayVar(repos, "repo", nil,
		"Git repository to give the agent a worktree of, a path on the daemon's machine (repeatable; default: the current directory's repository, locally)")
}

// workspaceFlags reads --workspace/--no-workspace, and the directory to look
// for a repository in: this CLI's own working directory, which only means
// something to a daemon on the same machine.
func workspaceFlags(cmd *cobra.Command, remote bool) (*bool, string) {
	var choice *bool
	if cmd.Flags().Changed("workspace") {
		on := true
		choice = &on
	} else if cmd.Flags().Changed("no-workspace") {
		off := false
		choice = &off
	}
	dir := ""
	if !remote {
		dir, _ = os.Getwd()
	}
	return choice, dir
}

func printDryRun(w io.Writer, res model.SpawnResult) {
	fmt.Fprintf(w, "Adapter:   %s\n", res.Adapter)
	fmt.Fprintf(w, "Backend:   %s\n", res.Backend)
	fmt.Fprintf(w, "Workspace: %s\n", res.Workspace)
	printWorktrees(w, res.Workspaces)
	fmt.Fprintf(w, "Command:   %s\n", strings.Join(res.Args, " "))
	fmt.Fprintf(w, "Prompt:    %d chars (--json shows the text)\n\nEnvironment (values hidden):\n", len(res.Prompt))
	tw := newTable(w, "  NAME", "FROM")
	for _, e := range res.Env {
		row(tw, "  "+e.Name, e.Origin)
	}
	tw.Flush()
}

func printSpawned(w io.Writer, team string, res model.SpawnResult) {
	fmt.Fprintf(w, "OK Spawned '%s' (%s, id: %s)\n", res.AgentName, res.Backend, res.AgentID)
	fmt.Fprintf(w, "  Workspace: %s\n", res.Workspace)
	printWorktrees(w, res.Workspaces)
	if res.Backend == "tmux" {
		fmt.Fprintf(w, "  Watch: tmux attach -t %s\n", model.TmuxSession(team))
	} else {
		fmt.Fprintf(w, "  Log: %s\n", res.Target)
	}
}

func printWorktrees(w io.Writer, list []model.Workspace) {
	for _, ws := range list {
		fmt.Fprintf(w, "  Worktree:  %s (%s, branch %s from %s)\n", ws.WorktreePath, ws.RepoRoot, ws.BranchName, ws.BaseBranch)
	}
}
