package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func (a *app) planCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "plan", Short: "Plan approval: members submit plans, the leader approves or rejects them"}
	cmd.AddCommand(a.planSubmitCmd(), a.planDecideCmd(true), a.planDecideCmd(false), a.planListCmd(), a.planShowCmd())
	return cmd
}

func (a *app) planSubmitCmd() *cobra.Command {
	var summary string
	cmd := &cobra.Command{
		Use:   "submit TEAM AGENT PLAN",
		Short: "Submit a plan for the leader's approval (PLAN is text or a path to a file)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := args[2]
			if info, err := os.Stat(body); err == nil && info.Mode().IsRegular() {
				data, err := os.ReadFile(body)
				if err != nil {
					return a.fail(err)
				}
				body = string(data)
			}
			plan, err := a.client().SubmitPlan(cmd.Context(), args[0], store.SubmitPlanInput{Agent: args[1], Summary: summary, Plan: body})
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": "submitted", "planId": plan.ID, "agent": plan.Agent}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Plan %s submitted for '%s'; waiting for the leader's decision\n", plan.ID, plan.Agent)
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&summary, "summary", "s", "", "Short summary (default: the start of the plan)")
	return cmd
}

func (a *app) planDecideCmd(approve bool) *cobra.Command {
	var feedback string
	use, short, status := "reject", "Reject a pending plan (leader only)", "rejected"
	if approve {
		use, short, status = "approve", "Approve a pending plan (leader only)", "approved"
	}
	cmd := &cobra.Command{
		Use:   use + " TEAM PLAN_ID AGENT",
		Short: short,
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := a.client().DecidePlan(cmd.Context(), args[0], args[1], approve, store.DecidePlanInput{
				Agent: args[2], Caller: config.IdentityFromEnv().AgentName, Feedback: feedback})
			if err != nil {
				return a.fail(err)
			}
			a.emit(map[string]string{"status": status, "planId": plan.ID}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Plan %s %s; '%s' has been told\n", plan.ID, status, plan.Agent)
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&feedback, "feedback", "f", "", "Feedback for the agent")
	return cmd
}

func (a *app) planListCmd() *cobra.Command {
	var agent, status string
	cmd := &cobra.Command{
		Use:   "list TEAM",
		Short: "List a team's plans",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			plans, err := a.client().ListPlans(cmd.Context(), args[0], agent, model.PlanStatus(status))
			if err != nil {
				return a.fail(err)
			}
			a.emit(plans, func(w io.Writer) {
				if len(plans) == 0 {
					fmt.Fprintln(w, "No plans")
					return
				}
				tw := newTable(w, "ID", "AGENT", "STATUS", "SUBMITTED", "SUMMARY")
				for _, p := range plans {
					summary := p.Summary
					if summary == "" {
						summary = planFirstLine(p.Body)
					}
					row(tw, p.ID, p.Agent, string(p.Status), formatTimestamp(p.CreatedAt), truncate(summary, 60))
				}
				tw.Flush()
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&agent, "agent", "a", "", "Only this agent's plans")
	cmd.Flags().StringVarP(&status, "status", "s", "", "Only plans with this status (pending, approved, rejected)")
	return cmd
}

func (a *app) planShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show TEAM PLAN_ID",
		Short: "Show one plan and its decision",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.client().GetPlan(cmd.Context(), args[0], args[1])
			if err != nil {
				return a.fail(err)
			}
			a.emit(p, func(w io.Writer) {
				fmt.Fprintf(w, "Plan:      %s\nAgent:     %s\nStatus:    %s\nSubmitted: %s\n", p.ID, p.Agent, p.Status, formatTimestamp(p.CreatedAt))
				if p.DecidedBy != "" {
					fmt.Fprintf(w, "Decided:   %s by %s\n", formatTimestamp(p.DecidedAt), p.DecidedBy)
				}
				if p.Feedback != "" {
					fmt.Fprintf(w, "Feedback:  %s\n", p.Feedback)
				}
				if p.Summary != "" {
					fmt.Fprintf(w, "Summary:   %s\n", p.Summary)
				}
				fmt.Fprintf(w, "\n%s\n", p.Body)
			})
			return nil
		},
	}
}

func planFirstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// planApprovalPrompt is added to the first prompt of plan-required agents.
func planApprovalPrompt(team, agent string) string {
	return fmt.Sprintf(`
## Plan approval

- Before starting any task, write your plan and submit it:
  `+"`team-worker plan submit %s %s <plan text or file> --summary \"<one line>\"`"+`
- Then wait for a plan_approved message in your inbox. Until a plan is approved,
  moving a task to in_progress or completed is refused.
- If the leader sends plan_rejected, read the feedback, revise the plan and submit it again.
`, team, agent)
}
