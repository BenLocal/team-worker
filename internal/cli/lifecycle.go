package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/store"
)

func (a *app) lifecycleCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "lifecycle", Short: "Agent exit, idle and shutdown signals"}
	cmd.AddCommand(a.onExitCmd(), a.idleCmd(), a.requestShutdownCmd(), a.superviseCmd(),
		a.shutdownReplyCmd("approve-shutdown", "shutdown_approved", "Approve a shutdown request, then exit"),
		a.shutdownReplyCmd("reject-shutdown", "shutdown_rejected", "Reject a shutdown request and keep working"))
	return cmd
}

func (a *app) onExitCmd() *cobra.Command {
	var team, agent, runID string
	var code int
	cmd := &cobra.Command{
		Use:   "on-exit",
		Short: "Record an agent's exit (run automatically by spawn)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.captureNativeSession(cmd.Context(), team, agent, runID); err != nil {
				fmt.Fprintf(a.errOut, "capture session: %v\n", err)
			}
			if _, _, err := a.syncSessionUsage(cmd.Context(), team, agent); err != nil {
				fmt.Fprintf(a.errOut, "record session usage: %v\n", err)
			}
			res, err := a.client().AgentExitedRun(cmd.Context(), team, agent, runID, code)
			if err != nil {
				return a.fail(err)
			}
			a.emit(res, func(w io.Writer) {
				fmt.Fprintf(w, "Agent '%s' exited; %d task(s) returned to pending\n", agent, len(res.Released))
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&team, "team", "t", "", "Team name")
	cmd.Flags().StringVarP(&agent, "agent", "n", "", "Agent name")
	cmd.Flags().StringVar(&runID, "run-id", os.Getenv("TEAM_WORKER_RUN_ID"), "Agent run id")
	cmd.Flags().IntVar(&code, "code", 0, "Agent exit code")
	cmd.MarkFlagRequired("team")
	cmd.MarkFlagRequired("agent")
	return cmd
}

// sendToLeader sends a message from the calling agent to its team leader.
func (a *app) sendToLeader(cmd *cobra.Command, team string, in store.SendInput) (model.Message, error) {
	leader, err := a.client().Leader(cmd.Context(), team)
	if err != nil {
		return model.Message{}, err
	}
	in.From = config.IdentityFromEnv().AgentName
	in.To = leader.Name
	return a.client().Send(cmd.Context(), team, in)
}

func (a *app) idleCmd() *cobra.Command {
	var lastTask, taskStatus string
	cmd := &cobra.Command{
		Use:   "idle TEAM",
		Short: "Tell the leader this agent has no work",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content := config.IdentityFromEnv().AgentName + " is idle"
			if lastTask != "" {
				content += fmt.Sprintf(" (last task %s: %s)", lastTask, strings.TrimSpace(taskStatus))
			}
			msg, err := a.sendToLeader(cmd, args[0], store.SendInput{Type: "idle", Content: content})
			if err != nil {
				return a.fail(err)
			}
			a.emit(msg, func(w io.Writer) { fmt.Fprintf(w, "OK Idle notice sent to '%s'\n", msg.To) })
			return nil
		},
	}
	cmd.Flags().StringVar(&lastTask, "last-task", "", "ID of the last task worked on")
	cmd.Flags().StringVar(&taskStatus, "task-status", "", "Status of the last task")
	return cmd
}

func (a *app) requestShutdownCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "request-shutdown TEAM AGENT",
		Short: "Ask an agent to finish up and exit",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			content := "Please finish or hand back your current task, then approve this shutdown and exit."
			if reason != "" {
				content = reason + "\n" + content
			}
			msg, err := a.client().Send(cmd.Context(), args[0], store.SendInput{
				From: config.IdentityFromEnv().AgentName, To: args[1], Type: "shutdown_request", Content: content})
			if err != nil {
				return a.fail(err)
			}
			a.emit(msg, func(w io.Writer) {
				fmt.Fprintf(w, "OK Shutdown requested from '%s' (request: %s)\n", args[1], msg.RequestID)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Why the agent should stop")
	return cmd
}

func (a *app) shutdownReplyCmd(use string, msgType model.MessageType, short string) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   use + " TEAM REQUEST_ID",
		Short: short,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if msgType == "shutdown_approved" {
				name := config.IdentityFromEnv().AgentName
				ag, err := a.client().GetAgent(cmd.Context(), args[0], name)
				if err == nil {
					_, err = a.client().RequestAgentStop(cmd.Context(), args[0], name, ag.RunID)
				}
				if err != nil && !client.IsNotFound(err) {
					return a.fail(err)
				}
			}
			content := fmt.Sprintf("%s for request %s", strings.ReplaceAll(string(msgType), "_", " "), args[1])
			if reason != "" {
				content += ": " + reason
			}
			msg, err := a.sendToLeader(cmd, args[0], store.SendInput{Type: msgType, Content: content})
			if err != nil {
				return a.fail(err)
			}
			a.emit(msg, func(w io.Writer) { fmt.Fprintf(w, "OK Sent %s to '%s'\n", msgType, msg.To) })
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Optional explanation")
	return cmd
}

func (a *app) agentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Spawned agent processes"}
	cmd.AddCommand(a.agentListCmd(), a.agentStopCmd(), a.agentScreenCmd(), a.agentPromptCmd(), a.agentResumeCmd())
	return cmd
}

func aliveLabel(ag model.Agent) string {
	switch {
	case ag.Status == model.AgentExited:
		return "exited"
	case ag.Alive == nil:
		return "unknown"
	case *ag.Alive:
		return "alive"
	}
	return "dead"
}

// activityLabel is what a running agent's hooks last reported, or "-".
func activityLabel(ag model.Agent) string {
	if ag.Status == model.AgentExited || ag.Activity == "" {
		return "-"
	}
	return string(ag.Activity)
}

func (a *app) agentListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list TEAM",
		Short: "List spawned agents and whether their process is alive",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agents, err := a.client().ListAgents(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			a.emit(agents, func(w io.Writer) {
				if len(agents) == 0 {
					fmt.Fprintln(w, "No agents spawned")
					return
				}
				tw := newTable(w, "NAME", "STATE", "ACTIVITY", "BACKEND", "TARGET", "PID", "STARTED", "WORKSPACE")
				for _, ag := range agents {
					row(tw, ag.Name, aliveLabel(ag), activityLabel(ag), ag.Backend, ag.Target, fmt.Sprint(ag.PID),
						formatTimestamp(ag.StartedAt), ag.Cwd)
				}
				tw.Flush()
			})
			return nil
		},
	}
}

func (a *app) agentStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop TEAM NAME",
		Short: "Stop an agent's process and return its tasks to pending",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := a.client()
			ag, err := c.GetAgent(cmd.Context(), args[0], args[1])
			if err != nil {
				return a.fail(err)
			}
			if !proc.OnClient(ag, c.Remote()) {
				// Only the agent's machine can end its process: record the
				// request, which its --keepalive supervisor acts on.
				if _, err := c.RequestAgentStop(cmd.Context(), ag.Team, ag.Name, ag.RunID); err != nil {
					return a.fail(err)
				}
				a.emit(map[string]string{"status": "stop_requested", "agent": ag.Name, "host": ag.Host}, func(w io.Writer) {
					fmt.Fprintf(w, "OK Stop requested for '%s', which runs on %s. A --keepalive agent stops within a few seconds; "+
						"otherwise run team-worker agent stop on that machine.\n", ag.Name, proc.Where(ag))
				})
				return nil
			}
			if err := a.captureNativeSession(cmd.Context(), ag.Team, ag.Name, ag.RunID); err != nil {
				fmt.Fprintf(a.errOut, "capture session: %v\n", err)
			}
			if _, err := c.RequestAgentStop(cmd.Context(), ag.Team, ag.Name, ag.RunID); err != nil {
				return a.fail(err)
			}
			if err := proc.Stop(ag); err != nil {
				return a.fail(err)
			}
			res, err := c.AgentExitedRun(cmd.Context(), args[0], args[1], ag.RunID, 0)
			if err != nil {
				return a.fail(err)
			}
			a.emit(res, func(w io.Writer) {
				fmt.Fprintf(w, "OK Stopped '%s'; %d task(s) returned to pending\n", args[1], len(res.Released))
			})
			return nil
		},
	}
}

func (a *app) agentScreenCmd() *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "screen TEAM NAME",
		Short: "Print what an agent's terminal (or subprocess log) currently shows",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ag, err := a.client().GetAgent(cmd.Context(), args[0], args[1])
			if err != nil {
				return a.fail(err)
			}
			if err := reachable(a.client(), ag); err != nil {
				return a.fail(err)
			}
			text, err := proc.Screen(ag, lines)
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"agent": ag.Name, "screen": text}, func(w io.Writer) { fmt.Fprint(w, text) })
			return nil
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "l", 0, "Extra scrollback lines to include")
	return cmd
}

// reachable errors when ag's process runs on a machine other than this CLI's,
// where its pane id or pid would name an unrelated process.
func reachable(c *client.Client, ag model.Agent) error {
	if proc.OnClient(ag, c.Remote()) {
		return nil
	}
	return fmt.Errorf("Agent '%s' runs on %s; run this command there", ag.Name, proc.Where(ag))
}
