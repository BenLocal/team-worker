package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"

	"github.com/benlocal/team-worker/internal/model"
)

const costColumns = "id, agent, provider, model, input_tokens, output_tokens, cost_cents, source, session_id, reported_at"

func scanCost(row scanner) (model.CostEvent, error) {
	var e model.CostEvent
	err := row.Scan(&e.ID, &e.AgentName, &e.Provider, &e.Model, &e.InputTokens, &e.OutputTokens, &e.CostCents, &e.Source, &e.SessionID, &e.ReportedAt)
	return e, err
}

func validCost(e model.CostEvent) error {
	if err := validate(e.AgentName, "agent name"); err != nil {
		return err
	}
	if e.InputTokens < 0 || e.OutputTokens < 0 || e.CostCents < 0 || math.IsNaN(e.CostCents) || math.IsInf(e.CostCents, 0) {
		return invalidf("Token counts and cost must not be negative")
	}
	if len(e.Provider) > 200 || len(e.Model) > 200 || len(e.SessionID) > 1024 {
		return invalidf("Provider, model or session id is too long")
	}
	return nil
}

// ReportCost records a usage report from an agent or a person.
func (s *Store) ReportCost(ctx context.Context, team string, e model.CostEvent) (model.CostEvent, error) {
	e.Source, e.SessionID = "report", ""
	if err := validCost(e); err != nil {
		return e, err
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		e.ID, e.ReportedAt = randomHex(8), s.timestamp()
		if _, err := tx.ExecContext(ctx, "INSERT INTO cost_events (team, "+costColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			team, e.ID, e.AgentName, e.Provider, e.Model, e.InputTokens, e.OutputTokens, e.CostCents, e.Source, e.SessionID, e.ReportedAt); err != nil {
			return err
		}
		return s.alertBudget(ctx, tx, team)
	})
	return e, err
}

// RecordSessionUsage stores the running totals of one native session,
// replacing any earlier capture of the same session.
func (s *Store) RecordSessionUsage(ctx context.Context, team string, e model.CostEvent) (model.CostEvent, error) {
	e.Source = "session"
	if err := validCost(e); err != nil {
		return e, err
	}
	if e.SessionID == "" {
		return e, invalidf("A session id is required")
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		e.ID, e.ReportedAt = randomHex(8), s.timestamp()
		if err := tx.QueryRowContext(ctx, `INSERT INTO cost_events (team, `+costColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(team, agent, session_id) WHERE source = 'session' DO UPDATE SET
			  provider = excluded.provider, model = excluded.model, input_tokens = excluded.input_tokens,
			  output_tokens = excluded.output_tokens, cost_cents = excluded.cost_cents, reported_at = excluded.reported_at
			RETURNING id`,
			team, e.ID, e.AgentName, e.Provider, e.Model, e.InputTokens, e.OutputTokens, e.CostCents, e.Source, e.SessionID, e.ReportedAt).Scan(&e.ID); err != nil {
			return err
		}
		return s.alertBudget(ctx, tx, team)
	})
	return e, err
}

// Costs returns the team-wide summary and the events, optionally only one
// agent's; the summary always covers the whole team, as in ClawTeam.
func (s *Store) Costs(ctx context.Context, team, agent string) (model.CostView, error) {
	view := model.CostView{Events: []model.CostEvent{}}
	if err := requireTeam(ctx, s.db, team); err != nil {
		return view, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+costColumns+" FROM cost_events WHERE team = ? ORDER BY reported_at, rowid", team)
	if err != nil {
		return view, err
	}
	defer rows.Close()
	sum := model.CostSummary{TeamName: team, ByAgent: map[string]float64{}, ByAgentTokens: map[string]model.TokenCount{}}
	for rows.Next() {
		e, err := scanCost(rows)
		if err != nil {
			return view, err
		}
		sum.TotalCostCents += e.CostCents
		sum.TotalInputTokens += e.InputTokens
		sum.TotalOutputTokens += e.OutputTokens
		sum.ByAgent[e.AgentName] += e.CostCents
		tc := sum.ByAgentTokens[e.AgentName]
		tc.InputTokens += e.InputTokens
		tc.OutputTokens += e.OutputTokens
		sum.ByAgentTokens[e.AgentName] = tc
		sum.EventCount++
		if agent == "" || e.AgentName == agent {
			view.Events = append(view.Events, e)
		}
	}
	if err := rows.Err(); err != nil {
		return view, err
	}
	view.Summary = sum
	b, err := budget(ctx, s.db, team)
	view.BudgetCents, view.BudgetEnforce = b.BudgetCents, b.Enforce
	return view, err
}

// Budget reports a team's limit and spending.
func (s *Store) Budget(ctx context.Context, team string) (model.Budget, error) {
	if err := requireTeam(ctx, s.db, team); err != nil {
		return model.Budget{}, err
	}
	return budget(ctx, s.db, team)
}

// SetBudget sets the limit in cents (0 is unlimited) and whether reaching it
// blocks new work. The leader is alerted again the next time it is reached.
func (s *Store) SetBudget(ctx context.Context, team string, cents float64, enforce bool) (model.Budget, error) {
	if cents < 0 || math.IsNaN(cents) || math.IsInf(cents, 0) {
		return model.Budget{}, invalidf("Budget must not be negative")
	}
	var out model.Budget
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE teams SET budget_cents = ?, budget_enforce = ?, budget_alerted = 0 WHERE name = ?",
			cents, enforce, team); err != nil {
			return err
		}
		if err := s.alertBudget(ctx, tx, team); err != nil {
			return err
		}
		var err error
		out, err = budget(ctx, tx, team)
		return err
	})
	return out, err
}

func budget(ctx context.Context, q queryer, team string) (model.Budget, error) {
	b := model.Budget{Team: team}
	err := q.QueryRowContext(ctx, `SELECT t.budget_cents, t.budget_enforce,
		  (SELECT COALESCE(SUM(cost_cents), 0) FROM cost_events WHERE team = t.name)
		FROM teams t WHERE t.name = ?`, team).Scan(&b.BudgetCents, &b.Enforce, &b.SpentCents)
	b.Exhausted = b.Enforce && b.BudgetCents > 0 && b.SpentCents >= b.BudgetCents
	return b, err
}

// budgetExhausted is the one check behind blocked task claims and spawns.
func budgetExhausted(ctx context.Context, q queryer, team string) error {
	b, err := budget(ctx, q, team)
	if err != nil || !b.Exhausted {
		return err
	}
	return conflictf("Team budget is exhausted ($%.2f of $%.2f); raise it with team-worker cost budget %s <dollars>",
		b.SpentCents/100, b.BudgetCents/100, team)
}

// alertBudget tells the leader once when spending first reaches the budget.
func (s *Store) alertBudget(ctx context.Context, tx *sql.Tx, team string) error {
	var alerted bool
	if err := tx.QueryRowContext(ctx, "SELECT budget_alerted FROM teams WHERE name = ?", team).Scan(&alerted); err != nil {
		return err
	}
	b, err := budget(ctx, tx, team)
	if err != nil || alerted || b.BudgetCents <= 0 || b.SpentCents < b.BudgetCents {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE teams SET budget_alerted = 1 WHERE name = ?", team); err != nil {
		return err
	}
	leader, err := leaderInbox(ctx, tx, team)
	if err != nil || leader == "" {
		return err
	}
	effect := "Work continues; budgets only warn unless enforced with `team-worker cost budget --enforce`."
	if b.Enforce {
		effect = "New task claims and agent spawns are blocked until the budget is raised."
	}
	return deliver(ctx, tx, team, leader, model.Message{
		Type:      "message",
		From:      "team-worker",
		To:        leader,
		Content:   fmt.Sprintf("Team budget reached: spent $%.2f of $%.2f. %s", b.SpentCents/100, b.BudgetCents/100, effect),
		RequestID: randomHex(12),
		Timestamp: s.timestamp(),
	})
}
