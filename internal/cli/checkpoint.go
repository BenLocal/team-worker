package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (a *app) checkpointCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "checkpoint", Short: "Schedule and track a single task observation"}
	cmd.AddCommand(a.checkpointCreateCmd(), a.checkpointListCmd(), a.checkpointShowCmd())
	for _, action := range []string{"ack", "complete", "cancel"} {
		cmd.AddCommand(a.checkpointUpdateCmd(action))
	}
	return cmd
}

func (a *app) checkpointCreateCmd() *cobra.Command {
	var in store.CreateCheckpointInput
	var due, resultDue, caller string
	var ackTimeout, escalate time.Duration
	cmd := &cobra.Command{Use: "create TEAM TASK", Short: "Register a checkpoint for the current task owner; reuse --key for retries", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		var err error
		in.DueAt, err = time.Parse(time.RFC3339Nano, due)
		if err != nil {
			return a.fail(fmt.Errorf("invalid --at (use RFC3339 with time zone): %w", err))
		}
		in.ResultDueAt, err = time.Parse(time.RFC3339Nano, resultDue)
		if err != nil {
			return a.fail(fmt.Errorf("invalid --result-by (use RFC3339 with time zone): %w", err))
		}
		if ackTimeout%time.Second != 0 || escalate%time.Second != 0 {
			return a.fail(fmt.Errorf("timeouts must be whole seconds"))
		}
		in.AckTimeoutSeconds, in.EscalateAfterSeconds = int64(ackTimeout/time.Second), int64(escalate/time.Second)
		in.TaskID, in.Caller = args[1], checkpointCallerName(caller)
		cp, err := a.client().CreateCheckpoint(cmd.Context(), args[0], in)
		if err != nil {
			return a.fail(err)
		}
		a.emitCheckpoint(cp)
		return nil
	}}
	cmd.Flags().StringVar(&caller, "caller", "", "Act as this member or operator (default: current identity, or operator outside an agent)")
	cmd.Flags().StringVar(&in.Key, "key", "", "Stable registration key; different observations need different keys")
	cmd.Flags().StringVar(&due, "at", "", "Observation time (RFC3339, including offset)")
	cmd.Flags().StringVar(&resultDue, "result-by", "", "Result deadline (RFC3339, including offset)")
	cmd.Flags().DurationVar(&ackTimeout, "ack-timeout", 5*time.Minute, "Time after --at allowed to acknowledge")
	cmd.Flags().DurationVar(&escalate, "escalate-after", 10*time.Minute, "Time after leader alert before notifying operator")
	cmd.Flags().StringVar(&in.Action, "action", "", "Observation to perform")
	cmd.Flags().StringVar(&in.ExpectedEvidence, "expect", "", "Expected evidence")
	return cmd
}

func (a *app) checkpointListCmd() *cobra.Command {
	return &cobra.Command{Use: "list TEAM", Short: "List persisted checkpoints, including completed and canceled ones", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cps, err := a.client().ListCheckpoints(cmd.Context(), args[0])
		if err != nil {
			return a.fail(err)
		}
		a.emit(cps, func(w io.Writer) {
			tw := newTable(w, "ID", "TASK", "OWNER", "STATUS", "DUE", "TIMEOUT")
			for _, cp := range cps {
				row(tw, cp.ID, cp.TaskID, cp.Owner, cp.Status, cp.DueAt.Format(time.RFC3339), cp.TimeoutReason)
			}
			tw.Flush()
		})
		return nil
	}}
}

func (a *app) checkpointShowCmd() *cobra.Command {
	return &cobra.Command{Use: "show TEAM ID", Short: "Show checkpoint state, deadlines and evidence", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		cp, err := a.client().GetCheckpoint(cmd.Context(), args[0], args[1])
		if err != nil {
			return a.fail(err)
		}
		a.emitCheckpoint(cp)
		return nil
	}}
}

func (a *app) checkpointUpdateCmd(action string) *cobra.Command {
	var in store.CheckpointUpdateInput
	var caller string
	cmd := &cobra.Command{Use: action + " TEAM ID", Short: map[string]string{"ack": "Acknowledge as the owner and return the execution token", "complete": "Report an observation result; does not complete the task", "cancel": "Cancel an observation with a reason"}[action], Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		in.Action, in.Caller = action, checkpointCallerName(caller)
		cp, err := a.client().UpdateCheckpoint(cmd.Context(), args[0], args[1], in)
		if err != nil {
			return a.fail(err)
		}
		a.emitCheckpoint(cp)
		return nil
	}}
	cmd.Flags().StringVar(&caller, "caller", "", "Act as this member or operator (default: current identity, or operator outside an agent)")
	if action == "complete" {
		cmd.Flags().StringVar(&in.Token, "token", "", "Token returned by ack")
		cmd.Flags().StringVar(&in.Outcome, "outcome", "", "passed, failed or inconclusive")
		cmd.Flags().StringVar(&in.Summary, "summary", "", "Observed result")
		cmd.Flags().StringVar(&in.Evidence, "evidence", "", "Evidence location and checked version")
	}
	if action == "cancel" {
		cmd.Flags().StringVar(&in.Reason, "reason", "", "Why this checkpoint is canceled")
	}
	return cmd
}

func (a *app) emitCheckpoint(cp model.Checkpoint) {
	a.emit(cp, func(w io.Writer) {
		fmt.Fprintf(w, "Checkpoint %s: %s\nTask: %s\nOwner: %s\nDue: %s\nResult by: %s\nAction: %s\nExpected evidence: %s\n", cp.ID, cp.Status, cp.TaskID, cp.Owner, cp.DueAt.Format(time.RFC3339), cp.ResultDueAt.Format(time.RFC3339), cp.Action, cp.ExpectedEvidence)
		if cp.Token != "" {
			fmt.Fprintf(w, "Token: %s\n", cp.Token)
		}
		if cp.TimeoutReason != "" {
			fmt.Fprintf(w, "Timeout: %s\nLeader alert: %s\nOperator alert: %s\n", cp.TimeoutReason, cp.LeaderAlertAt, cp.OperatorAlertAt)
		}
		if cp.Outcome != "" {
			fmt.Fprintf(w, "Outcome: %s\nSummary: %s\nEvidence: %s\n", cp.Outcome, cp.Summary, cp.Evidence)
		}
		if cp.CancelReason != "" {
			fmt.Fprintf(w, "Canceled: %s\n", cp.CancelReason)
		}
	})
}

func checkpointCallerName(override string) string {
	if override != "" {
		return override
	}
	if os.Getenv("TEAM_WORKER_AGENT_NAME") == "" {
		return store.OperatorInbox
	}
	id := config.IdentityFromEnv()
	if id.AgentName == store.OperatorInbox {
		return store.OperatorInbox
	}
	return (model.Member{Name: id.AgentName, User: id.User}).InboxName()
}
