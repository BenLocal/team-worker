package model

// PlanStatus is where a submitted plan stands in its approval.
type PlanStatus string

const (
	PlanPending  PlanStatus = "pending"
	PlanApproved PlanStatus = "approved"
	PlanRejected PlanStatus = "rejected"
)

// ValidPlanStatus reports whether s names a plan status.
func ValidPlanStatus(s PlanStatus) bool {
	return s == PlanPending || s == PlanApproved || s == PlanRejected
}

// Plan is a member's plan and the leader's decision on it.
type Plan struct {
	ID        string     `json:"id"`
	Team      string     `json:"team"`
	Agent     string     `json:"agent"`
	Summary   string     `json:"summary,omitempty"`
	Body      string     `json:"plan"`
	Status    PlanStatus `json:"status"`
	Feedback  string     `json:"feedback,omitempty"`
	DecidedBy string     `json:"decidedBy,omitempty"`
	CreatedAt string     `json:"createdAt"`
	DecidedAt string     `json:"decidedAt,omitempty"`
}
