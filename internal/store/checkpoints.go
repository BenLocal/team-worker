package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

type CreateCheckpointInput struct {
	Key                  string    `json:"key"`
	TaskID               string    `json:"taskId"`
	Caller               string    `json:"caller"`
	DueAt                time.Time `json:"dueAt"`
	AckTimeoutSeconds    int64     `json:"ackTimeoutSeconds"`
	ResultDueAt          time.Time `json:"resultDueAt"`
	EscalateAfterSeconds int64     `json:"escalateAfterSeconds"`
	Action               string    `json:"action"`
	ExpectedEvidence     string    `json:"expectedEvidence"`
}

type CheckpointUpdateInput struct {
	Action   string `json:"action"`
	Caller   string `json:"caller"`
	Token    string `json:"token"`
	Outcome  string `json:"outcome"`
	Summary  string `json:"summary"`
	Evidence string `json:"evidence"`
	Reason   string `json:"reason"`
}

func (s *Store) CreateCheckpoint(ctx context.Context, team string, in CreateCheckpointInput) (model.Checkpoint, error) {
	var cp model.Checkpoint
	if strings.TrimSpace(in.Key) == "" || strings.TrimSpace(in.Action) == "" || strings.TrimSpace(in.ExpectedEvidence) == "" {
		return cp, invalidf("Key, action and expected evidence are required")
	}
	// Bound durations before multiplying to avoid overflow.
	if in.AckTimeoutSeconds < 1 || in.AckTimeoutSeconds > 2592000 || in.EscalateAfterSeconds < 1 || in.EscalateAfterSeconds > 2592000 {
		return cp, invalidf("Acknowledgment and escalation timeouts must be between 1 second and 30 days")
	}
	if in.DueAt.IsZero() || !in.ResultDueAt.After(in.DueAt.Add(time.Duration(in.AckTimeoutSeconds)*time.Second)) {
		return cp, invalidf("Result deadline must follow the acknowledgment deadline")
	}
	in.DueAt, in.ResultDueAt = in.DueAt.UTC(), in.ResultDueAt.UTC()
	err := s.write(ctx, func(tx *sql.Tx) error {
		task, err := getTask(ctx, tx, team, in.TaskID)
		if err != nil {
			return err
		}
		caller, err := checkpointCaller(ctx, tx, team, in.Caller)
		if err != nil {
			return err
		}
		leader, err := leaderInbox(ctx, tx, team)
		if err != nil {
			return err
		}
		// A repeated registration returns its original record even after cancellation.
		existing, err := scanCheckpoint(tx.QueryRowContext(ctx, "SELECT body FROM checkpoints WHERE team=? AND request_key=?", team, in.Key))
		if err == nil {
			if caller != existing.CreatedBy && caller != leader && caller != OperatorInbox {
				return conflictf("Checkpoint belongs to another caller")
			}
			if existing.TaskID != in.TaskID || !existing.DueAt.Equal(in.DueAt) || !existing.ResultDueAt.Equal(in.ResultDueAt) || existing.Action != in.Action || existing.ExpectedEvidence != in.ExpectedEvidence || existing.AckTimeoutSeconds != in.AckTimeoutSeconds || existing.EscalateAfterSeconds != in.EscalateAfterSeconds {
				return conflictf("Checkpoint key already exists with different parameters")
			}
			cp = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if task.Owner == "" || task.Status == model.StatusCompleted {
			return conflictf("Checkpoint requires an owned, unfinished task")
		}
		owner, err := checkpointMemberInbox(ctx, tx, team, task.Owner)
		if err != nil {
			return err
		}
		if caller != owner && caller != leader && caller != OperatorInbox {
			return conflictf("Only the task owner, leader or operator may register a checkpoint")
		}
		cp = model.Checkpoint{ID: randomHex(12), Team: team, Key: in.Key, TaskID: in.TaskID, Owner: owner, CreatedBy: caller, Status: "scheduled", DueAt: in.DueAt, AckTimeoutSeconds: in.AckTimeoutSeconds, ResultDueAt: in.ResultDueAt, EscalateAfterSeconds: in.EscalateAfterSeconds, Action: in.Action, ExpectedEvidence: in.ExpectedEvidence, CreatedAt: s.timestamp()}
		body, err := json.Marshal(cp)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO checkpoints(team,id,request_key,task_id,status,next_at,body) VALUES(?,?,?,?,?,?,?)", team, cp.ID, cp.Key, cp.TaskID, cp.Status, cp.DueAt.UnixMilli(), string(body))
		return err
	})
	return cp, err
}

func checkpointCaller(ctx context.Context, q queryer, team, caller string) (string, error) {
	if caller == "" || caller == OperatorInbox {
		return OperatorInbox, nil
	}
	return checkpointMemberInbox(ctx, q, team, caller)
}

// Checkpoints need a concrete recipient, unlike free-form inbox messages.
func checkpointMemberInbox(ctx context.Context, q queryer, team, name string) (string, error) {
	members, err := listMembers(ctx, q, team)
	if err != nil {
		return "", err
	}
	inbox := ""
	for _, member := range members {
		if member.Name == name || member.InboxName() == name {
			if inbox != "" {
				return "", invalidf("Ambiguous checkpoint member %q; use the full inbox name", name)
			}
			inbox = member.InboxName()
		}
	}
	if inbox == "" {
		return "", invalidf("Checkpoint member %q does not exist", name)
	}
	return inbox, nil
}

func scanCheckpoint(row scanner) (model.Checkpoint, error) {
	var cp model.Checkpoint
	var body string
	if err := row.Scan(&body); err != nil {
		return cp, err
	}
	err := json.Unmarshal([]byte(body), &cp)
	return cp, err
}

func getCheckpoint(ctx context.Context, q queryer, team, id string) (model.Checkpoint, error) {
	cp, err := scanCheckpoint(q.QueryRowContext(ctx, "SELECT body FROM checkpoints WHERE team=? AND id=?", team, id))
	if errors.Is(err, sql.ErrNoRows) {
		err = notFoundf("Checkpoint '%s' not found", id)
	}
	return cp, err
}

func (s *Store) GetCheckpoint(ctx context.Context, team, id string) (model.Checkpoint, error) {
	return getCheckpoint(ctx, s.db, team, id)
}

func (s *Store) ListCheckpoints(ctx context.Context, team string) ([]model.Checkpoint, error) {
	if err := requireTeam(ctx, s.db, team); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT body FROM checkpoints WHERE team=? ORDER BY rowid", team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Checkpoint{}
	for rows.Next() {
		cp, err := scanCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

// checkpointNextAt is shared by the scheduler and late owner reports.
func checkpointNextAt(cp model.Checkpoint) (time.Time, error) {
	switch cp.Status {
	case "scheduled":
		return cp.DueAt, nil
	case "notified", "acknowledged":
		if cp.OperatorAlertAt != "" {
			return time.Time{}, nil
		}
		if cp.LeaderAlertAt != "" {
			at, err := time.Parse(time.RFC3339Nano, cp.LeaderAlertAt)
			if err != nil {
				return time.Time{}, err
			}
			return at.Add(time.Duration(cp.EscalateAfterSeconds) * time.Second), nil
		}
		if cp.Status == "notified" {
			return cp.DueAt.Add(time.Duration(cp.AckTimeoutSeconds) * time.Second), nil
		}
		return cp.ResultDueAt, nil
	}
	return time.Time{}, nil
}

func saveCheckpoint(ctx context.Context, tx *sql.Tx, cp model.Checkpoint) error {
	at, err := checkpointNextAt(cp)
	if err != nil {
		return err
	}
	var next any
	if !at.IsZero() {
		next = at.UnixMilli()
	}
	body, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE checkpoints SET status=?,next_at=?,body=? WHERE team=? AND id=?", cp.Status, next, string(body), cp.Team, cp.ID)
	return err
}

func (s *Store) UpdateCheckpoint(ctx context.Context, team, id string, in CheckpointUpdateInput) (model.Checkpoint, error) {
	var cp model.Checkpoint
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		cp, err = getCheckpoint(ctx, tx, team, id)
		if err != nil {
			return err
		}
		caller, err := checkpointCaller(ctx, tx, team, in.Caller)
		if err != nil {
			return err
		}
		now := s.timestamp()
		if (in.Action == "ack" || in.Action == "complete") && (cp.Status == "notified" || cp.Status == "acknowledged") {
			at, err := checkpointNextAt(cp)
			if err != nil {
				return err
			}
			if !at.IsZero() && !s.Now().Before(at) {
				leader, err := leaderInbox(ctx, tx, team)
				if err != nil {
					return err
				}
				if leader == "" {
					leader = OperatorInbox
				}
				if err := advanceCheckpoint(ctx, tx, &cp, leader, now); err != nil {
					return err
				}
			}
		}
		switch in.Action {
		case "ack":
			if caller != cp.Owner {
				return conflictf("Only the checkpoint owner may acknowledge")
			}
			if cp.Status == "acknowledged" || cp.Status == "completed" {
				return saveCheckpoint(ctx, tx, cp)
			}
			if cp.Status != "notified" {
				return conflictf("Checkpoint is not awaiting acknowledgment")
			}
			cp.Status, cp.AcknowledgedAt, cp.Token = "acknowledged", now, randomHex(24)
		case "complete":
			if caller != cp.Owner || in.Token == "" || in.Token != cp.Token {
				return conflictf("Checkpoint owner and acknowledgment token must match")
			}
			if in.Outcome != "passed" && in.Outcome != "failed" && in.Outcome != "inconclusive" {
				return invalidf("Outcome must be passed, failed or inconclusive")
			}
			if strings.TrimSpace(in.Summary) == "" || strings.TrimSpace(in.Evidence) == "" {
				return invalidf("Summary and evidence are required")
			}
			if cp.Status == "completed" {
				if cp.Outcome == in.Outcome && cp.Summary == in.Summary && cp.Evidence == in.Evidence {
					return nil
				}
				return conflictf("Checkpoint already completed with a different result")
			}
			if cp.Status != "acknowledged" {
				return conflictf("Checkpoint is not acknowledged")
			}
			cp.Status, cp.CompletedAt, cp.Outcome, cp.Summary, cp.Evidence = "completed", now, in.Outcome, in.Summary, in.Evidence
			leader, err := leaderInbox(ctx, tx, team)
			if err != nil {
				return err
			}
			if leader == "" {
				leader = OperatorInbox
			}
			if err := checkpointMessage(ctx, tx, cp, leader, fmt.Sprintf("Checkpoint %s reported %s. Summary: %s. Evidence: %s. Verify the evidence before accepting the task.", cp.ID, cp.Outcome, cp.Summary, cp.Evidence), now); err != nil {
				return err
			}
		case "cancel":
			leader, err := leaderInbox(ctx, tx, team)
			if err != nil {
				return err
			}
			if caller != cp.Owner && caller != leader && caller != OperatorInbox {
				return conflictf("Only the owner, leader or operator may cancel")
			}
			if strings.TrimSpace(in.Reason) == "" {
				return invalidf("Cancellation reason is required")
			}
			if cp.Status == "canceled" {
				return nil
			}
			if cp.Status == "completed" {
				return conflictf("Completed checkpoint cannot be canceled")
			}
			cp.Status, cp.CanceledAt, cp.CancelReason = "canceled", now, in.Reason
			to := cp.Owner
			if caller == cp.Owner {
				to = leader
			}
			if to == "" || to == caller {
				to = OperatorInbox
			}
			if err := checkpointMessage(ctx, tx, cp, to, fmt.Sprintf("Checkpoint %s canceled by %s: %s. Do not execute its old notification; register a replacement if the observation is still needed.", cp.ID, caller, in.Reason), now); err != nil {
				return err
			}
		default:
			return invalidf("Unknown checkpoint action %q", in.Action)
		}
		return saveCheckpoint(ctx, tx, cp)
	})
	return cp, err
}

func checkpointMessage(ctx context.Context, tx *sql.Tx, cp model.Checkpoint, to, content, now string) error {
	return deliver(ctx, tx, cp.Team, to, model.Message{Type: "message", From: "team-worker", To: to, Content: content, RequestID: randomHex(12), Timestamp: now})
}

// AdvanceCheckpoints atomically advances due records and enqueues their notices.
// Every tick queries persisted deadlines, including those missed during downtime.
func (s *Store) AdvanceCheckpoints(ctx context.Context) error {
	var due bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM checkpoints WHERE next_at<=?)", s.Now().UnixMilli()).Scan(&due); err != nil {
		return err
	}
	if !due {
		return nil
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		now := s.Now()
		rows, err := tx.QueryContext(ctx, `SELECT c.body,COALESCE(CASE WHEN m.user_name='' THEN m.name ELSE m.user_name || '_' || m.name END,'operator')
   FROM checkpoints c JOIN teams t ON t.name=c.team
   LEFT JOIN members m ON m.team=t.name AND m.agent_id=t.lead_agent_id
   WHERE c.next_at<=? ORDER BY c.next_at LIMIT 100`, now.UnixMilli())
		if err != nil {
			return err
		}
		type due struct {
			cp     model.Checkpoint
			leader string
		}
		batch := []due{}
		for rows.Next() {
			var body, leader string
			if err := rows.Scan(&body, &leader); err != nil {
				rows.Close()
				return err
			}
			var cp model.Checkpoint
			if err := json.Unmarshal([]byte(body), &cp); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, due{cp, leader})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		stamp := now.UTC().Format("2006-01-02T15:04:05.000000-07:00")
		for _, item := range batch {
			cp := item.cp
			if err := advanceCheckpoint(ctx, tx, &cp, item.leader, stamp); err != nil {
				return err
			}
			if err := saveCheckpoint(ctx, tx, cp); err != nil {
				return err
			}
		}
		return nil
	})
}

func advanceCheckpoint(ctx context.Context, tx *sql.Tx, cp *model.Checkpoint, leader, stamp string) error {
	to, content := cp.Owner, ""
	if cp.Status == "scheduled" {
		cp.Status, cp.NotifiedAt = "notified", stamp
		content = fmt.Sprintf("Checkpoint %s for task %s is due. Action: %s. Expected evidence: %s. Result deadline: %s. Run 'team-worker checkpoint ack %s %s', then complete with the returned token and actual evidence. Re-read checkpoint status before acting; canceled checkpoints must not run.", cp.ID, cp.TaskID, cp.Action, cp.ExpectedEvidence, cp.ResultDueAt.Format(time.RFC3339), cp.Team, cp.ID)
	} else if cp.LeaderAlertAt == "" {
		cp.LeaderAlertAt = stamp
		cp.TimeoutReason = "result overdue"
		if cp.Status == "notified" {
			cp.TimeoutReason = "acknowledgment overdue"
		}
		to = leader
		if to == cp.Owner || to == OperatorInbox {
			to = OperatorInbox
			cp.OperatorAlertAt = stamp
		}
		content = fmt.Sprintf("Checkpoint %s for task %s owned by %s: %s. Inspect the member and evidence; cancel and replace the checkpoint if responsibility or deadlines change. No automatic interruption was performed.", cp.ID, cp.TaskID, cp.Owner, cp.TimeoutReason)
	} else {
		to = OperatorInbox
		cp.OperatorAlertAt = stamp
		content = fmt.Sprintf("Checkpoint %s for task %s owned by %s remains unresolved after the leader alert (%s). Inspect with 'team-worker checkpoint show %s %s'.", cp.ID, cp.TaskID, cp.Owner, cp.TimeoutReason, cp.Team, cp.ID)
	}
	if err := checkpointMessage(ctx, tx, *cp, to, content, stamp); err != nil {
		return err
	}
	return nil
}

// cancelTaskCheckpoints runs in the same transaction as task reassignment or completion.
func cancelTaskCheckpoints(ctx context.Context, tx *sql.Tx, team, taskID, now, reason string) error {
	rows, err := tx.QueryContext(ctx, "SELECT body FROM checkpoints WHERE team=? AND task_id=? AND status NOT IN ('completed','canceled')", team, taskID)
	if err != nil {
		return err
	}
	var batch []model.Checkpoint
	for rows.Next() {
		cp, err := scanCheckpoint(rows)
		if err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, cp)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, cp := range batch {
		cp.Status, cp.CanceledAt, cp.CancelReason = "canceled", now, reason
		if err := saveCheckpoint(ctx, tx, cp); err != nil {
			return err
		}
		if err := checkpointMessage(ctx, tx, cp, cp.Owner, fmt.Sprintf("Checkpoint %s canceled: %s. Do not execute its old notification or submit a result.", cp.ID, reason), now); err != nil {
			return err
		}
	}
	return nil
}
