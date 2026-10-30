package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/benlocal/team-worker/internal/model"
)

// MaxPlanLength is the maximum size of a plan body in bytes.
const MaxPlanLength = 1 << 20

// summaryRunes is how much of the body stands in for a missing summary.
const summaryRunes = 200

type SubmitPlanInput struct {
	Agent   string `json:"agent"`
	Summary string `json:"summary"`
	Plan    string `json:"plan"`
}

// DecidePlanInput approves or rejects a plan. Caller must be the team leader.
type DecidePlanInput struct {
	Agent    string `json:"agent"`
	Caller   string `json:"caller"`
	Feedback string `json:"feedback"`
}

const planColumns = "id, team, agent, summary, body, status, feedback, decided_by, created_at, decided_at"

func scanPlan(row scanner) (model.Plan, error) {
	var p model.Plan
	err := row.Scan(&p.ID, &p.Team, &p.Agent, &p.Summary, &p.Body, &p.Status, &p.Feedback, &p.DecidedBy, &p.CreatedAt, &p.DecidedAt)
	return p, err
}

// SubmitPlan stores a new pending plan and asks the leader to review it in
// the same transaction. Each submission gets a new id; earlier plans of the
// agent keep their status so the leader can still answer them.
func (s *Store) SubmitPlan(ctx context.Context, team string, in SubmitPlanInput) (model.Plan, error) {
	if err := validate(in.Agent, "agent name"); err != nil {
		return model.Plan{}, err
	}
	if strings.TrimSpace(in.Plan) == "" {
		return model.Plan{}, invalidf("Plan must not be empty")
	}
	if len(in.Plan) > MaxPlanLength {
		return model.Plan{}, invalidf("Plan must be text of at most %d bytes", MaxPlanLength)
	}
	summary := in.Summary
	if summary == "" {
		summary = firstRunes(in.Plan, summaryRunes)
	}
	p := model.Plan{ID: randomHex(12), Team: team, Agent: in.Agent, Summary: in.Summary, Body: in.Plan,
		Status: model.PlanPending, CreatedAt: s.timestamp()}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		leader, err := leaderInbox(ctx, tx, team)
		if err != nil {
			return err
		}
		if leader == "" {
			return notFoundf("No leader found")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO plans ("+planColumns+") VALUES (?, ?, ?, ?, ?, ?, '', '', ?, '')",
			p.ID, team, p.Agent, p.Summary, p.Body, p.Status, p.CreatedAt); err != nil {
			return err
		}
		return deliver(ctx, tx, team, leader, model.Message{Type: "plan_approval_request", From: p.Agent, To: leader,
			RequestID: p.ID, Timestamp: p.CreatedAt, Summary: summary, Plan: p.Body})
	})
	return p, err
}

// DecidePlan records the leader's decision on a pending plan and tells the
// agent in the same transaction.
func (s *Store) DecidePlan(ctx context.Context, team, id string, approve bool, in DecidePlanInput) (model.Plan, error) {
	var p model.Plan
	err := s.write(ctx, func(tx *sql.Tx) error {
		t, err := getTeam(ctx, tx, team)
		if err != nil {
			return err
		}
		if p, err = getPlan(ctx, tx, team, id); err != nil {
			return err
		}
		if in.Agent != "" && p.Agent != in.Agent {
			return invalidf("Plan '%s' was submitted by '%s', not '%s'", id, p.Agent, in.Agent)
		}
		if leader := leadMember(t); leader == nil || in.Caller != leader.Name {
			name := ""
			if leader != nil {
				name = leader.Name
			}
			return conflictf("Only the team leader (%s) can approve or reject plans; you are '%s'", name, in.Caller)
		}
		if p.Status != model.PlanPending {
			return conflictf("Plan '%s' is already %s", id, p.Status)
		}
		p.Status, p.Feedback, p.DecidedBy, p.DecidedAt = model.PlanRejected, in.Feedback, in.Caller, s.timestamp()
		kind := "plan_rejected"
		if approve {
			p.Status, kind = model.PlanApproved, "plan_approved"
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE plans SET status = ?, feedback = ?, decided_by = ?, decided_at = ? WHERE team = ? AND id = ?",
			p.Status, p.Feedback, p.DecidedBy, p.DecidedAt, team, id); err != nil {
			return err
		}
		inbox, err := resolveInbox(ctx, tx, team, p.Agent, "")
		if err != nil {
			return err
		}
		return deliver(ctx, tx, team, inbox, model.Message{Type: model.MessageType(kind), From: in.Caller, To: inbox,
			RequestID: p.ID, Timestamp: p.DecidedAt, Feedback: in.Feedback})
	})
	return p, err
}

func (s *Store) GetPlan(ctx context.Context, team, id string) (model.Plan, error) {
	if err := requireTeam(ctx, s.db, team); err != nil {
		return model.Plan{}, err
	}
	return getPlan(ctx, s.db, team, id)
}

func getPlan(ctx context.Context, q queryer, team, id string) (model.Plan, error) {
	p, err := scanPlan(q.QueryRowContext(ctx, "SELECT "+planColumns+" FROM plans WHERE team = ? AND id = ?", team, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, notFoundf("Plan '%s' not found in team '%s'", id, team)
	}
	return p, err
}

// ListPlans returns a team's plans in submission order, optionally only one
// agent's or one status.
func (s *Store) ListPlans(ctx context.Context, team, agent string, status model.PlanStatus) ([]model.Plan, error) {
	if status != "" && !model.ValidPlanStatus(status) {
		return nil, invalidf("Invalid plan status '%s'; use pending, approved or rejected", status)
	}
	if err := requireTeam(ctx, s.db, team); err != nil {
		return nil, err
	}
	query := "SELECT " + planColumns + " FROM plans WHERE team = ?"
	args := []any{team}
	if agent != "" {
		query += " AND agent = ?"
		args = append(args, agent)
	}
	if status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}
	rows, err := s.db.QueryContext(ctx, query+" ORDER BY created_at, id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := []model.Plan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

// SetPlanRequired turns plan approval on or off for one member.
func (s *Store) SetPlanRequired(ctx context.Context, team, name, user string, required bool) error {
	if err := validate(name, "member name"); err != nil {
		return err
	}
	if err := validateOptional(user, "user name"); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE members SET plan_required = ? WHERE team = ? AND name = ? AND user_name = ?",
			required, team, name, user)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFoundf("Member '%s' (user=%s) not found in team '%s'", name, user, team)
		}
		return nil
	})
}

// requireApprovedPlan stops a plan-required member from starting or finishing
// work before the leader approved one of its plans.
func requireApprovedPlan(ctx context.Context, q queryer, team, agent string) error {
	if agent == "" {
		return nil
	}
	var required bool
	err := q.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM members WHERE team = ? AND name = ? AND plan_required = 1)", team, agent).Scan(&required)
	if err != nil || !required {
		return err
	}
	var approved bool
	err = q.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM plans WHERE team = ? AND agent = ? AND status = 'approved')", team, agent).Scan(&approved)
	if err != nil || approved {
		return err
	}
	return conflictf("Agent '%s' needs an approved plan before starting or completing tasks. "+
		"Submit one with `team-worker plan submit %s %s <plan>` and wait for plan_approved.", agent, team, agent)
}

// leadMember returns the member who leads t, falling back to the first member.
func leadMember(t model.Team) *model.Member {
	for i := range t.Members {
		if t.Members[i].AgentID == t.LeadAgentID {
			return &t.Members[i]
		}
	}
	if len(t.Members) > 0 {
		return &t.Members[0]
	}
	return nil
}

func firstRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
