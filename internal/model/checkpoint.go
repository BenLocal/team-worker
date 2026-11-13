package model

import "time"

// Checkpoint records one scheduled observation, independently of task completion.
// Notifications and timeout alerts do not prove that the observation ran.
type Checkpoint struct {
	ID                   string    `json:"id"`
	Team                 string    `json:"team"`
	Key                  string    `json:"key"`
	TaskID               string    `json:"taskId"`
	Owner                string    `json:"owner"`
	CreatedBy            string    `json:"createdBy"`
	Status               string    `json:"status"`
	DueAt                time.Time `json:"dueAt"`
	AckTimeoutSeconds    int64     `json:"ackTimeoutSeconds"`
	ResultDueAt          time.Time `json:"resultDueAt"`
	EscalateAfterSeconds int64     `json:"escalateAfterSeconds"`
	Action               string    `json:"action"`
	ExpectedEvidence     string    `json:"expectedEvidence"`
	CreatedAt            string    `json:"createdAt"`
	NotifiedAt           string    `json:"notifiedAt,omitempty"`
	AcknowledgedAt       string    `json:"acknowledgedAt,omitempty"`
	Token                string    `json:"token,omitempty"`
	CompletedAt          string    `json:"completedAt,omitempty"`
	Outcome              string    `json:"outcome,omitempty"`
	Summary              string    `json:"summary,omitempty"`
	Evidence             string    `json:"evidence,omitempty"`
	CanceledAt           string    `json:"canceledAt,omitempty"`
	CancelReason         string    `json:"cancelReason,omitempty"`
	LeaderAlertAt        string    `json:"leaderAlertAt,omitempty"`
	OperatorAlertAt      string    `json:"operatorAlertAt,omitempty"`
	TimeoutReason        string    `json:"timeoutReason,omitempty"`
}
