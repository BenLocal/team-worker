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
		if session.SessionID == "" {
			if err := a.captureNativeSession(cmd.Context(), args[0], name, session.RunID); err != nil {
				return a.fail(err)
			}
			session, err = a.client().GetSession(cmd.Context(), args[0], name, config.IdentityFromEnv().User)
			if err != nil {
				return a.fail(err)
			}
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
	cmd.AddCommand(show, save)
	return cmd
}

func (a *app) agentResumeCmd() *cobra.Command {
	var replace, dryRun, keepalive bool
	var maxRestarts int
	var delay time.Duration
	var task, taskFile string
	var envPairs []string
	var profileName string
	cmd := &cobra.Command{Use: "resume TEAM NAME", Short: "Resume an agent's exact saved CLI session", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		session, err := a.client().GetSession(cmd.Context(), args[0], args[1], config.IdentityFromEnv().User)
		if err != nil {
			return a.fail(err)
		}
		if session.SessionID == "" {
			if err := a.captureNativeSession(cmd.Context(), args[0], args[1], session.RunID); err != nil {
				return a.fail(err)
			}
			session, err = a.client().GetSession(cmd.Context(), args[0], args[1], config.IdentityFromEnv().User)
			if err != nil {
				return a.fail(err)
			}
			if session.SessionID == "" {
				return a.fail(fmt.Errorf("no native session id was captured; use session save first"))
			}
		}
		layers, err := envFlagLayer(envPairs)
		if err != nil {
			return a.fail(err)
		}
		// A profile's variables come back from the profile, not the caller.
		profileLayers, err := profileEnvLayer(profileName, session.Command)
		if err != nil {
			return a.fail(err)
		}
		layers = append(profileLayers, layers...)
		for _, key := range session.EnvKeys {
			_, supplied := os.LookupEnv(key)
			for _, layer := range layers {
				if _, ok := layer.Set[key]; ok {
					supplied = true
				}
			}
			if !supplied {
				return a.fail(fmt.Errorf("resume requires environment variable %s; supply it with --env or the caller environment", key))
			}
		}
		if cmd.Flags().Changed("keepalive") {
			session.KeepAlive = keepalive
		}
		if cmd.Flags().Changed("max-restarts") {
			session.MaxRestarts = maxRestarts
		}
		if cmd.Flags().Changed("restart-delay") {
			session.RestartDelayMillis = delay.Milliseconds()
		}
		var taskSet bool
		if task, taskSet, err = taskInput(cmd, task, taskFile); err != nil {
			return a.fail(err)
		}
		res, err := a.spawnAgent(cmd.Context(), spawnRequest{RequiredEnvKeys: session.EnvKeys, Team: args[0], Name: args[1], Task: task, TaskSet: taskSet, Command: session.Command, Backend: session.Backend, Cwd: session.Cwd, SkipPerms: session.SkipPermissions, KeepAlive: session.KeepAlive, MaxRestarts: session.MaxRestarts, RestartDelay: time.Duration(session.RestartDelayMillis) * time.Millisecond, ResumeID: session.SessionID, Replace: replace, DryRun: dryRun, EnvLayers: layers})
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
