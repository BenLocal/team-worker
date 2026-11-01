package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
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

func (a *app) launchCmd() *cobra.Command {
	var goal, teamName, backend, cwd, promptFile, profileName string
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
			in := model.LaunchRequest{Template: args[0], Goal: goal, Team: teamName, Backend: backend, Cwd: cwd,
				Profile: profileName, NoSkip: noSkip, User: config.IdentityFromEnv().User}
			// A template file is the caller's; a template name is the daemon's.
			if strings.HasSuffix(args[0], ".toml") {
				data, err := os.ReadFile(args[0])
				if err != nil {
					return a.fail(err)
				}
				in.TemplateText = string(data)
			}
			if dash := cmd.ArgsLenAtDash(); dash >= 0 {
				in.Command = args[dash:]
			}
			var err error
			if in.EnvLayers, err = envFlagLayer(envPairs); err != nil {
				return a.fail(err)
			}
			if promptFile != "" {
				data, err := os.ReadFile(promptFile)
				if err != nil {
					return a.fail(err)
				}
				in.TeamPrompt = strings.TrimRight(string(data), "\n")
			}
			out, err := a.client().Launch(cmd.Context(), in)
			if err != nil {
				return a.fail(err)
			}
			teamName, launched, backend, failed := out.Team, out.Agents, out.Backend, out.Status == "partial"
			a.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "Team '%s' launched from template '%s'\n\n", teamName, out.Template)
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
					fmt.Fprintf(w, "Watch:  tmux attach -t %s\n", model.TmuxSession(teamName))
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
	f.StringVar(&profileName, "profile", "", "Runtime profile for every agent; template environment still overrides it")
	f.BoolVar(&noSkip, "no-skip-permissions", false, "Keep the agent CLIs' tool approval prompts")
	return cmd
}
