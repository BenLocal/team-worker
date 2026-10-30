package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/benlocal/team-worker/internal/model"
)

const agentColumns = `team, name, agent_id, backend, target, pid, command, cwd, status, started_at, exited_at, nudge, run_id, stop_requested, exit_code, host, activity, activity_at`

func scanAgent(row scanner) (model.Agent, error) {
	var a model.Agent
	var command string
	err := row.Scan(&a.Team, &a.Name, &a.AgentID, &a.Backend, &a.Target, &a.PID, &command, &a.Cwd,
		&a.Status, &a.StartedAt, &a.ExitedAt, &a.Nudge, &a.RunID, &a.StopRequested, &a.ExitCode, &a.Host, &a.Activity, &a.ActivityAt)
	if err != nil {
		return a, err
	}
	if err := json.Unmarshal([]byte(command), &a.Command); err != nil {
		return a, err
	}
	if a.Command == nil {
		a.Command = []string{}
	}
	return a, nil
}

// RegisterAgent records a freshly spawned agent as running, replacing any
// earlier record with the same name.
func (s *Store) RegisterAgent(ctx context.Context, a model.Agent) (model.Agent, error) {
	if err := validate(a.Name, "agent name"); err != nil {
		return a, err
	}
	if a.Command == nil {
		a.Command = []string{}
	}
	a.Status = model.AgentRunning
	a.ExitCode = 0
	a.StartedAt = s.timestamp()
	a.ExitedAt = ""
	// A new run has reported nothing yet; re-registering a run keeps its state.
	a.Activity, a.ActivityAt = "", ""
	command, err := json.Marshal(a.Command)
	if err != nil {
		return a, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, a.Team); err != nil {
			return err
		}
		previous, err := getAgent(ctx, tx, a.Team, a.Name)
		if err != nil && KindOf(err) != KindNotFound {
			return err
		}
		if a.PreviousRunID != "" && (err != nil || previous.RunID != a.PreviousRunID || previous.StopRequested || previous.Status != model.AgentExited) {
			return conflictf("Agent restart was cancelled or replaced")
		}
		if err == nil && a.RunID != "" {
			if previous.RunID == a.RunID {
				a.Status, a.ExitedAt, a.StopRequested, a.ExitCode = previous.Status, previous.ExitedAt, previous.StopRequested, previous.ExitCode
				a.StartedAt = previous.StartedAt
				a.Activity, a.ActivityAt = previous.Activity, previous.ActivityAt
			} else if previous.RunID != "" && previous.Status == model.AgentRunning {
				return conflictf("Agent already has a running instance")
			}
		}
		_, err = tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO agents (`+agentColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.Team, a.Name, a.AgentID, a.Backend, a.Target, a.PID, string(command), a.Cwd,
			a.Status, a.StartedAt, a.ExitedAt, a.Nudge, a.RunID, a.StopRequested, a.ExitCode, a.Host, a.Activity, a.ActivityAt)
		return err
	})
	return a, err
}

func (s *Store) GetAgent(ctx context.Context, team, name string) (model.Agent, error) {
	return getAgent(ctx, s.db, team, name)
}

func getAgent(ctx context.Context, q queryer, team, name string) (model.Agent, error) {
	a, err := scanAgent(q.QueryRowContext(ctx,
		"SELECT "+agentColumns+" FROM agents WHERE team = ? AND name = ?", team, name))
	if errors.Is(err, sql.ErrNoRows) {
		return a, notFoundf("Agent '%s' not found in team '%s'", name, team)
	}
	return a, err
}

func (s *Store) ListAgents(ctx context.Context, team string) ([]model.Agent, error) {
	if err := requireTeam(ctx, s.db, team); err != nil {
		return nil, err
	}
	return s.queryAgents(ctx, "SELECT "+agentColumns+" FROM agents WHERE team = ? ORDER BY started_at", team)
}

// RunningAgents lists agents in every team that are recorded as running.
func (s *Store) RunningAgents(ctx context.Context) ([]model.Agent, error) {
	return s.queryAgents(ctx, "SELECT "+agentColumns+" FROM agents WHERE status = ? ORDER BY team, name",
		model.AgentRunning)
}

func (s *Store) queryAgents(ctx context.Context, query string, args ...any) ([]model.Agent, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := []model.Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// InboxStats returns how many messages wait in an agent's inbox and the id
// of the newest one, which grows whenever a new message arrives.
func (s *Store) InboxStats(ctx context.Context, team, agent string) (count int, newest int64, err error) {
	inbox, err := resolveInbox(ctx, s.db, team, agent, "")
	if err != nil {
		return 0, 0, err
	}
	err = s.db.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(MAX(id), 0) FROM messages WHERE team = ? AND inbox = ?", team, inbox,
	).Scan(&count, &newest)
	return count, newest, err
}

// lockHolderDead reports whether a task lock's holder is known to be gone:
// its agent record says exited, or the probe finds the process dead.
// Holders without a record are treated as alive.
func (s *Store) lockHolderDead(ctx context.Context, q queryer, team, name string) (bool, error) {
	a, err := getAgent(ctx, q, team, name)
	if KindOf(err) == KindNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a.Status == model.AgentExited {
		return true, nil
	}
	alive, known := s.Probe(a)
	return known && !alive, nil
}

// AgentExited marks an agent as exited, returns its in-progress tasks to
// pending and tells the leader about them. Repeated calls for an agent that
// is already marked exited do nothing, so the exit hook and the daemon's
// reaper can both report the same exit.
// SetAgentActivity records what a running agent's hooks report. A report
// from another run than the current one, or for an agent that is not
// running, is ignored: it comes from a process that has been replaced.
func (s *Store) SetAgentActivity(ctx context.Context, team, name, runID string, activity model.AgentActivity) (model.Agent, error) {
	if !activity.Valid() {
		return model.Agent{}, invalidf("Unknown agent activity '%s'", activity)
	}
	// Most reports repeat the current state; skipping those keeps change
	// notifications, which wake every waiting Stop hook, for real changes.
	a, err := getAgent(ctx, s.db, team, name)
	if err != nil || a.Activity == activity {
		return a, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		var err error
		if a, err = getAgent(ctx, tx, team, name); err != nil {
			return err
		}
		if a.Status != model.AgentRunning || (runID != "" && a.RunID != runID) || a.Activity == activity {
			return nil
		}
		a.Activity, a.ActivityAt = activity, s.timestamp()
		_, err = tx.ExecContext(ctx, "UPDATE agents SET activity = ?, activity_at = ? WHERE team = ? AND name = ?",
			a.Activity, a.ActivityAt, team, name)
		return err
	})
	return a, err
}

func (s *Store) AgentExited(ctx context.Context, team, name string) (model.AgentExit, error) {
	return s.AgentExitedRun(ctx, team, name, "", 0)
}

func (s *Store) AgentExitedRun(ctx context.Context, team, name, runID string, exitCode int) (model.AgentExit, error) {
	res := model.AgentExit{Agent: name, Released: []model.Task{}}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		a, err := getAgent(ctx, tx, team, name)
		switch {
		case err == nil && (a.Status == model.AgentExited || a.RunID != runID):
			return nil
		case err == nil:
			if _, err := tx.ExecContext(ctx,
				"UPDATE agents SET status = ?, exited_at = ?, exit_code = ? WHERE team = ? AND name = ?",
				model.AgentExited, s.timestamp(), exitCode, team, name); err != nil {
				return err
			}
		case KindOf(err) != KindNotFound:
			return err
		}
		res.Changed = true

		released, err := releaseTasks(ctx, tx, team, name, s.timestamp())
		if err != nil || len(released) == 0 {
			return err
		}
		res.Released = released
		subjects := make([]string, len(released))
		for i, t := range released {
			subjects[i] = t.Subject
		}
		leader, err := leaderInbox(ctx, tx, team)
		if err != nil || leader == "" {
			return err
		}
		return deliver(ctx, tx, team, leader, model.Message{
			Type: "message",
			From: name,
			To:   leader,
			Content: fmt.Sprintf("Agent '%s' exited unexpectedly. Reset %d task(s) to pending: %s",
				name, len(released), strings.Join(subjects, ", ")),
			RequestID: randomHex(12),
			Timestamp: s.timestamp(),
		})
	})
	return res, err
}

// releaseTasks returns an agent's in-progress tasks to pending and unlocks them.
func releaseTasks(ctx context.Context, tx *sql.Tx, team, agent, now string) ([]model.Task, error) {
	rows, err := tx.QueryContext(ctx,
		"SELECT "+taskColumns+` FROM tasks WHERE team = ? AND status = ? AND (owner = ? OR locked_by = ?)
		 ORDER BY rowid`,
		team, model.StatusInProgress, agent, agent)
	if err != nil {
		return nil, err
	}
	var released []model.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		released = append(released, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range released {
		released[i].Status = model.StatusPending
		released[i].LockedBy, released[i].LockedAt = "", ""
		released[i].UpdatedAt = now
		if err := saveTask(ctx, tx, team, released[i]); err != nil {
			return nil, err
		}
	}
	return released, nil
}

func leaderInbox(ctx context.Context, q queryer, team string) (string, error) {
	t, err := getTeam(ctx, q, team)
	if err != nil {
		return "", err
	}
	for _, m := range t.Members {
		if m.AgentID == t.LeadAgentID {
			return m.InboxName(), nil
		}
	}
	if len(t.Members) > 0 {
		return t.Members[0].InboxName(), nil
	}
	return "", nil
}

// Board gathers everything the team board shows in one read.
func (s *Store) Board(ctx context.Context, team string, messageLimit int) (model.Board, error) {
	var b model.Board
	var err error
	if b.Team, err = s.GetTeam(ctx, team); err != nil {
		return b, err
	}
	if b.Tasks, err = s.ListTasks(ctx, team, TaskFilter{}); err != nil {
		return b, err
	}
	if b.Stats, err = s.TaskStats(ctx, team); err != nil {
		return b, err
	}
	if b.Messages, err = s.MessageLog(ctx, team, messageLimit, ""); err != nil {
		return b, err
	}
	agents, err := s.ListAgents(ctx, team)
	if err != nil {
		return b, err
	}
	byName := map[string]model.Agent{}
	for _, a := range agents {
		byName[a.Name] = a
	}
	counts := map[string]int{}
	rows, err := s.db.QueryContext(ctx,
		"SELECT inbox, COUNT(*) FROM messages WHERE team = ? GROUP BY inbox", team)
	if err != nil {
		return b, err
	}
	for rows.Next() {
		var inbox string
		var n int
		if err := rows.Scan(&inbox, &n); err != nil {
			rows.Close()
			return b, err
		}
		counts[inbox] = n
	}
	rows.Close()
	b.Members = make([]model.BoardMember, len(b.Team.Members))
	for i, m := range b.Team.Members {
		b.Members[i] = model.BoardMember{Member: m, InboxCount: counts[m.InboxName()]}
		if a, ok := byName[m.Name]; ok {
			b.Members[i].Agent = &a
		}
	}
	return b, rows.Err()
}
