package model

// JoinStatus is the state of a request to join a team.
type JoinStatus string

const (
	JoinPending  JoinStatus = "pending"
	JoinApproved JoinStatus = "approved"
	JoinRejected JoinStatus = "rejected"
)

// JoinRequest is a request to join a team, kept until the leader decides it.
type JoinRequest struct {
	RequestID    string     `json:"requestId"`
	TeamName     string     `json:"teamName"`
	ProposedName string     `json:"proposedName"`
	User         string     `json:"user,omitempty"`
	Capabilities string     `json:"capabilities,omitempty"`
	Status       JoinStatus `json:"status"`
	AssignedName string     `json:"assignedName,omitempty"`
	AgentID      string     `json:"agentId,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	DecidedBy    string     `json:"decidedBy,omitempty"`
	CreatedAt    string     `json:"createdAt"`
	DecidedAt    string     `json:"decidedAt,omitempty"`
}
