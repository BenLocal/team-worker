package cli

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/usage"
)

func (a *app) costCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "cost", Short: "Token usage, cost reports and team budgets"}
	cmd.AddCommand(a.costReportCmd(), a.costShowCmd(), a.costBudgetCmd(), a.costSyncCmd())
	return cmd
}

func dollars(cents float64) string { return fmt.Sprintf("$%.2f", cents/100) }

func (a *app) costReportCmd() *cobra.Command {
	var e model.CostEvent
	cmd := &cobra.Command{
		Use:   "report TEAM",
		Short: "Record token usage and cost",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := a.client().ReportCost(cmd.Context(), args[0], e)
			if err != nil {
				return a.fail(err)
			}
			b, budgetErr := a.client().Budget(cmd.Context(), args[0])
			a.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "OK Cost reported: %d in / %d out tokens, %s\n", out.InputTokens, out.OutputTokens, dollars(out.CostCents))
				if budgetErr == nil && b.BudgetCents > 0 && b.SpentCents > b.BudgetCents {
					fmt.Fprintf(w, "WARNING: Budget exceeded! Spent %s / %s\n", dollars(b.SpentCents), dollars(b.BudgetCents))
				}
			})
			return nil
		},
	}
	f := cmd.Flags()
	f.Int64Var(&e.InputTokens, "input-tokens", 0, "Input tokens used")
	f.Int64Var(&e.OutputTokens, "output-tokens", 0, "Output tokens used")
	f.Float64Var(&e.CostCents, "cost-cents", 0, "Cost in cents")
	f.StringVar(&e.Provider, "provider", "", "Provider name")
	f.StringVar(&e.Model, "model", "", "Model name")
	f.StringVarP(&e.AgentName, "agent", "a", config.IdentityFromEnv().AgentName, "Agent name (default: from env)")
	return cmd
}

func (a *app) costShowCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "show TEAM",
		Short: "Show a team's usage, cost and budget",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			view, err := a.client().Costs(cmd.Context(), args[0], agent)
			if err != nil {
				return a.fail(err)
			}
			a.emit(view, func(w io.Writer) { printCosts(w, view) })
			return nil
		},
	}
	cmd.Flags().StringVarP(&agent, "agent", "a", "", "Only list this agent's events")
	return cmd
}

func printCosts(w io.Writer, v model.CostView) {
	s := v.Summary
	budget := "no budget"
	if v.BudgetCents > 0 {
		budget = fmt.Sprintf("budget %s", dollars(v.BudgetCents))
		if v.BudgetEnforce {
			budget += " (enforced)"
		}
	}
	fmt.Fprintf(w, "Team %s: %s spent, %s; %d in / %d out tokens in %d event(s)\n",
		s.TeamName, dollars(s.TotalCostCents), budget, s.TotalInputTokens, s.TotalOutputTokens, s.EventCount)
	agents := make([]string, 0, len(s.ByAgentTokens))
	for name := range s.ByAgentTokens {
		agents = append(agents, name)
	}
	sort.Strings(agents)
	if len(agents) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "AGENT\tCOST\tINPUT\tOUTPUT")
		for _, name := range agents {
			t := s.ByAgentTokens[name]
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\n", name, dollars(s.ByAgent[name]), t.InputTokens, t.OutputTokens)
		}
		tw.Flush()
	}
}

func (a *app) costBudgetCmd() *cobra.Command {
	var enforce bool
	cmd := &cobra.Command{
		Use:   "budget TEAM DOLLARS",
		Short: "Set the team budget in dollars (0 for unlimited)",
		Long: "Set the team budget. Reaching it alerts the leader once; with --enforce it also\n" +
			"blocks new task claims and agent spawns until the budget is raised.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var amount float64
			if _, err := fmt.Sscanf(args[1], "%g", &amount); err != nil || amount < 0 || math.IsInf(amount, 0) || math.IsNaN(amount) {
				return a.fail(fmt.Errorf("budget must be a non-negative number of dollars"))
			}
			b, err := a.client().SetBudget(cmd.Context(), args[0], math.Round(amount*10000)/100, enforce)
			if err != nil {
				return a.fail(err)
			}
			out := map[string]any{"status": "set", "team": args[0], "budgetDollars": b.BudgetCents / 100, "enforce": b.Enforce}
			a.emit(out, func(w io.Writer) {
				switch {
				case b.BudgetCents == 0:
					fmt.Fprintf(w, "OK Budget for '%s' removed\n", args[0])
				case b.Enforce:
					fmt.Fprintf(w, "OK Budget for '%s' set to %s; reaching it blocks new work\n", args[0], dollars(b.BudgetCents))
				default:
					fmt.Fprintf(w, "OK Budget for '%s' set to %s\n", args[0], dollars(b.BudgetCents))
				}
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&enforce, "enforce", false, "Block new task claims and spawns once the budget is spent")
	return cmd
}

func (a *app) costSyncCmd() *cobra.Command {
	var agent string
	cmd := &cobra.Command{
		Use:   "sync TEAM",
		Short: "Record token usage from the agents' native sessions now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			agents, err := a.client().ListAgents(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			recorded := []model.CostEvent{}
			for _, ag := range agents {
				if agent != "" && ag.Name != agent {
					continue
				}
				e, ok, err := a.syncSessionUsage(cmd.Context(), args[0], ag.Name)
				if err != nil {
					return a.fail(fmt.Errorf("%s: %w", ag.Name, err))
				}
				if ok {
					recorded = append(recorded, e)
				}
			}
			a.emit(map[string]any{"team": args[0], "recorded": recorded}, func(w io.Writer) {
				fmt.Fprintf(w, "OK Recorded session usage for %d agent(s)\n", len(recorded))
			})
			return nil
		},
	}
	cmd.Flags().StringVarP(&agent, "agent", "a", "", "Only this agent")
	return cmd
}

// syncSessionUsage records the token totals of an agent's native session, if
// its CLI keeps a session file team-worker can read.
func (a *app) syncSessionUsage(ctx context.Context, team, name string) (model.CostEvent, bool, error) {
	session, err := a.client().GetSession(ctx, team, name, config.IdentityFromEnv().User)
	if client.IsNotFound(err) {
		return model.CostEvent{}, false, nil
	}
	if err != nil {
		return model.CostEvent{}, false, err
	}
	t, ok, err := usage.Session(session.Client, session.SessionID)
	if err != nil || !ok {
		return model.CostEvent{}, false, err
	}
	e, err := a.client().RecordSessionUsage(ctx, team, model.CostEvent{
		AgentName: name, Provider: t.Provider, Model: t.Model, SessionID: session.SessionID,
		InputTokens: t.Input, OutputTokens: t.Output,
	})
	return e, err == nil, err
}

// requireBudget refuses new work when the team's enforced budget is spent.
func (a *app) requireBudget(ctx context.Context, team string) error {
	b, err := a.client().Budget(ctx, team)
	if err != nil || !b.Exhausted {
		return err
	}
	return fmt.Errorf("team budget is exhausted (%s of %s); raise it with team-worker cost budget %s <dollars>",
		dollars(b.SpentCents), dollars(b.BudgetCents), team)
}
