package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/benlocal/team-worker/internal/model"
)

// MaxArtifactLength is the largest artifact a run stores, in bytes.
const MaxArtifactLength = 1 << 20

var artifactName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// StartHarnessInput starts a run. Zero values take the defaults.
type StartHarnessInput struct {
	Goal        string `json:"goal"`
	CLI         string `json:"cli"`
	AgentCount  int    `json:"agentCount"`
	MaxAttempts int    `json:"maxAttempts"`
}

// HarnessAgentInput records an agent the conductor started for a run.
type HarnessAgentInput struct {
	Name  string `json:"name"`
	Role  string `json:"role"`
	Phase string `json:"phase"`
}

const harnessColumns = "id, team, goal, cli, agent_count, current_phase, status, attempt, max_attempts, created_at, updated_at"

func scanHarness(row scanner) (model.HarnessRun, error) {
	var r model.HarnessRun
	err := row.Scan(&r.ID, &r.Team, &r.Goal, &r.CLI, &r.AgentCount, &r.Phase, &r.Status, &r.Attempt, &r.MaxAttempts,
		&r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// StartHarness creates a run in the discuss phase. A team has at most one
// running run.
func (s *Store) StartHarness(ctx context.Context, team string, in StartHarnessInput) (model.HarnessRun, error) {
	if strings.TrimSpace(in.Goal) == "" {
		return model.HarnessRun{}, invalidf("Harness goal must not be empty")
	}
	if in.CLI == "" {
		in.CLI = "claude"
	}
	if in.AgentCount == 0 {
		in.AgentCount = 3
	}
	if in.MaxAttempts == 0 {
		in.MaxAttempts = 2
	}
	if in.AgentCount < 1 || in.AgentCount > 20 {
		return model.HarnessRun{}, invalidf("Agent count must be between 1 and 20")
	}
	if in.MaxAttempts < 1 || in.MaxAttempts > 10 {
		return model.HarnessRun{}, invalidf("Max attempts must be between 1 and 10")
	}
	now := s.timestamp()
	r := model.HarnessRun{ID: randomHex(12), Team: team, Goal: in.Goal, CLI: in.CLI, AgentCount: in.AgentCount,
		Phase: model.HarnessDiscuss, Status: model.HarnessRunning, Attempt: 1, MaxAttempts: in.MaxAttempts,
		CreatedAt: now, UpdatedAt: now}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		if running, err := runningHarness(ctx, tx, team); err != nil {
			return err
		} else if running != nil {
			return conflictf("Team '%s' already has a running harness (%s); abort it first", team, running.ID)
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO harness_runs ("+harnessColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			r.ID, team, r.Goal, r.CLI, r.AgentCount, r.Phase, r.Status, r.Attempt, r.MaxAttempts, r.CreatedAt, r.UpdatedAt)
		return err
	})
	return r, err
}

func runningHarness(ctx context.Context, q queryer, team string) (*model.HarnessRun, error) {
	r, err := scanHarness(q.QueryRowContext(ctx,
		"SELECT "+harnessColumns+" FROM harness_runs WHERE team = ? AND status = 'running'", team))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

// resolveHarness finds a run by id, or with an empty id the running run,
// falling back to the newest one.
func resolveHarness(ctx context.Context, q queryer, team, id string) (model.HarnessRun, error) {
	if err := requireTeam(ctx, q, team); err != nil {
		return model.HarnessRun{}, err
	}
	var row *sql.Row
	if id != "" {
		row = q.QueryRowContext(ctx, "SELECT "+harnessColumns+" FROM harness_runs WHERE team = ? AND id = ?", team, id)
	} else {
		row = q.QueryRowContext(ctx, "SELECT "+harnessColumns+` FROM harness_runs WHERE team = ?
			ORDER BY status = 'running' DESC, created_at DESC, rowid DESC LIMIT 1`, team)
	}
	r, err := scanHarness(row)
	if errors.Is(err, sql.ErrNoRows) {
		if id != "" {
			return r, notFoundf("Harness run '%s' not found in team '%s'", id, team)
		}
		return r, notFoundf("Team '%s' has no harness run; start one with team-worker harness start", team)
	}
	return r, err
}

// ListHarness returns a team's runs, newest first.
func (s *Store) ListHarness(ctx context.Context, team string) ([]model.HarnessRun, error) {
	if err := requireTeam(ctx, s.db, team); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+harnessColumns+" FROM harness_runs WHERE team = ? ORDER BY created_at DESC, rowid DESC", team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []model.HarnessRun{}
	for rows.Next() {
		r, err := scanHarness(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// HarnessStatus returns a run with its current gate state.
func (s *Store) HarnessStatus(ctx context.Context, team, id string) (model.HarnessStatus, error) {
	r, err := resolveHarness(ctx, s.db, team, id)
	if err != nil {
		return model.HarnessStatus{}, err
	}
	return harnessStatus(ctx, s.db, r)
}

func harnessStatus(ctx context.Context, q queryer, r model.HarnessRun) (model.HarnessStatus, error) {
	st := model.HarnessStatus{HarnessRun: r, Artifacts: []string{}, History: []model.HarnessEvent{}, Agents: []model.HarnessAgent{}}
	arts, err := listArtifacts(ctx, q, r.Team, r.ID)
	if err != nil {
		return st, err
	}
	for _, a := range arts {
		st.Artifacts = append(st.Artifacts, a.Name)
	}
	rows, err := q.QueryContext(ctx, "SELECT phase, action, note, at FROM harness_history WHERE team = ? AND run_id = ? ORDER BY seq", r.Team, r.ID)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var e model.HarnessEvent
		if err := rows.Scan(&e.Phase, &e.Action, &e.Note, &e.At); err != nil {
			rows.Close()
			return st, err
		}
		st.History = append(st.History, e)
	}
	rows.Close()
	if st.Agents, err = harnessAgents(ctx, q, r.Team, r.ID); err != nil {
		return st, err
	}
	g, err := checkGate(ctx, q, r)
	if err != nil {
		return st, err
	}
	st.CanAdvance, st.GateReason = g.pass, g.reason
	return st, nil
}

// gate is the result of checking whether a run may leave its phase. A
// failed evaluation passes the gate but sends the run back to execute.
type gate struct {
	pass     bool
	reason   string
	rollback bool
	feedback string
}

func checkGate(ctx context.Context, q queryer, r model.HarnessRun) (gate, error) {
	if r.Status != model.HarnessRunning {
		return gate{reason: "The run is " + r.Status}, nil
	}
	missing := func(name string) (bool, error) {
		var ok bool
		err := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM harness_artifacts WHERE team = ? AND run_id = ? AND name = ?)",
			r.Team, r.ID, name).Scan(&ok)
		return !ok, err
	}
	switch r.Phase {
	case model.HarnessDiscuss:
		if m, err := missing(model.ArtifactDiscussion); err != nil || m {
			return gate{reason: "Register " + model.ArtifactDiscussion + " with team-worker harness artifact"}, err
		}
	case model.HarnessPlan:
		if m, err := missing(model.ArtifactSpec); err != nil || m {
			return gate{reason: "Register " + model.ArtifactSpec + " with team-worker harness artifact"}, err
		}
		var approved bool
		err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM plans p JOIN harness_agents a
			ON a.team = p.team AND a.name = p.agent AND a.run_id = ? AND a.role = ?
			WHERE p.team = ? AND p.status = 'approved' AND p.created_at >= ?)`,
			r.ID, model.RolePlanner, r.Team, r.CreatedAt).Scan(&approved)
		if err != nil || !approved {
			return gate{reason: "Waiting for the leader to approve the planner's plan (team-worker harness approve " + r.Team + ")"}, err
		}
	case model.HarnessExecute:
		var total, done int
		err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(status = 'completed'), 0) FROM tasks
			WHERE team = ? AND json_extract(metadata, '$.harnessRun') = ? AND json_extract(metadata, '$.harnessAttempt') = ?`,
			r.Team, r.ID, r.Attempt).Scan(&total, &done)
		if err != nil {
			return gate{}, err
		}
		if total == 0 {
			return gate{reason: "No tasks for this attempt yet; create them with team-worker harness task"}, nil
		}
		if done < total {
			return gate{reason: fmt.Sprintf("%d of %d run tasks completed", done, total)}, nil
		}
	case model.HarnessVerify:
		var content string
		err := q.QueryRowContext(ctx, "SELECT content FROM harness_artifacts WHERE team = ? AND run_id = ? AND name = ?",
			r.Team, r.ID, model.ArtifactEvalReport).Scan(&content)
		if errors.Is(err, sql.ErrNoRows) {
			return gate{reason: "Register " + model.ArtifactEvalReport + ` with {"verdict":"pass"|"fail"}`}, nil
		}
		if err != nil {
			return gate{}, err
		}
		var report struct {
			Verdict  string `json:"verdict"`
			Summary  string `json:"summary"`
			Feedback string `json:"feedback"`
		}
		if json.Unmarshal([]byte(content), &report) != nil {
			return gate{reason: model.ArtifactEvalReport + " is not valid JSON"}, nil
		}
		switch strings.ToLower(report.Verdict) {
		case "pass":
		case "fail":
			feedback := report.Feedback
			if feedback == "" {
				feedback = report.Summary
			}
			reason := "The evaluation failed; advancing returns the run to execute"
			if r.Attempt >= r.MaxAttempts {
				reason = "The evaluation failed and no attempts are left; advancing fails the run"
			}
			return gate{pass: true, reason: reason, rollback: true, feedback: feedback}, nil
		default:
			return gate{reason: model.ArtifactEvalReport + ` needs "verdict": "pass" or "fail"`}, nil
		}
	case model.HarnessShip:
		return gate{reason: "The run is at its last phase"}, nil
	}
	return gate{pass: true}, nil
}

// AdvanceHarness moves a run to its next phase when the gate passes. A
// failed evaluation sends it back to execute with a rework task and tells
// the executors, or fails the run when no attempts are left.
func (s *Store) AdvanceHarness(ctx context.Context, team, id string) (model.HarnessAdvance, error) {
	var out model.HarnessAdvance
	err := s.write(ctx, func(tx *sql.Tx) error {
		r, err := resolveHarness(ctx, tx, team, id)
		if err != nil {
			return err
		}
		g, err := checkGate(ctx, tx, r)
		if err != nil {
			return err
		}
		if !g.pass {
			return conflictf("Cannot advance: %s", g.reason)
		}
		now := s.timestamp()
		out = model.HarnessAdvance{ID: r.ID, From: r.Phase, Attempt: r.Attempt}
		if g.rollback {
			out.RolledBack = true
			return s.rollbackHarness(ctx, tx, &r, g.feedback, now, &out)
		}
		next := model.HarnessPhases[slices.Index(model.HarnessPhases, r.Phase)+1]
		status := model.HarnessRunning
		if next == model.HarnessShip {
			status = model.HarnessCompleted
		}
		if err := addHarnessEvent(ctx, tx, r, r.Phase, "completed", "", now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE harness_runs SET current_phase = ?, status = ?, updated_at = ? WHERE team = ? AND id = ?",
			next, status, now, team, r.ID); err != nil {
			return err
		}
		out.Phase, out.Status = next, status
		return nil
	})
	return out, err
}

func (s *Store) rollbackHarness(ctx context.Context, tx *sql.Tx, r *model.HarnessRun, feedback, now string, out *model.HarnessAdvance) error {
	if r.Attempt >= r.MaxAttempts {
		if err := addHarnessEvent(ctx, tx, *r, r.Phase, "failed", feedback, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE harness_runs SET status = 'failed', updated_at = ? WHERE team = ? AND id = ?", now, r.Team, r.ID)
		out.Phase, out.Status = r.Phase, model.HarnessFailed
		return err
	}
	attempt := r.Attempt + 1
	if err := addHarnessEvent(ctx, tx, *r, r.Phase, "rolled_back", feedback, now); err != nil {
		return err
	}
	// The evaluation report belongs to the failed attempt; the next one needs its own.
	if _, err := tx.ExecContext(ctx, "DELETE FROM harness_artifacts WHERE team = ? AND run_id = ? AND name = ?",
		r.Team, r.ID, model.ArtifactEvalReport); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE harness_runs SET current_phase = ?, attempt = ?, updated_at = ? WHERE team = ? AND id = ?",
		model.HarnessExecute, attempt, now, r.Team, r.ID); err != nil {
		return err
	}
	// A rework task gives executors something to pick up and keeps the execute gate closed until it is done.
	desc := "The evaluation of the previous attempt failed. Fix the work against spec.md."
	if feedback != "" {
		desc += "\n\nEvaluator feedback:\n" + feedback
	}
	task := model.Task{ID: randomHex(8), Subject: fmt.Sprintf("Address evaluation feedback (attempt %d)", attempt),
		Description: desc, Status: model.StatusPending, Priority: model.PriorityHigh, CreatedAt: now, UpdatedAt: now,
		Blocks: []string{}, BlockedBy: []string{},
		Metadata: map[string]any{"harnessRun": r.ID, "harnessAttempt": attempt, "harnessPhase": model.HarnessExecute}}
	if err := insertTask(ctx, tx, r.Team, task); err != nil {
		return err
	}
	agents, err := harnessAgents(ctx, tx, r.Team, r.ID)
	if err != nil {
		return err
	}
	content := fmt.Sprintf("Harness %s: the evaluation failed, so the run is back in execute (attempt %d of %d). "+
		"Pick up task %s.", r.ID, attempt, r.MaxAttempts, task.ID)
	if feedback != "" {
		content += "\n\nFeedback:\n" + feedback
	}
	for _, a := range agents {
		if a.Role != model.RoleExecutor {
			continue
		}
		inbox, err := resolveInbox(ctx, tx, r.Team, a.Name, "")
		if err != nil {
			continue // the agent left the team; nobody to tell
		}
		if err := deliver(ctx, tx, r.Team, inbox, model.Message{Type: "message", From: "harness", To: inbox,
			Content: content, Timestamp: now}); err != nil {
			return err
		}
	}
	out.Phase, out.Status, out.Attempt = model.HarnessExecute, model.HarnessRunning, attempt
	return nil
}

func addHarnessEvent(ctx context.Context, tx *sql.Tx, r model.HarnessRun, phase, action, note, at string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO harness_history (team, run_id, phase, action, note, at) VALUES (?, ?, ?, ?, ?, ?)",
		r.Team, r.ID, phase, action, note, at)
	return err
}

// AbortHarness stops a running run and returns its agents so the caller can
// stop their processes.
func (s *Store) AbortHarness(ctx context.Context, team, id, reason string) (model.HarnessStatus, error) {
	var st model.HarnessStatus
	err := s.write(ctx, func(tx *sql.Tx) error {
		r, err := resolveHarness(ctx, tx, team, id)
		if err != nil {
			return err
		}
		if r.Status != model.HarnessRunning {
			return conflictf("Harness run '%s' is already %s", r.ID, r.Status)
		}
		now := s.timestamp()
		if err := addHarnessEvent(ctx, tx, r, r.Phase, "aborted", reason, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE harness_runs SET status = 'aborted', updated_at = ? WHERE team = ? AND id = ?",
			now, team, r.ID); err != nil {
			return err
		}
		r.Status, r.UpdatedAt = model.HarnessAborted, now
		st, err = harnessStatus(ctx, tx, r)
		return err
	})
	return st, err
}

// ApproveHarnessPlan approves the newest pending plan of the run's planners
// through the normal plan approval, so the caller must be the team leader.
func (s *Store) ApproveHarnessPlan(ctx context.Context, team, id, caller, feedback string) (model.Plan, error) {
	r, err := resolveHarness(ctx, s.db, team, id)
	if err != nil {
		return model.Plan{}, err
	}
	if r.Status != model.HarnessRunning {
		return model.Plan{}, conflictf("Harness run '%s' is %s", r.ID, r.Status)
	}
	var planID string
	err = s.db.QueryRowContext(ctx, `SELECT p.id FROM plans p JOIN harness_agents a
		ON a.team = p.team AND a.name = p.agent AND a.run_id = ? AND a.role = ?
		WHERE p.team = ? AND p.status = 'pending' AND p.created_at >= ?
		ORDER BY p.created_at DESC, p.rowid DESC LIMIT 1`, r.ID, model.RolePlanner, team, r.CreatedAt).Scan(&planID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Plan{}, notFoundf("No pending plan from this run's planner; it submits one with team-worker plan submit")
	}
	if err != nil {
		return model.Plan{}, err
	}
	return s.DecidePlan(ctx, team, planID, true, DecidePlanInput{Caller: caller, Feedback: feedback})
}

// PutArtifact registers or replaces a run's artifact.
func (s *Store) PutArtifact(ctx context.Context, team, id, name, content string) (model.HarnessArtifact, error) {
	if !artifactName.MatchString(name) || name == "." || name == ".." {
		return model.HarnessArtifact{}, invalidf("Artifact names use letters, digits, '.', '_' and '-' (at most 128)")
	}
	if len(content) > MaxArtifactLength {
		return model.HarnessArtifact{}, invalidf("Artifact must be at most %d bytes", MaxArtifactLength)
	}
	var a model.HarnessArtifact
	err := s.write(ctx, func(tx *sql.Tx) error {
		r, err := resolveHarness(ctx, tx, team, id)
		if err != nil {
			return err
		}
		now := s.timestamp()
		if _, err := tx.ExecContext(ctx, `INSERT INTO harness_artifacts (team, run_id, name, content, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (team, run_id, name) DO UPDATE SET content = excluded.content, updated_at = excluded.updated_at`,
			team, r.ID, name, content, now, now); err != nil {
			return err
		}
		a, err = getArtifact(ctx, tx, team, r.ID, name)
		return err
	})
	return a, err
}

func (s *Store) GetArtifact(ctx context.Context, team, id, name string) (model.HarnessArtifact, error) {
	r, err := resolveHarness(ctx, s.db, team, id)
	if err != nil {
		return model.HarnessArtifact{}, err
	}
	return getArtifact(ctx, s.db, team, r.ID, name)
}

func getArtifact(ctx context.Context, q queryer, team, runID, name string) (model.HarnessArtifact, error) {
	var a model.HarnessArtifact
	err := q.QueryRowContext(ctx, "SELECT name, content, created_at, updated_at FROM harness_artifacts WHERE team = ? AND run_id = ? AND name = ?",
		team, runID, name).Scan(&a.Name, &a.Content, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, notFoundf("Artifact '%s' not found in harness run '%s'", name, runID)
	}
	a.Size = len(a.Content)
	return a, err
}

// ListArtifacts returns a run's artifacts without their content.
func (s *Store) ListArtifacts(ctx context.Context, team, id string) ([]model.HarnessArtifact, error) {
	r, err := resolveHarness(ctx, s.db, team, id)
	if err != nil {
		return nil, err
	}
	return listArtifacts(ctx, s.db, team, r.ID)
}

func listArtifacts(ctx context.Context, q queryer, team, runID string) ([]model.HarnessArtifact, error) {
	rows, err := q.QueryContext(ctx, "SELECT name, length(CAST(content AS BLOB)), created_at, updated_at FROM harness_artifacts WHERE team = ? AND run_id = ? ORDER BY name",
		team, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	arts := []model.HarnessArtifact{}
	for rows.Next() {
		var a model.HarnessArtifact
		if err := rows.Scan(&a.Name, &a.Size, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		arts = append(arts, a)
	}
	return arts, rows.Err()
}

// CreateHarnessTask creates a task that belongs to the run's current
// attempt, so the execute gate waits for it.
func (s *Store) CreateHarnessTask(ctx context.Context, team, id string, in CreateTaskInput) (model.Task, error) {
	r, err := resolveHarness(ctx, s.db, team, id)
	if err != nil {
		return model.Task{}, err
	}
	if r.Status != model.HarnessRunning {
		return model.Task{}, conflictf("Harness run '%s' is %s", r.ID, r.Status)
	}
	if in.Metadata == nil {
		in.Metadata = map[string]any{}
	}
	in.Metadata["harnessRun"], in.Metadata["harnessAttempt"], in.Metadata["harnessPhase"] = r.ID, r.Attempt, model.HarnessExecute
	return s.CreateTask(ctx, team, in)
}

// AddHarnessAgent records an agent started for a run.
func (s *Store) AddHarnessAgent(ctx context.Context, team, id string, in HarnessAgentInput) (model.HarnessAgent, error) {
	if err := validate(in.Name, "agent name"); err != nil {
		return model.HarnessAgent{}, err
	}
	switch in.Role {
	case model.RolePlanner, model.RoleExecutor, model.RoleEvaluator:
	default:
		return model.HarnessAgent{}, invalidf("Unknown harness role '%s'", in.Role)
	}
	a := model.HarnessAgent{Name: in.Name, Role: in.Role, Phase: in.Phase, CreatedAt: s.timestamp()}
	err := s.write(ctx, func(tx *sql.Tx) error {
		r, err := resolveHarness(ctx, tx, team, id)
		if err != nil {
			return err
		}
		if a.Phase == "" {
			a.Phase = r.Phase
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO harness_agents (team, run_id, name, role, phase, created_at) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (team, run_id, name) DO NOTHING`, team, r.ID, a.Name, a.Role, a.Phase, a.CreatedAt)
		return err
	})
	return a, err
}

func harnessAgents(ctx context.Context, q queryer, team, runID string) ([]model.HarnessAgent, error) {
	rows, err := q.QueryContext(ctx, "SELECT name, role, phase, created_at FROM harness_agents WHERE team = ? AND run_id = ? ORDER BY created_at, name",
		team, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := []model.HarnessAgent{}
	for rows.Next() {
		var a model.HarnessAgent
		if err := rows.Scan(&a.Name, &a.Role, &a.Phase, &a.CreatedAt); err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}
