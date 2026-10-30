package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

type CreateTaskInput struct {
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	Owner       string         `json:"owner"`
	Priority    model.Priority `json:"priority"`
	Blocks      []string       `json:"blocks"`
	BlockedBy   []string       `json:"blockedBy"`
	Metadata    map[string]any `json:"metadata"`
}

func (s *Store) CreateTask(ctx context.Context, team string, in CreateTaskInput) (model.Task, error) {
	if in.Subject == "" {
		return model.Task{}, invalidf("Task subject must not be empty")
	}
	if in.Priority == "" {
		in.Priority = model.PriorityMedium
	}
	if _, err := model.ParsePriority(string(in.Priority)); err != nil {
		return model.Task{}, invalidf("%s", err)
	}
	now := s.timestamp()
	t := model.Task{
		ID:          randomHex(8),
		Subject:     in.Subject,
		Description: in.Description,
		Status:      model.StatusPending,
		Priority:    in.Priority,
		Owner:       in.Owner,
		Blocks:      dedupe(in.Blocks),
		BlockedBy:   dedupe(in.BlockedBy),
		CreatedAt:   now,
		UpdatedAt:   now,
		Metadata:    in.Metadata,
	}
	if t.Metadata == nil {
		t.Metadata = map[string]any{}
	}
	if len(t.BlockedBy) > 0 {
		t.Status = model.StatusBlocked
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		if err := checkDeps(ctx, tx, team, t.ID, t.BlockedBy); err != nil {
			return err
		}
		if err := insertTask(ctx, tx, team, t); err != nil {
			return err
		}
		return addDeps(ctx, tx, team, t.ID, t.BlockedBy)
	})
	return t, err
}

func (s *Store) GetTask(ctx context.Context, team, id string) (model.Task, error) {
	if err := requireTeam(ctx, s.db, team); err != nil {
		return model.Task{}, err
	}
	return getTask(ctx, s.db, team, id)
}

type UpdateTaskInput struct {
	Status       *model.TaskStatus `json:"status,omitempty"`
	Owner        *string           `json:"owner,omitempty"`
	Subject      *string           `json:"subject,omitempty"`
	Description  *string           `json:"description,omitempty"`
	Priority     *model.Priority   `json:"priority,omitempty"`
	AddBlocks    []string          `json:"addBlocks,omitempty"`
	AddBlockedBy []string          `json:"addBlockedBy,omitempty"`
	Metadata     map[string]any    `json:"metadata,omitempty"`
	// Caller is the agent making the change; it takes the lock when starting a task.
	Caller string `json:"caller,omitempty"`
	Force  bool   `json:"force,omitempty"`
}

// UpdateTask applies a partial update. Completing a task unblocks its
// dependents in the same transaction.
func (s *Store) UpdateTask(ctx context.Context, team, id string, in UpdateTaskInput) (model.Task, error) {
	if in.Status != nil {
		if _, err := model.ParseTaskStatus(string(*in.Status)); err != nil {
			return model.Task{}, invalidf("%s", err)
		}
	}
	if in.Priority != nil {
		if _, err := model.ParsePriority(string(*in.Priority)); err != nil {
			return model.Task{}, invalidf("%s", err)
		}
	}
	var t model.Task
	err := s.write(ctx, func(tx *sql.Tx) error {
		if err := requireTeam(ctx, tx, team); err != nil {
			return err
		}
		var err error
		t, err = getTask(ctx, tx, team, id)
		if err != nil {
			return err
		}
		now := s.timestamp()

		if in.Status != nil && (*in.Status == model.StatusInProgress || *in.Status == model.StatusCompleted) {
			if err := requireApprovedPlan(ctx, tx, team, in.Caller); err != nil {
				return err
			}
		}
		if in.Status != nil {
			switch *in.Status {
			case model.StatusInProgress:
				if t.Status != model.StatusInProgress {
					if err := budgetExhausted(ctx, tx, team); err != nil {
						return err
					}
				}
				if t.LockedBy != "" && t.LockedBy != in.Caller && !in.Force {
					dead, err := s.lockHolderDead(ctx, tx, team, t.LockedBy)
					if err != nil {
						return err
					}
					if !dead {
						return conflictf("Task '%s' is locked by '%s' (since %s). Use --force to override.",
							t.ID, t.LockedBy, t.LockedAt)
					}
				}
				t.LockedBy = in.Caller
				t.LockedAt = ""
				if in.Caller != "" {
					t.LockedAt = now
				}
				if t.StartedAt == "" {
					t.StartedAt = now
				}
				if in.Owner == nil && in.Caller != "" && t.Owner == "" {
					t.Owner = in.Caller
				}
			case model.StatusCompleted, model.StatusPending:
				t.LockedBy, t.LockedAt = "", ""
			}
			if *in.Status == model.StatusCompleted && t.StartedAt != "" {
				if start, err := time.Parse(time.RFC3339Nano, t.StartedAt); err == nil {
					secs := s.Now().Sub(start).Seconds()
					t.Metadata["duration_seconds"] = math.Round(secs*100) / 100
				}
			}
			t.Status = *in.Status
		}
		if in.Owner != nil {
			t.Owner = *in.Owner
		}
		if in.Subject != nil {
			t.Subject = *in.Subject
		}
		if in.Description != nil {
			t.Description = *in.Description
		}
		if in.Priority != nil {
			t.Priority = *in.Priority
		}
		for _, b := range in.AddBlocks {
			if b != "" && !slices.Contains(t.Blocks, b) {
				t.Blocks = append(t.Blocks, b)
			}
		}
		var newDeps []string
		for _, b := range dedupe(in.AddBlockedBy) {
			if !slices.Contains(t.BlockedBy, b) {
				newDeps = append(newDeps, b)
			}
		}
		if len(newDeps) > 0 {
			if err := checkDeps(ctx, tx, team, t.ID, newDeps); err != nil {
				return err
			}
			if err := addDeps(ctx, tx, team, t.ID, newDeps); err != nil {
				return err
			}
			t.BlockedBy = append(t.BlockedBy, newDeps...)
			if t.Status == model.StatusPending {
				t.Status = model.StatusBlocked
			}
		}
		for k, v := range in.Metadata {
			t.Metadata[k] = v
		}
		t.UpdatedAt = now

		if err := saveTask(ctx, tx, team, t); err != nil {
			return err
		}
		if t.Status == model.StatusCompleted {
			return resolveDependents(ctx, tx, team, t.ID, now)
		}
		return nil
	})
	return t, err
}

type TaskFilter struct {
	Status       model.TaskStatus
	Owner        string
	Priority     model.Priority
	SortPriority bool
}

// ListTasks returns tasks in creation order, or urgent-first with SortPriority.
func (s *Store) ListTasks(ctx context.Context, team string, f TaskFilter) ([]model.Task, error) {
	if err := requireTeam(ctx, s.db, team); err != nil {
		return nil, err
	}
	query := "SELECT " + taskColumns + " FROM tasks WHERE team = ?"
	args := []any{team}
	if f.Status != "" {
		query += " AND status = ?"
		args = append(args, f.Status)
	}
	if f.Owner != "" {
		query += " AND owner = ?"
		args = append(args, f.Owner)
	}
	if f.Priority != "" {
		query += " AND priority = ?"
		args = append(args, f.Priority)
	}
	query += " ORDER BY rowid"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	tasks := []model.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		tasks = append(tasks, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	deps, err := teamDeps(ctx, s.db, team)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		if d := deps[tasks[i].ID]; d != nil {
			tasks[i].BlockedBy = d
		}
	}
	if f.SortPriority {
		sort.SliceStable(tasks, func(i, j int) bool {
			ri, rj := model.PriorityRank(tasks[i].Priority), model.PriorityRank(tasks[j].Priority)
			if ri != rj {
				return ri < rj
			}
			return tasks[i].CreatedAt < tasks[j].CreatedAt
		})
	}
	return tasks, nil
}

func (s *Store) TaskStats(ctx context.Context, team string) (model.TaskStats, error) {
	tasks, err := s.ListTasks(ctx, team, TaskFilter{})
	if err != nil {
		return model.TaskStats{}, err
	}
	st := model.TaskStats{Total: len(tasks)}
	var sum float64
	for _, t := range tasks {
		switch t.Status {
		case model.StatusCompleted:
			st.Completed++
			if d, ok := t.Metadata["duration_seconds"].(float64); ok {
				st.TimedCompleted++
				sum += d
			}
		case model.StatusInProgress:
			st.InProgress++
		case model.StatusPending:
			st.Pending++
		case model.StatusBlocked:
			st.Blocked++
		}
	}
	if st.TimedCompleted > 0 {
		st.AvgDurationSeconds = math.Round(sum/float64(st.TimedCompleted)*100) / 100
	}
	return st, nil
}

const taskColumns = `id, subject, description, status, priority, owner, locked_by, locked_at,
	blocks, started_at, created_at, updated_at, metadata`

type scanner interface{ Scan(dest ...any) error }

func scanTask(row scanner) (model.Task, error) {
	var t model.Task
	var blocks, metadata string
	if err := row.Scan(&t.ID, &t.Subject, &t.Description, &t.Status, &t.Priority, &t.Owner,
		&t.LockedBy, &t.LockedAt, &blocks, &t.StartedAt, &t.CreatedAt, &t.UpdatedAt, &metadata); err != nil {
		return t, err
	}
	if err := json.Unmarshal([]byte(blocks), &t.Blocks); err != nil {
		return t, err
	}
	if err := json.Unmarshal([]byte(metadata), &t.Metadata); err != nil {
		return t, err
	}
	if t.Blocks == nil {
		t.Blocks = []string{}
	}
	if t.Metadata == nil {
		t.Metadata = map[string]any{}
	}
	t.BlockedBy = []string{}
	return t, nil
}

func getTask(ctx context.Context, q queryer, team, id string) (model.Task, error) {
	t, err := scanTask(q.QueryRowContext(ctx,
		"SELECT "+taskColumns+" FROM tasks WHERE team = ? AND id = ?", team, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, notFoundf("Task '%s' not found", id)
	}
	if err != nil {
		return t, err
	}
	rows, err := q.QueryContext(ctx,
		"SELECT blocked_by FROM task_deps WHERE team = ? AND task_id = ? ORDER BY seq", team, id)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	for rows.Next() {
		var dep string
		if err := rows.Scan(&dep); err != nil {
			return t, err
		}
		t.BlockedBy = append(t.BlockedBy, dep)
	}
	return t, rows.Err()
}

func teamDeps(ctx context.Context, q queryer, team string) (map[string][]string, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT task_id, blocked_by FROM task_deps WHERE team = ? ORDER BY seq", team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deps := map[string][]string{}
	for rows.Next() {
		var id, dep string
		if err := rows.Scan(&id, &dep); err != nil {
			return nil, err
		}
		deps[id] = append(deps[id], dep)
	}
	return deps, rows.Err()
}

func insertTask(ctx context.Context, q queryer, team string, t model.Task) error {
	blocks, metadata, err := encodeTaskJSON(t)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`INSERT INTO tasks (team, id, subject, description, status, priority, owner, locked_by,
		 locked_at, blocks, started_at, created_at, updated_at, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		team, t.ID, t.Subject, t.Description, t.Status, t.Priority, t.Owner, t.LockedBy,
		t.LockedAt, blocks, t.StartedAt, t.CreatedAt, t.UpdatedAt, metadata)
	return err
}

func saveTask(ctx context.Context, q queryer, team string, t model.Task) error {
	blocks, metadata, err := encodeTaskJSON(t)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx,
		`UPDATE tasks SET subject = ?, description = ?, status = ?, priority = ?, owner = ?,
		 locked_by = ?, locked_at = ?, blocks = ?, started_at = ?, updated_at = ?, metadata = ?
		 WHERE team = ? AND id = ?`,
		t.Subject, t.Description, t.Status, t.Priority, t.Owner, t.LockedBy, t.LockedAt,
		blocks, t.StartedAt, t.UpdatedAt, metadata, team, t.ID)
	return err
}

func encodeTaskJSON(t model.Task) (string, string, error) {
	blocks, err := json.Marshal(t.Blocks)
	if err != nil {
		return "", "", err
	}
	metadata, err := json.Marshal(t.Metadata)
	if err != nil {
		return "", "", err
	}
	return string(blocks), string(metadata), nil
}

// checkDeps rejects self-dependencies and any new edge that would close a
// cycle, i.e. when the task is reachable from one of its proposed blockers.
func checkDeps(ctx context.Context, q queryer, team, id string, deps []string) error {
	if len(deps) == 0 {
		return nil
	}
	if slices.Contains(deps, id) {
		return invalidf("Task '%s' cannot be blocked by itself", id)
	}
	encoded, err := json.Marshal(deps)
	if err != nil {
		return err
	}
	var one int
	err = q.QueryRowContext(ctx,
		`WITH RECURSIVE reach(id) AS (
			SELECT value FROM json_each(?)
			UNION
			SELECT d.blocked_by FROM task_deps d JOIN reach r ON d.team = ? AND d.task_id = r.id
		) SELECT 1 FROM reach WHERE id = ? LIMIT 1`,
		string(encoded), team, id).Scan(&one)
	if err == nil {
		return invalidf("Task dependencies cannot contain cycles")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func addDeps(ctx context.Context, q queryer, team, id string, deps []string) error {
	for _, dep := range deps {
		if _, err := q.ExecContext(ctx,
			"INSERT OR IGNORE INTO task_deps (team, task_id, blocked_by) VALUES (?, ?, ?)",
			team, id, dep); err != nil {
			return err
		}
	}
	return nil
}

// resolveDependents drops the completed task from every blocker list and
// moves blocked tasks with no remaining blockers back to pending.
func resolveDependents(ctx context.Context, q queryer, team, completedID, now string) error {
	rows, err := q.QueryContext(ctx,
		"SELECT task_id FROM task_deps WHERE team = ? AND blocked_by = ?", team, completedID)
	if err != nil {
		return err
	}
	var dependents []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		dependents = append(dependents, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(dependents) == 0 {
		return err
	}
	if _, err := q.ExecContext(ctx,
		"DELETE FROM task_deps WHERE team = ? AND blocked_by = ?", team, completedID); err != nil {
		return err
	}
	for _, id := range dependents {
		if _, err := q.ExecContext(ctx,
			`UPDATE tasks SET updated_at = ?,
			   status = CASE WHEN status = 'blocked' AND NOT EXISTS (
			     SELECT 1 FROM task_deps d WHERE d.team = tasks.team AND d.task_id = tasks.id
			   ) THEN 'pending' ELSE status END
			 WHERE team = ? AND id = ?`,
			now, team, id); err != nil {
			return err
		}
	}
	return nil
}

func dedupe(items []string) []string {
	out := []string{}
	for _, item := range items {
		if item != "" && !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}
