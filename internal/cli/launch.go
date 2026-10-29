package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/spawn"
	"github.com/benlocal/team-worker/internal/store"
	"github.com/benlocal/team-worker/internal/templates"
)

func (a *app) templateCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "template", Short: "Team templates for launch"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the templates in ~/.team-worker/templates",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, err := templates.List(config.TemplateDir())
			if err != nil {
				return a.fail(err)
			}
			a.emit(list, func(w io.Writer) {
				if len(list) == 0 {
					fmt.Fprintf(w, "No templates in %s\nWrite one as <name>.toml; the format is described in docs/templates.md.\n",
						config.TemplateDir())
					return
				}
				tw := newTable(w, "NAME", "AGENTS", "SOURCE", "DESCRIPTION")
				for _, t := range list {
					row(tw, t.Name, fmt.Sprint(len(t.Agents)+1), t.Source, t.Description)
				}
				tw.Flush()
			})
			return nil
		},
	}, &cobra.Command{
		Use:   "show NAME",
		Short: "Show a template's agents and tasks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := templates.Load(args[0], config.TemplateDir())
			if err != nil {
				return a.fail(err)
			}
			a.emit(t, func(w io.Writer) {
				fmt.Fprintf(w, "%s — %s\n  command: %s  backend: %s  source: %s\n\n", t.Name, t.Description,
					strings.Join(t.Command, " "), t.Backend, t.Source)
				tw := newTable(w, "AGENT", "TYPE", "ROLE")
				row(tw, t.Leader.Name, t.Leader.Type, "leader")
				for _, ag := range t.Agents {
					row(tw, ag.Name, ag.Type, "worker")
				}
				tw.Flush()
				fmt.Fprintln(w, "\nTasks:")
				for _, task := range t.Tasks {
					line := "  - " + task.Subject + "  @" + task.Owner
					if len(task.BlockedBy) > 0 {
						line += "  after " + strings.Join(task.BlockedBy, ", ")
					}
					fmt.Fprintln(w, line)
				}
			})
			return nil
		},
	})
	return cmd
}

type launchedAgent struct {
	spawnResult
	Error string `json:"error,omitempty"`
}

func (a *app) launchCmd() *cobra.Command {
	var goal, teamName, backend, cwd, promptFile string
	var envPairs []string
	var noSkip bool
	cmd := &cobra.Command{
		Use:   "launch TEMPLATE|FILE.toml [-- COMMAND ARGS...]",
		Short: "Create a team from a template and spawn all its agents",
		Long: "Create a team from a template: register the leader and members, create the template's\n" +
			"tasks with their dependencies, then spawn the leader and every worker. A command after --\n" +
			"replaces the template's agent command for all agents.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dash := cmd.ArgsLenAtDash(); dash > 1 || (dash == -1 && len(args) > 1) {
				return a.fail(fmt.Errorf("unexpected arguments; put the agent command after --"))
			}
			tmpl, err := templates.Load(args[0], config.TemplateDir())
			if err != nil {
				return a.fail(err)
			}
			var override []string
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				override = args[dash:]
			}
			env, err := parseEnv(envPairs)
			if err != nil {
				return a.fail(err)
			}
			if teamName == "" {
				teamName = tmpl.Name + "-" + store.NewAgentID()[:6]
			}
			if backend == "" {
				backend = tmpl.Backend
			}

			ctx := cmd.Context()
			c := a.client()
			user := config.IdentityFromEnv().User
			leader := tmpl.Leader
			if _, err := c.CreateTeam(ctx, store.CreateTeamInput{Name: teamName, Description: tmpl.Description,
				LeaderName: leader.Name, User: user, LeaderAgentType: agentType(leader)}); err != nil {
				return a.fail(err)
			}
			if promptFile != "" {
				data, err := os.ReadFile(promptFile)
				if err != nil {
					return a.fail(err)
				}
				if _, err := c.SetTeamPrompt(ctx, teamName, strings.TrimRight(string(data), "\n")); err != nil {
					return a.fail(err)
				}
			}
			for _, ag := range tmpl.Agents {
				if _, err := c.AddMember(ctx, teamName, store.AddMemberInput{
					Name: ag.Name, AgentType: agentType(ag), User: user}); err != nil {
					return a.fail(err)
				}
			}
			ids := map[string]string{}
			for _, task := range tmpl.Tasks {
				var deps []string
				for _, key := range task.BlockedBy {
					deps = append(deps, ids[key])
				}
				in := store.CreateTaskInput{Subject: task.Subject,
					Description: templates.Render(task.Description, goal, teamName, task.Owner, leader.Name),
					Owner:       task.Owner, Priority: model.Priority(task.Priority), BlockedBy: deps}
				created, err := c.CreateTask(ctx, teamName, in)
				if err != nil {
					return a.fail(err)
				}
				if task.Key != "" {
					ids[task.Key] = created.ID
				}
			}

			var launched []launchedAgent
			failed := false
			for _, ag := range append([]templates.Agent{leader}, tmpl.Agents...) {
				command := ag.Command
				if len(override) > 0 {
					command = override
				} else if len(command) == 0 {
					command = tmpl.Command
				}
				res, err := a.spawnAgent(ctx, spawnRequest{
					Team: teamName, Name: ag.Name, AgentType: agentType(ag), Backend: backend, Command: command,
					Task: templates.Render(ag.Task, goal, teamName, ag.Name, leader.Name),
					Cwd:  cwd, Env: env, SkipPerms: !noSkip,
				})
				entry := launchedAgent{spawnResult: res}
				if err != nil {
					failed = true
					entry.AgentName, entry.Error = ag.Name, err.Error()
				}
				launched = append(launched, entry)
			}

			status := "launched"
			if failed {
				status = "partial"
			}
			out := map[string]any{"status": status, "team": teamName, "template": tmpl.Name,
				"backend": backend, "agents": launched}
			a.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "Team '%s' launched from template '%s'\n\n", teamName, tmpl.Name)
				tw := newTable(w, "AGENT", "STATUS", "WORKSPACE")
				for _, l := range launched {
					state := "spawned"
					if l.Error != "" {
						state = "failed: " + l.Error
					}
					row(tw, l.AgentName, state, l.Workspace)
				}
				tw.Flush()
				fmt.Fprintln(w)
				if backend == "tmux" {
					fmt.Fprintf(w, "Watch:  tmux attach -t %s\n", spawn.SessionName(teamName))
				}
				fmt.Fprintf(w, "Board:  team-worker board show %s   (web: team-worker board serve)\n", teamName)
			})
			if failed {
				return exitError{}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&goal, "goal", "g", "", "Goal filled into the template's {goal}")
	f.StringVarP(&teamName, "team", "t", "", "Team name (default: <template>-<random>)")
	f.StringVarP(&backend, "backend", "b", "", "Override the template backend: tmux or subprocess")
	f.StringVar(&promptFile, "team-prompt-file", "", "Team instructions every agent receives in its first prompt")
	f.StringVar(&cwd, "cwd", "", "Working directory for every agent (default: one workspace per agent)")
	f.StringArrayVar(&envPairs, "env", nil, "Extra environment variable KEY=VALUE for every agent")
	f.BoolVar(&noSkip, "no-skip-permissions", false, "Keep the agent CLIs' tool approval prompts")
	return cmd
}

func agentType(a templates.Agent) string {
	if a.Type == "" {
		return "general-purpose"
	}
	return a.Type
}
