package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (a *app) harnessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "harness",
		Short: "Plan-then-execute runs: discuss, plan, execute, verify, ship",
		Long: "A harness run moves a team through discuss → plan → execute → verify → ship.\n" +
			"Each phase has a gate checked by team-workerd: discussion.md, then spec.md plus an\n" +
			"approved planner plan, then all run tasks completed, then eval-report.json with\n" +
			"verdict pass (a failed verdict returns the run to execute). `harness conduct` starts\n" +
			"the role agents and advances automatically; the other commands work by hand.",
	}
	cmd.AddCommand(a.harnessStartCmd(), a.harnessStatusCmd(), a.harnessAdvanceCmd(), a.harnessApproveCmd(),
		a.harnessAbortCmd(), a.harnessArtifactCmd(), a.harnessArtifactsCmd(), a.harnessTaskCmd(), a.harnessConductCmd())
	return cmd
}

// ensureHarnessTeam creates the team, with the caller as leader, when it
// does not exist yet.
func (a *app) ensureHarnessTeam(ctx context.Context, team string) error {
	c := a.client()
	_, err := c.GetTeam(ctx, team)
	if err == nil || !client.IsNotFound(err) {
		return err
	}
	id := config.IdentityFromEnv()
	leader := os.Getenv("TEAM_WORKER_AGENT_NAME")
	if leader == "" {
		leader = "conductor"
	}
	_, err = c.CreateTeam(ctx, store.CreateTeamInput{Name: team, Description: "Harness team", LeaderName: leader, User: id.User})
	return err
}

func (a *app) startHarness(ctx context.Context, team string, in store.StartHarnessInput) (model.HarnessRun, error) {
	if err := a.ensureHarnessTeam(ctx, team); err != nil {
		return model.HarnessRun{}, err
	}
	return a.client().StartHarness(ctx, team, in)
}

func (a *app) harnessStartCmd() *cobra.Command {
	var team string
	var in store.StartHarnessInput
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a harness run in the discuss phase (creates the team when missing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			run, err := a.startHarness(cmd.Context(), team, in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"harness_id": run.ID, "team": run.Team, "phase": run.Phase}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Harness %s started for team '%s' in phase %s\n", run.ID, run.Team, run.Phase)
				fmt.Fprintf(w, "Run `team-worker harness conduct %s` to start the agents and advance automatically.\n", run.Team)
			})
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&in.Goal, "goal", "g", "", "What the run should achieve (required)")
	f.StringVarP(&team, "team", "t", "default", "Team name")
	f.StringVarP(&in.CLI, "cli", "c", "claude", "Agent CLI for the role agents")
	f.IntVarP(&in.AgentCount, "agents", "n", 3, "Number of executors")
	f.IntVar(&in.MaxAttempts, "max-attempts", 2, "Execute/verify attempts before a failed evaluation fails the run")
	cmd.MarkFlagRequired("goal")
	return cmd
}

func (a *app) harnessStatusCmd() *cobra.Command {
	var run string
	cmd := &cobra.Command{
		Use:   "status TEAM",
		Short: "Show the team's current (or given) harness run and its gate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := a.client().HarnessStatus(cmd.Context(), args[0], run)
			if err != nil {
				return a.fail(err)
			}
			a.emit(st, func(w io.Writer) { printHarnessStatus(w, st) })
			return nil
		},
	}
	cmd.Flags().StringVar(&run, "run", "", "Harness run ID (default: the running or newest run)")
	return cmd
}

func printHarnessStatus(w io.Writer, st model.HarnessStatus) {
	fmt.Fprintf(w, "Harness:  %s (%s)\nTeam:     %s\nGoal:     %s\n", st.ID, st.Status, st.Team, st.Goal)
	fmt.Fprintf(w, "Phase:    %s (attempt %d of %d)\n", st.Phase, st.Attempt, st.MaxAttempts)
	if st.CanAdvance {
		fmt.Fprintln(w, "Gate:     open")
	} else {
		fmt.Fprintln(w, "Gate:     closed")
	}
	if st.GateReason != "" {
		fmt.Fprintf(w, "          %s\n", st.GateReason)
	}
	if len(st.Artifacts) > 0 {
		fmt.Fprintf(w, "Artifacts: %s\n", strings.Join(st.Artifacts, ", "))
	}
	for _, ag := range st.Agents {
		fmt.Fprintf(w, "Agent:    %s (%s, started in %s)\n", ag.Name, ag.Role, ag.Phase)
	}
	for _, e := range st.History {
		line := fmt.Sprintf("History:  %s %s %s", formatTimestamp(e.At), e.Phase, e.Action)
		if e.Note != "" {
			line += ": " + truncate(e.Note, 80)
		}
		fmt.Fprintln(w, line)
	}
}

func (a *app) harnessAdvanceCmd() *cobra.Command {
	var run string
	cmd := &cobra.Command{
		Use:   "advance TEAM",
		Short: "Move the run to its next phase when the gate is open",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := a.client().AdvanceHarness(cmd.Context(), args[0], run)
			if err != nil {
				return a.fail(err)
			}
			a.emit(out, func(w io.Writer) { printAdvance(w, out) })
			return nil
		},
	}
	cmd.Flags().StringVar(&run, "run", "", "Harness run ID (default: the running run)")
	return cmd
}

func printAdvance(w io.Writer, out model.HarnessAdvance) {
	switch {
	case out.Status == model.HarnessFailed:
		fmt.Fprintf(w, "FAILED Harness %s: the evaluation failed and no attempts are left\n", out.ID)
	case out.RolledBack:
		fmt.Fprintf(w, "OK Harness %s: evaluation failed, back to execute (attempt %d)\n", out.ID, out.Attempt)
	case out.Status == model.HarnessCompleted:
		fmt.Fprintf(w, "OK Harness %s completed (%s → %s)\n", out.ID, out.From, out.Phase)
	default:
		fmt.Fprintf(w, "OK Harness %s: %s → %s\n", out.ID, out.From, out.Phase)
	}
}

func (a *app) harnessApproveCmd() *cobra.Command {
	var run, feedback string
	cmd := &cobra.Command{
		Use:   "approve TEAM",
		Short: "Approve the run planner's pending plan (leader only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := a.client().ApproveHarness(cmd.Context(), args[0], run, config.IdentityFromEnv().AgentName, feedback)
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": "approved", "planId": plan.ID, "agent": plan.Agent}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Plan %s from '%s' approved\n", plan.ID, plan.Agent)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&run, "run", "", "Harness run ID (default: the running run)")
	cmd.Flags().StringVarP(&feedback, "feedback", "f", "", "Feedback for the planner")
	return cmd
}

func (a *app) harnessAbortCmd() *cobra.Command {
	var run, reason string
	cmd := &cobra.Command{
		Use:   "abort TEAM",
		Short: "Abort the running run and stop its agents",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := a.client().AbortHarness(cmd.Context(), args[0], run, reason)
			if err != nil {
				return a.fail(err)
			}
			stopped := a.stopHarnessAgents(cmd.Context(), st.Team, st.Agents, "", a.stopAgent)
			a.emit(map[string]any{"status": "aborted", "harness_id": st.ID, "phase": st.Phase, "stopped": stopped}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Harness %s aborted in phase %s; %d agent(s) stopped\n", st.ID, st.Phase, len(stopped))
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&run, "run", "", "Harness run ID (default: the running run)")
	cmd.Flags().StringVarP(&reason, "reason", "r", "", "Why the run is aborted")
	return cmd
}

// harnessStopper stops one agent's process.
type harnessStopper func(ctx context.Context, team, name string) error

// stopHarnessAgents stops the live agents of a run, except those with role
// keep, and returns the names it stopped. Failures are reported, not fatal.
func (a *app) stopHarnessAgents(ctx context.Context, team string, agents []model.HarnessAgent, keep string, stop harnessStopper) []string {
	stopped := []string{}
	for _, ag := range agents {
		if ag.Role == keep || !a.harnessAgentAlive(ctx, team, ag.Name) {
			continue
		}
		if err := stop(ctx, team, ag.Name); err != nil {
			fmt.Fprintf(a.errOut, "stop %s: %v\n", ag.Name, err)
			continue
		}
		stopped = append(stopped, ag.Name)
	}
	return stopped
}

func (a *app) harnessAgentAlive(ctx context.Context, team, name string) bool {
	ag, err := a.client().GetAgent(ctx, team, name)
	return err == nil && ag.Status == model.AgentRunning && (ag.Alive == nil || *ag.Alive)
}

// stopAgent stops an agent the way `agent stop` does: the daemon saves its
// session, marks the stop as intended, ends the process and releases its
// tasks.
func (a *app) stopAgent(ctx context.Context, team, name string) error {
	_, err := a.client().TerminateAgent(ctx, team, name)
	return err
}

func (a *app) harnessArtifactCmd() *cobra.Command {
	var run, file, content string
	cmd := &cobra.Command{
		Use:   "artifact TEAM NAME",
		Short: "Register or replace a run artifact (from --file, --content or stdin)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := content
			switch {
			case file != "" && cmd.Flags().Changed("content"):
				return a.fail(errors.New("use either --file or --content"))
			case file != "":
				data, err := os.ReadFile(file)
				if err != nil {
					return a.fail(err)
				}
				body = string(data)
			case !cmd.Flags().Changed("content"):
				data, err := io.ReadAll(io.LimitReader(a.in, store.MaxArtifactLength+1))
				if err != nil {
					return a.fail(err)
				}
				body = string(data)
			}
			art, err := a.client().PutArtifact(cmd.Context(), args[0], run, args[1], body)
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]any{"status": "registered", "name": art.Name, "size": art.Size}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Artifact %s registered (%d bytes)\n", art.Name, art.Size)
			})
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&run, "run", "", "Harness run ID (default: the running run)")
	f.StringVar(&file, "file", "", "Read the artifact from this file")
	f.StringVar(&content, "content", "", "Artifact content")
	return cmd
}

func (a *app) harnessArtifactsCmd() *cobra.Command {
	var run, show string
	cmd := &cobra.Command{
		Use:   "artifacts TEAM",
		Short: "List a run's artifacts, or print one with --show",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := a.client()
			if show != "" {
				art, err := c.GetArtifact(cmd.Context(), args[0], run, show)
				if err != nil {
					return a.fail(err)
				}
				a.emit(art, func(w io.Writer) { fmt.Fprint(w, art.Content) })
				return nil
			}
			arts, err := c.ListArtifacts(cmd.Context(), args[0], run)
			if err != nil {
				return a.fail(err)
			}
			a.emit(arts, func(w io.Writer) {
				if len(arts) == 0 {
					fmt.Fprintln(w, "No artifacts")
					return
				}
				tw := newTable(w, "NAME", "SIZE", "UPDATED")
				for _, art := range arts {
					row(tw, art.Name, fmt.Sprint(art.Size), formatTimestamp(art.UpdatedAt))
				}
				tw.Flush()
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&run, "run", "", "Harness run ID (default: the running or newest run)")
	cmd.Flags().StringVar(&show, "show", "", "Print this artifact's content")
	return cmd
}

func (a *app) harnessTaskCmd() *cobra.Command {
	var run, blockedBy string
	var in store.CreateTaskInput
	cmd := &cobra.Command{
		Use:   "task TEAM SUBJECT",
		Short: "Create a task that belongs to the run (the execute gate waits for it)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.Subject = args[1]
			in.BlockedBy = splitList(blockedBy)
			t, err := a.client().CreateHarnessTask(cmd.Context(), args[0], run, in)
			if err != nil {
				return a.fail(err)
			}
			a.emit(t, func(w io.Writer) { fmt.Fprintf(w, "OK Harness task created: %s\n  Subject: %s\n", t.ID, t.Subject) })
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&run, "run", "", "Harness run ID (default: the running run)")
	f.StringVarP(&in.Description, "description", "d", "", "Task description")
	f.StringVarP(&in.Owner, "owner", "o", "", "Owner (default: unassigned)")
	f.StringVar(&blockedBy, "blocked-by", "", "Comma-separated task IDs this task waits for")
	return cmd
}

func (a *app) harnessConductCmd() *cobra.Command {
	var in store.StartHarnessInput
	var poll time.Duration
	var stopIt bool
	cmd := &cobra.Command{
		Use:   "conduct TEAM",
		Short: "Have the daemon start the role agents of each phase and advance the run",
		Long: "Has the daemon conduct the team's running run, or start one when --goal is given. Every\n" +
			"poll it reloads the run, starts the agents the phase needs (planner, executors, evaluator),\n" +
			"stops those of finished phases and advances when the gate opens, until the run ends. It\n" +
			"returns at once; follow with harness status. --stop ends conducting; the run keeps running.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, team, c := cmd.Context(), args[0], a.client()
			if stopIt {
				stopped, err := c.StopConducting(ctx, team)
				if err != nil {
					return a.fail(err)
				}
				a.emit(map[string]any{"team": team, "stopped": stopped}, func(w io.Writer) {
					if stopped {
						fmt.Fprintf(w, "OK Stopped conducting '%s'; its harness run keeps running\n", team)
					} else {
						fmt.Fprintf(w, "No conductor runs for '%s'\n", team)
					}
				})
				return nil
			}
			st, err := c.HarnessStatus(ctx, team, "")
			if in.Goal != "" && (err != nil && client.IsNotFound(err) || err == nil && st.Status != model.HarnessRunning) {
				if _, err = a.startHarness(ctx, team, in); err != nil {
					return a.fail(err)
				}
			} else if err != nil {
				return a.fail(err)
			}
			out, err := c.Conduct(ctx, team, poll)
			if err != nil {
				return a.fail(err)
			}
			a.emit(out, func(w io.Writer) {
				if out.Started {
					fmt.Fprintf(w, "OK The daemon conducts harness run %s of '%s'; follow with team-worker harness status %s\n", out.Run, team, team)
				} else {
					fmt.Fprintf(w, "OK Harness run %s of '%s' is already being conducted\n", out.Run, team)
				}
			})
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&in.Goal, "goal", "g", "", "Start a new run with this goal when none is running")
	f.StringVarP(&in.CLI, "cli", "c", "claude", "Agent CLI for a new run")
	f.IntVarP(&in.AgentCount, "agents", "n", 3, "Number of executors for a new run")
	f.IntVar(&in.MaxAttempts, "max-attempts", 2, "Execute/verify attempts for a new run")
	f.DurationVar(&poll, "poll", 5*time.Second, "How often the daemon checks the run")
	f.BoolVar(&stopIt, "stop", false, "Stop conducting; the run keeps running")
	return cmd
}
