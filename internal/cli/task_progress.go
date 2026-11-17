package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (a *app) taskProgressCmd() *cobra.Command {
	var p model.TaskProgress
	cmd := &cobra.Command{
		Use:   "progress TEAM TASK_ID",
		Short: "Append a stage report without replacing the final result",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.client().UpdateTask(cmd.Context(), args[0], args[1], store.UpdateTaskInput{Progress: &p, Caller: config.IdentityFromEnv().AgentName})
			if err != nil {
				return a.fail(err)
			}
			a.emit(t, func(w io.Writer) {
				fmt.Fprintf(w, "OK Progress recorded for %s\n", t.ID)
				printTaskTracking(w, t)
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&p.Key, "key", "", "Stable report key; retry with identical contents")
	cmd.Flags().StringVar(&p.Summary, "summary", "", "Stage summary")
	cmd.Flags().StringVar(&p.State, "state", "active", "Execution state: active or waiting")
	cmd.Flags().StringVar(&p.WaitScope, "wait-scope", "", "Remaining work blocked by this trigger")
	cmd.Flags().StringVar(&p.IndependentWork, "independent-work", "", "Evidence that independent work is complete, split out, or unavailable")
	cmd.Flags().StringArrayVar(&p.IndependentTasks, "independent-task", nil, "Task ID for independent work (repeatable)")
	cmd.Flags().StringVar(&p.Reason, "reason", "", "Reason for waiting")
	cmd.Flags().StringVar(&p.NextAction, "next-action", "", "Action to take when the wait ends")
	cmd.Flags().StringVar(&p.CheckpointID, "checkpoint", "", "Registered checkpoint ID to wait for")
	cmd.Flags().StringVar(&p.RequestID, "request", "", "Pending decision request ID to wait for")
	cmd.Flags().StringArrayVar(&p.Artifacts, "artifact", nil, "Stage evidence path or URL (repeatable)")
	cmd.Flags().StringVar(&p.Verification, "verification", "", "Checks and outcomes for this stage")
	return cmd
}

func (a *app) taskReviewWaitCmd() *cobra.Command {
	var review model.TaskWaitReview
	cmd := &cobra.Command{
		Use:   "review-wait TEAM TASK_ID",
		Short: "Record the leader's evidence that remaining work must wait",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := a.client().UpdateTask(cmd.Context(), args[0], args[1], store.UpdateTaskInput{WaitReview: &review, Caller: config.IdentityFromEnv().AgentName})
			if err != nil {
				return a.fail(err)
			}
			a.emit(t, func(w io.Writer) { fmt.Fprintf(w, "OK Waiting scope reviewed for %s\n", t.ID); printTaskTracking(w, t) })
			return nil
		},
	}
	cmd.Flags().StringVar(&review.Version, "version", "", "Wait version from the task you inspected")
	cmd.Flags().StringVar(&review.Summary, "summary", "", "Evidence for remaining dependencies and independent work disposition")
	return cmd
}

func taskProgressText(t model.Task) string {
	if t.SupersededBy != "" {
		return "Superseded by " + t.SupersededBy
	}
	parts := []string{}
	if t.Kind == "goal" {
		parts = append(parts, "Goal")
	}
	if t.ParentID != "" {
		parts = append(parts, "Parent "+t.ParentID)
	}
	if len(t.Progress) > 0 {
		p := t.Progress[len(t.Progress)-1]
		state := p.State
		if t.ExecutionState != "" {
			state = t.ExecutionState
		}
		if t.Status == model.StatusCompleted {
			state = "last stage"
		}
		parts = append(parts, state+": "+p.Summary)
		if p.Reason != "" {
			parts = append(parts, p.Reason)
		}
		if p.State == "waiting" && p.CheckpointID == "" && p.RequestID == "" {
			parts = append(parts, "No registered trigger")
		}
	}
	if t.NeedsWaitReview() {
		parts = append(parts, "Waiting scope needs leader review")
	}
	if t.WaitingRequest != nil {
		parts = append(parts, "Request "+t.WaitingRequest.ID+": "+string(t.WaitingRequest.Status))
	}
	if t.WaitCheckpoint != nil {
		parts = append(parts, "Checkpoint "+t.WaitCheckpoint.ID+": "+t.WaitCheckpoint.Status)
	}
	if t.NextCheckpoint != nil {
		parts = append(parts, "Next "+t.NextCheckpoint.DueAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	return strings.Join(parts, "; ")
}

func printTaskTracking(w io.Writer, t model.Task) {
	if text := taskProgressText(t); text != "" {
		fmt.Fprintf(w, "  Progress: %s\n", text)
	}
	if len(t.Progress) > 0 && t.Progress[len(t.Progress)-1].State == "waiting" {
		fmt.Fprintf(w, "  Wait version: %s\n", model.TaskWaitVersion(t))
	}
	if t.WaitReview != nil {
		fmt.Fprintf(w, "  Wait review by %s (version %s): %s\n", t.WaitReview.ReviewedBy, t.WaitReview.Version, t.WaitReview.Summary)
	}
	for _, p := range t.Progress {
		fmt.Fprintf(w, "  Stage [%s] %s (%s, %s)\n", p.Key, p.Summary, p.State, p.ReportedAt)
		if p.WaitScope != "" {
			fmt.Fprintf(w, "    Waiting scope: %s\n", p.WaitScope)
		}
		if p.IndependentWork != "" {
			fmt.Fprintf(w, "    Independent work: %s\n", p.IndependentWork)
		}
		if len(p.IndependentTasks) > 0 {
			fmt.Fprintf(w, "    Independent tasks: %s\n", strings.Join(p.IndependentTasks, ", "))
		}
		if p.NextAction != "" {
			fmt.Fprintf(w, "    Next action: %s\n", p.NextAction)
		}
		for _, f := range p.Artifacts {
			fmt.Fprintf(w, "    Artifact: %s\n", f)
		}
		if p.Verification != "" {
			fmt.Fprintf(w, "    Verification: %s\n", p.Verification)
		}
	}
}
