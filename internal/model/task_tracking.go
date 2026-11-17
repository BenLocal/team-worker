package model

import (
	"crypto/sha256"
	"fmt"
	"slices"
)

// TaskTracking keeps execution history separate from the final result.
type TaskTracking struct {
	WaitReview   *TaskWaitReview `json:"waitReview,omitempty"`
	Kind         string          `json:"kind,omitempty"`
	ParentID     string          `json:"parentId,omitempty"`
	SupersededBy string          `json:"supersededBy,omitempty"`
	Progress     []TaskProgress  `json:"progress,omitempty"`
}

type TaskProgress struct {
	WaitScope        string   `json:"waitScope,omitempty"`
	IndependentWork  string   `json:"independentWork,omitempty"`
	IndependentTasks []string `json:"independentTasks,omitempty"`
	Key              string   `json:"key"`
	Summary          string   `json:"summary"`
	State            string   `json:"state"`
	Reason           string   `json:"reason,omitempty"`
	NextAction       string   `json:"nextAction,omitempty"`
	CheckpointID     string   `json:"checkpointId,omitempty"`
	RequestID        string   `json:"requestId,omitempty"`
	Artifacts        []string `json:"artifacts,omitempty"`
	Verification     string   `json:"verification,omitempty"`
	ReportedAt       string   `json:"reportedAt"`
	ReportedBy       string   `json:"reportedBy,omitempty"`
}

// TaskWaitReview records the leader's evidence for waiting on the remaining work.
type TaskWaitReview struct {
	Version    string `json:"version"`
	Summary    string `json:"summary"`
	ReviewedBy string `json:"reviewedBy,omitempty"`
	ReviewedAt string `json:"reviewedAt,omitempty"`
}

// TaskWaitVersion binds a review to the task scope and immutable latest report.
func TaskWaitVersion(t Task) string {
	key := ""
	if len(t.Progress) > 0 {
		key = t.Progress[len(t.Progress)-1].Key
	}
	deps := slices.Clone(t.BlockedBy)
	slices.Sort(deps)
	scope := []string{t.ID, t.Subject, t.Description, t.Owner, t.Kind, t.ParentID, string(t.Status), key}
	scope = append(scope, deps...)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%q", scope))))
}

func (t Task) NeedsWaitReview() bool {
	return t.Kind != "goal" && t.Status != StatusCompleted && t.Status != StatusSuperseded && t.ExecutionState == "waiting" && (t.WaitReview == nil || t.WaitReview.Version != TaskWaitVersion(t))
}
