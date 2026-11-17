package model

import "time"

// MemberWork describes assignments, independently of process liveness or activity.
type MemberWork struct {
	State        string   `json:"state"`
	Reason       string   `json:"reason"`
	OpenTasks    int      `json:"openTasks"`
	ReadyTasks   int      `json:"readyTasks"`
	Unreported   []string `json:"unreported,omitempty"`
	NextAt       string   `json:"nextAt,omitempty"`
	CheckpointID string   `json:"checkpointId,omitempty"`
}

func SummarizeMemberWork(tasks []Task, owner string, now time.Time) MemberWork {
	w := MemberWork{State: "standby", Reason: "No open execution tasks"}
	var next time.Time
	waiting := 0
	for _, t := range tasks {
		if t.Owner != owner || t.Kind == "goal" || t.Status == StatusCompleted || t.Status == StatusSuperseded {
			continue
		}
		w.OpenTasks++
		if cp := t.NextCheckpoint; cp != nil && (next.IsZero() || cp.DueAt.Before(next)) {
			next = cp.DueAt
			w.NextAt = cp.DueAt.Format(time.RFC3339)
			w.CheckpointID = cp.ID
		}
		if t.ExecutionState == "waiting" {
			if t.WaitCheckpoint != nil && !t.WaitCheckpoint.DueAt.After(now) {
				w.ReadyTasks++
			} else {
				waiting++
			}
			continue
		}
		if t.Status == StatusBlocked && len(t.BlockedBy) > 0 {
			waiting++
			continue
		}
		w.ReadyTasks++
		if len(t.Progress) == 0 || t.ExecutionState == "unregistered" {
			w.Unreported = append(w.Unreported, t.ID)
		}
	}
	switch {
	case w.OpenTasks == 0:
	case len(w.Unreported) > 0:
		w.State = "needs_review"
		w.Reason = "Open tasks have no stage report or registered waiting trigger"
	case w.ReadyTasks > 0:
		w.State = "assigned"
		w.Reason = "Assigned work needs execution or follow-up"
	case waiting == w.OpenTasks:
		w.State = "waiting"
		w.Reason = "All open tasks wait on registered triggers or task dependencies"
	}
	return w
}
