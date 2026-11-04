package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/benlocal/team-worker/internal/agent"
	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/spf13/cobra"
)

func (a *app) sessionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "Inspect or save native CLI sessions"}
	var name, nativeID, cliName string
	show := &cobra.Command{Use: "show TEAM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if name == "" {
			name = config.IdentityFromEnv().AgentName
		}
		session, err := a.client().GetSession(cmd.Context(), args[0], name, config.IdentityFromEnv().User)
		if err != nil {
			return a.fail(err)
		}
		a.emit(session, func(w io.Writer) {
			fmt.Fprintf(w, "Agent: %s\nClient: %s\nSession: %s\nWorkspace: %s\n", session.Agent, session.Client, session.SessionID, session.Cwd)
		})
		return nil
	}}
	show.Flags().StringVarP(&name, "agent", "a", "", "Agent name (default: current identity)")
	save := &cobra.Command{Use: "save TEAM", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if nativeID == "" {
			return a.fail(fmt.Errorf("native session id must not be empty"))
		}
		id := config.IdentityFromEnv()
		if name == "" {
			name = id.AgentName
		}
		ag, err := a.client().GetAgent(cmd.Context(), args[0], name)
		if err != nil {
			return a.fail(err)
		}
		session, err := a.client().GetSession(cmd.Context(), args[0], name, id.User)
		if err != nil && !client.IsNotFound(err) {
			return a.fail(err)
		}
		if session.Client == "" {
			session = model.Session{Team: args[0], Agent: name, User: id.User, Client: agent.SessionClient(ag.Command), Cwd: ag.Cwd, Command: ag.Command, Backend: ag.Backend}
		}
		if cliName != "" {
			session.Client = cliName
		}
		session.SessionID, session.Source, session.RunID = nativeID, "manual", ag.RunID
		if run := os.Getenv("TEAM_WORKER_RUN_ID"); run != "" {
			session.RunID = run
		}
		out, err := a.client().SaveSession(cmd.Context(), session)
		if err != nil {
			return a.fail(err)
		}
		a.emit(out, func(w io.Writer) { fmt.Fprintf(w, "OK Saved session for '%s'\n", name) })
		return nil
	}}
	save.Flags().StringVarP(&name, "agent", "a", "", "Agent name (default: current identity)")
	save.Flags().StringVar(&nativeID, "session-id", "", "Exact native session id")
	save.Flags().StringVar(&cliName, "client", "", "Native CLI name")
	save.MarkFlagRequired("session-id")
	var clearName string
	clearCmd := &cobra.Command{Use: "clear TEAM", Short: "Clear saved sessions so the next start is a fresh conversation",
		Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			n, err := a.client().ClearSessions(cmd.Context(), args[0], clearName, config.IdentityFromEnv().User)
			if err != nil {
				return a.fail(err)
			}
			switch {
			case clearName == "":
				a.emit(map[string]any{"status": "cleared", "count": n}, func(w io.Writer) { fmt.Fprintf(w, "OK Cleared %d session(s)\n", n) })
			case n > 0:
				a.emit(map[string]any{"status": "cleared", "agent": clearName}, func(w io.Writer) {
					fmt.Fprintf(w, "OK Session cleared for '%s'\n", clearName)
				})
			default:
				a.emit(map[string]any{"status": "not_found", "agent": clearName}, func(w io.Writer) {
					fmt.Fprintf(w, "No session for '%s'\n", clearName)
				})
			}
			return nil
		}}
	clearCmd.Flags().StringVarP(&clearName, "agent", "a", "", "Agent name (default: all)")
	cmd.AddCommand(show, save, clearCmd)
	return cmd
}

func (a *app) agentRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore",
		Short: "Resume, in every team, the agents a restart left behind",
		Long: "Resumes the saved session of every agent still recorded as running, not asked to\n" +
			"stop, whose process is gone. team-workerd does this itself when it starts; running it\n" +
			"again waits for that and resumes nothing twice.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			restored, err := a.client().RestoreAgents(cmd.Context())
			if err != nil {
				return a.fail(err)
			}
			a.emit(restored, func(w io.Writer) {
				if len(restored) == 0 {
					fmt.Fprintln(w, "Nothing left to restore; team-workerd resumes agents itself as it starts (see agent list and its log).")
					return
				}
				tw := newTable(w, "TEAM", "AGENT", "STATUS", "ERROR")
				for _, r := range restored {
					row(tw, r.Team, r.Agent, r.Status, r.Error)
				}
				tw.Flush()
			})
			return nil
		},
	}
}

func (a *app) agentResumeCmd() *cobra.Command {
	var replace, dryRun, keepalive bool
	var maxRestarts int
	var delay time.Duration
	var task, taskFile string
	var envPairs []string
	var profileName string
	cmd := &cobra.Command{Use: "resume TEAM NAME", Short: "Resume an agent's exact saved CLI session", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		in := model.ResumeRequest{Team: args[0], Name: args[1], User: config.IdentityFromEnv().User,
			Profile: profileName, Replace: replace, DryRun: dryRun}
		var err error
		if in.EnvLayers, err = envFlagLayer(envPairs); err != nil {
			return a.fail(err)
		}
		if cmd.Flags().Changed("keepalive") {
			in.KeepAlive = &keepalive
		}
		if cmd.Flags().Changed("max-restarts") {
			in.MaxRestarts = &maxRestarts
		}
		if cmd.Flags().Changed("restart-delay") {
			in.RestartDelay = &delay
		}
		if text, set, err := taskInput(cmd, task, taskFile); err != nil {
			return a.fail(err)
		} else if set {
			in.Task = &text
		}
		res, err := a.client().ResumeAgent(cmd.Context(), in)
		if err != nil {
			return a.fail(err)
		}
		a.emit(res, func(w io.Writer) {
			if dryRun {
				printDryRun(w, res)
			} else {
				printSpawned(w, args[0], res)
			}
		})
		return nil
	}}
	cmd.Flags().BoolVar(&keepalive, "keepalive", false, "Override the saved keepalive policy")
	cmd.Flags().IntVar(&maxRestarts, "max-restarts", 3, "Override the saved restart limit")
	cmd.Flags().DurationVar(&delay, "restart-delay", time.Second, "Override the saved initial retry delay")
	cmd.Flags().StringVar(&task, "task", "", "Replace the saved agent instructions")
	cmd.Flags().StringVar(&taskFile, "task-file", "", "Replace the saved agent instructions with this file's content")
	cmd.MarkFlagsMutuallyExclusive("task", "task-file")
	cmd.Flags().BoolVar(&replace, "replace", false, "Stop the current run before resuming")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the resume command without starting")
	cmd.Flags().StringArrayVar(&envPairs, "env", nil, "Environment variable KEY=VALUE (repeatable)")
	cmd.Flags().StringVar(&profileName, "profile", "", "Runtime profile whose environment the resumed agent gets again")
	return cmd
}
