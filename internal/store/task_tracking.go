package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/benlocal/team-worker/internal/model"
)

func validateTaskTracking(ctx context.Context, q queryer, team string, t model.Task) error {
	if t.Kind != "" && t.Kind != "task" && t.Kind != "goal" {
		return invalidf("Task kind must be task or goal")
	}
	if t.ParentID != "" {
		parent, err := getTask(ctx, q, team, t.ParentID)
		if err != nil {
			return err
		}
		if parent.Kind != "goal" {
			return invalidf("Parent task must be a goal")
		}
		var cycle bool
		err = q.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id) AS (
   SELECT ? UNION SELECT json_extract(t.tracking, '$.parentId') FROM tasks t JOIN ancestors a ON t.id=a.id AND t.team=?
  ) SELECT EXISTS(SELECT 1 FROM ancestors WHERE id=?)`, t.ParentID, team, t.ID).Scan(&cycle)
		if err != nil {
			return err
		}
		if cycle {
			return invalidf("Goal hierarchy cannot contain cycles")
		}
	}
	if t.Status == model.StatusSuperseded {
		if t.SupersededBy == "" || t.SupersededBy == t.ID {
			return invalidf("Superseded tasks require a different replacement task")
		}
		next, err := getTask(ctx, q, team, t.SupersededBy)
		if err != nil {
			return err
		}
		if (next.Kind == "goal") != (t.Kind == "goal") {
			return invalidf("Replacement must have the same task kind")
		}
		if next.Status == model.StatusSuperseded || next.Status == model.StatusCompleted {
			return invalidf("Replacement task must be open")
		}
		if err := checkDeps(ctx, q, team, t.ID, []string{next.ID}); err != nil {
			return err
		}
	} else if t.SupersededBy != "" {
		return invalidf("Replacement requires superseded status")
	}
	if t.Kind != "goal" {
		var children bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE team=? AND json_extract(tracking, '$.parentId')=?)`, team, t.ID).Scan(&children); err != nil {
			return err
		}
		if children {
			return invalidf("A goal with children cannot become an execution task")
		}
	}
	return nil
}

func appendTaskProgress(ctx context.Context, tx *sql.Tx, team string, t *model.Task, p model.TaskProgress, caller, now string) error {
	if t.Status == model.StatusCompleted || t.Status == model.StatusSuperseded {
		return conflictf("Report progress only on an open task")
	}
	if strings.TrimSpace(p.Key) == "" || strings.TrimSpace(p.Summary) == "" {
		return invalidf("Progress key and summary are required")
	}
	if p.State == "" {
		p.State = "active"
	}
	if p.State != "active" && p.State != "waiting" {
		return invalidf("Progress state must be active or waiting")
	}
	if p.State == "waiting" && (strings.TrimSpace(p.Reason) == "" || strings.TrimSpace(p.NextAction) == "") {
		return invalidf("Waiting requires a reason and next action")
	}
	if p.State != "waiting" && (p.CheckpointID != "" || p.RequestID != "") {
		return invalidf("Wait references require waiting state")
	}
	if p.CheckpointID != "" && p.RequestID != "" {
		return invalidf("Link one waiting trigger per progress report")
	}
	p.Artifacts = dedupe(p.Artifacts)
	p.ReportedAt = ""
	p.ReportedBy = ""
	for _, old := range t.Progress {
		old.Artifacts = dedupe(old.Artifacts)
		old.ReportedAt = ""
		old.ReportedBy = ""
		if old.Key == p.Key {
			if reflect.DeepEqual(old, p) {
				return nil
			}
			return conflictf("Progress key already exists with different contents")
		}
	}
	if p.CheckpointID != "" {
		cp, err := getCheckpoint(ctx, tx, team, p.CheckpointID)
		if err != nil {
			return err
		}
		if cp.TaskID != t.ID || cp.Owner != t.Owner || cp.Status == "completed" || cp.Status == "canceled" {
			return invalidf("Waiting checkpoint must be open and belong to this task and owner")
		}
	}
	if p.RequestID != "" {
		r, err := getDecision(ctx, tx, team, p.RequestID)
		if err != nil {
			return err
		}
		if r.Status != "pending" {
			return invalidf("Waiting request must be pending")
		}
	}
	p.ReportedAt = now
	p.ReportedBy = caller
	t.Progress = append(t.Progress, p)
	return nil
}

// transferTaskDependents preserves unfinished acceptance dependencies during handoff.
func transferTaskDependents(ctx context.Context, tx *sql.Tx, team, id, next, now string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET updated_at=? WHERE team=? AND id IN (SELECT task_id FROM task_deps WHERE team=? AND blocked_by=?)`, now, team, team, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE task_deps SET blocked_by=? WHERE team=? AND blocked_by=?`, next, team, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM task_deps WHERE team=? AND blocked_by=?`, team, id)
	return err
}

// enrichTaskTracking batches live references so a finished trigger is never shown as pending.
func (s *Store) enrichTaskTracking(ctx context.Context, team string, tasks []model.Task) error {
	if len(tasks) == 0 {
		return nil
	}
	cps, err := s.ListCheckpoints(ctx, team)
	if err != nil {
		return err
	}
	cpByID := map[string]model.Checkpoint{}
	next := map[string]model.Checkpoint{}
	for _, cp := range cps {
		cp.Token = ""
		cpByID[cp.ID] = cp
		if cp.Status == "completed" || cp.Status == "canceled" {
			continue
		}
		old, ok := next[cp.TaskID]
		if !ok || cp.DueAt.Before(old.DueAt) {
			next[cp.TaskID] = cp
		}
	}
	requests := map[string]model.DecisionRequest{}
	ids := []string{}
	for _, t := range tasks {
		if len(t.Progress) > 0 {
			p := t.Progress[len(t.Progress)-1]
			if p.RequestID != "" {
				ids = append(ids, p.RequestID)
			}
		}
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	// Read request references in one query instead of one lookup per task.
	rows, err := s.db.QueryContext(ctx, "SELECT "+decisionColumns+" FROM decision_requests WHERE team=? AND id IN (SELECT value FROM json_each(?))", team, string(encoded))
	if err != nil {
		return err
	}
	for rows.Next() {
		r, err := scanDecision(rows)
		if err != nil {
			rows.Close()
			return err
		}
		requests[r.ID] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range tasks {
		t := &tasks[i]
		if cp, ok := next[t.ID]; ok {
			t.NextCheckpoint = &cp
		}
		if len(t.Progress) == 0 {
			continue
		}
		p := t.Progress[len(t.Progress)-1]
		t.ExecutionState = p.State
		if p.State == "waiting" {
			t.ExecutionState = "unregistered"
			if cp, ok := cpByID[p.CheckpointID]; ok {
				t.ExecutionState = "waiting"
				if cp.Status == "completed" || cp.Status == "canceled" {
					t.ExecutionState = "follow_up"
				}
			}
			if r, ok := requests[p.RequestID]; ok {
				t.ExecutionState = "waiting"
				if r.Status != "pending" {
					t.ExecutionState = "follow_up"
				}
			}
		}
		if cp, ok := cpByID[p.CheckpointID]; ok {
			t.WaitCheckpoint = &cp
		}
		if r, ok := requests[p.RequestID]; ok {
			t.WaitingRequest = &r
		}
	}
	return nil
}

// supersessionRetry recognizes the same handoff after a lost response.
func supersessionRetry(t model.Task, in UpdateTaskInput) bool {
	if in.Status == nil || *in.Status != model.StatusSuperseded || in.SupersededBy == nil || *in.SupersededBy != t.SupersededBy || in.Result == nil || t.Result == nil {
		return false
	}
	if in.Owner != nil || in.Subject != nil || in.Description != nil || in.Priority != nil || in.Kind != nil || in.ParentID != nil || in.Progress != nil || len(in.Metadata) > 0 || len(in.AddBlocks)+len(in.AddBlockedBy)+len(in.RemoveBlockedBy) > 0 {
		return false
	}
	return in.Result.Summary == t.Result.Summary && in.Result.Verification == t.Result.Verification && reflect.DeepEqual(dedupe(in.Result.Artifacts), dedupe(t.Result.Artifacts))
}

func progressOnlyUpdate(in UpdateTaskInput) bool {
	in.Progress = nil
	in.Caller = ""
	in.Force = false
	return reflect.DeepEqual(in, UpdateTaskInput{})
}
