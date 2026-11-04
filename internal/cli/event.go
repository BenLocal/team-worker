package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

// eventCmd holds user event hooks, ClawTeam's `hook` commands. They live
// under `event` because `hook` runs the agent CLIs' own hooks.
func (a *app) eventCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "event", Short: "Team events and the user hooks that run on them"}
	types := &cobra.Command{
		Use:   "types",
		Short: "List the event types hooks can run on",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a.emit(model.EventTypes, func(w io.Writer) {
				tw := newTable(w, "EVENT", "FIELDS", "WHEN")
				for _, t := range model.EventTypes {
					row(tw, t.Name, strings.Join(t.Fields, ","), t.Description)
				}
				tw.Flush()
				fmt.Fprintln(w, "Every event also has team_name and timestamp.")
			})
			return nil
		},
	}
	hook := &cobra.Command{
		Use:   "hook",
		Short: "Shell commands the daemon runs when an event happens",
		Long: "Event hooks are shell commands that team-workerd runs, on its own machine, after an event\n" +
			"is committed. The event comes as JSON on stdin and as TEAM_WORKER_EVENT_* variables\n" +
			"(TYPE, TEAM_NAME, TIMESTAMP and one per field; lists are comma-separated). Hooks run one at\n" +
			"a time in priority order with a 30-second limit; a daemon restart reruns the hooks of an\n" +
			"event it had not finished. See `team-worker event types`.",
	}
	hook.AddCommand(a.eventHookListCmd(), a.eventHookAddCmd(), a.eventHookRemoveCmd(), a.eventHookTestCmd())
	cmd.AddCommand(types, hook)
	return cmd
}

func (a *app) eventHookListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the event hooks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			hooks, err := a.client().ListEventHooks(cmd.Context())
			if err != nil {
				return a.fail(err)
			}
			a.emit(hooks, func(w io.Writer) {
				if len(hooks) == 0 {
					fmt.Fprintln(w, "No hooks configured")
					return
				}
				tw := newTable(w, "ID", "EVENT", "TEAM", "PRIORITY", "COMMAND")
				for _, h := range hooks {
					team := h.Team
					if team == "" {
						team = "(all)"
					}
					row(tw, h.ID, h.Event, team, fmt.Sprint(h.Priority), h.Command)
				}
				tw.Flush()
			})
			return nil
		},
	}
}

func (a *app) eventHookAddCmd() *cobra.Command {
	var in store.EventHookInput
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Run a shell command when an event happens",
		Example: "  team-worker event hook add -e TaskCompleted -c 'notify-send \"$TEAM_WORKER_EVENT_TASK_ID done\"'\n" +
			"  team-worker event hook add -e WorkerCrash -t demo -c 'cat >> ~/crashes.jsonl'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			h, err := a.client().AddEventHook(cmd.Context(), in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(h, func(w io.Writer) { fmt.Fprintf(w, "OK Hook %s added: %s -> %s\n", h.ID, h.Event, h.Command) })
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&in.Event, "event", "e", "", "Event type, e.g. WorkerExit (see `event types`)")
	f.StringVarP(&in.Action, "action", "a", "shell", "Hook action; only shell is supported")
	f.StringVarP(&in.Command, "command", "c", "", "Shell command to run")
	f.IntVarP(&in.Priority, "priority", "p", 0, "Lower runs first")
	f.StringVarP(&in.Team, "team", "t", "", "Only run for this team (default: every team)")
	cmd.MarkFlagRequired("event")
	cmd.MarkFlagRequired("command")
	return cmd
}

func (a *app) eventHookRemoveCmd() *cobra.Command {
	var event, command string
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove an event's hooks, or only those with a given command",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := a.client().RemoveEventHooks(cmd.Context(), event, command)
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]any{"event": event, "removed": n}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Removed %d hook(s) for %s\n", n, event)
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&event, "event", "e", "", "Event type")
	cmd.Flags().StringVarP(&command, "command", "c", "", "Only remove hooks with this exact command")
	cmd.MarkFlagRequired("event")
	return cmd
}

func (a *app) eventHookTestCmd() *cobra.Command {
	var event, team, agent string
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Run an event's hooks now with a made-up event",
		Long: "Runs the hooks of an event type right away on the daemon's machine, with a made-up event\n" +
			"whose fields are empty apart from team_name and agent_name, and shows each hook's result.\n" +
			"It exits with 1 when a hook fails.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := a.client().TestEventHook(cmd.Context(), event, team, agent)
			if err != nil {
				return a.fail(err)
			}
			failed := false
			for _, r := range res.Results {
				failed = failed || r.ExitCode != 0 || r.Error != ""
			}
			a.emit(res, func(w io.Writer) {
				fmt.Fprintf(w, "Emitted %s -> %d hook(s) executed\n", res.Event, len(res.Results))
				for _, r := range res.Results {
					status := "OK"
					if r.ExitCode != 0 || r.Error != "" {
						status = fmt.Sprintf("FAILED (exit %d) %s", r.ExitCode, r.Error)
					}
					fmt.Fprintf(w, "  %s %s: %s\n", r.HookID, r.Command, status)
					if out := strings.TrimRight(r.Output, "\n"); out != "" {
						fmt.Fprintf(w, "    %s\n", strings.ReplaceAll(out, "\n", "\n    "))
					}
				}
			})
			if failed {
				return exitError{}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&event, "event", "e", "", "Event type to emit")
	cmd.Flags().StringVarP(&team, "team", "t", "test", "Team name in the event")
	cmd.Flags().StringVarP(&agent, "agent", "n", "test-agent", "Agent name in the event")
	cmd.MarkFlagRequired("event")
	return cmd
}
