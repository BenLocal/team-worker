// Package model holds the data types shared by the daemon, the store and the CLI.
// JSON field names follow ClawTeam so agent prompts and scripts keep working.
package model

import (
	"fmt"
	"regexp"
)

type TaskStatus string

const (
	StatusPending    TaskStatus = "pending"
	StatusInProgress TaskStatus = "in_progress"
	StatusCompleted  TaskStatus = "completed"
	StatusBlocked    TaskStatus = "blocked"
)

func ParseTaskStatus(s string) (TaskStatus, error) {
	switch v := TaskStatus(s); v {
	case StatusPending, StatusInProgress, StatusCompleted, StatusBlocked:
		return v, nil
	}
	return "", fmt.Errorf("invalid status %q: use pending, in_progress, completed or blocked", s)
}

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

func ParsePriority(s string) (Priority, error) {
	switch v := Priority(s); v {
	case PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent:
		return v, nil
	}
	return "", fmt.Errorf("invalid priority %q: use low, medium, high or urgent", s)
}

// PriorityRank orders priorities with urgent first.
func PriorityRank(p Priority) int {
	switch p {
	case PriorityUrgent:
		return 0
	case PriorityHigh:
		return 1
	case PriorityLow:
		return 3
	}
	return 2
}

type MessageType string

var messageTypes = map[MessageType]bool{
	"message": true, "join_request": true, "join_approved": true, "join_rejected": true,
	"plan_approval_request": true, "plan_approved": true, "plan_rejected": true,
	"shutdown_request": true, "shutdown_approved": true, "shutdown_rejected": true,
	"idle": true, "broadcast": true,
}

func ParseMessageType(s string) (MessageType, error) {
	if messageTypes[MessageType(s)] {
		return MessageType(s), nil
	}
	return "", fmt.Errorf("invalid message type %q", s)
}

type Member struct {
	Name      string `json:"name"`
	User      string `json:"user"`
	AgentID   string `json:"agentId"`
	AgentType string `json:"agentType"`
	JoinedAt  string `json:"joinedAt"`
	// PlanRequired members need an approved plan before they start or finish tasks.
	PlanRequired bool `json:"planRequired,omitempty"`
}

// InboxName is the inbox key for a member: "<user>_<name>" when scoped by user.
func (m Member) InboxName() string {
	if m.User != "" {
		return m.User + "_" + m.Name
	}
	return m.Name
}

type Team struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	LeadAgentID string   `json:"leadAgentId"`
	CreatedAt   string   `json:"createdAt"`
	Members     []Member `json:"members"`
}

type TeamSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	LeadAgentID string `json:"leadAgentId"`
	MemberCount int    `json:"memberCount"`
}

type Task struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	Status      TaskStatus     `json:"status"`
	Priority    Priority       `json:"priority"`
	Owner       string         `json:"owner"`
	LockedBy    string         `json:"lockedBy"`
	LockedAt    string         `json:"lockedAt"`
	Blocks      []string       `json:"blocks"`
	BlockedBy   []string       `json:"blockedBy"`
	StartedAt   string         `json:"startedAt"`
	CreatedAt   string         `json:"createdAt"`
	UpdatedAt   string         `json:"updatedAt"`
	Metadata    map[string]any `json:"metadata"`
}

type TaskStats struct {
	Total              int     `json:"total"`
	Completed          int     `json:"completed"`
	InProgress         int     `json:"in_progress"`
	Pending            int     `json:"pending"`
	Blocked            int     `json:"blocked"`
	TimedCompleted     int     `json:"timed_completed"`
	AvgDurationSeconds float64 `json:"avg_duration_seconds"`
}

type Message struct {
	Type         MessageType `json:"type"`
	From         string      `json:"from"`
	To           string      `json:"to,omitempty"`
	Content      string      `json:"content"`
	RequestID    string      `json:"requestId,omitempty"`
	Timestamp    string      `json:"timestamp"`
	Key          string      `json:"key,omitempty"`
	ProposedName string      `json:"proposedName,omitempty"`
	Capabilities string      `json:"capabilities,omitempty"`
	AssignedName string      `json:"assignedName,omitempty"`
	AgentID      string      `json:"agentId,omitempty"`
	TeamName     string      `json:"teamName,omitempty"`
	PlanFile     string      `json:"planFile,omitempty"`
	Summary      string      `json:"summary,omitempty"`
	Plan         string      `json:"plan,omitempty"`
	Feedback     string      `json:"feedback,omitempty"`
	Reason       string      `json:"reason,omitempty"`
	LastTask     string      `json:"lastTask,omitempty"`
	Status       string      `json:"status,omitempty"`
}

type AgentStatus string

const (
	AgentRunning AgentStatus = "running"
	AgentExited  AgentStatus = "exited"
)

// Agent is a spawned agent process. Target is the tmux pane id for the tmux
// backend; PID is the pane's shell or the subprocess.
type Agent struct {
	PreviousRunID string      `json:"previousRunId,omitempty"`
	RunID         string      `json:"runId,omitempty"`
	StopRequested bool        `json:"stopRequested,omitempty"`
	ExitCode      int         `json:"exitCode,omitempty"`
	Team          string      `json:"team"`
	Name          string      `json:"name"`
	AgentID       string      `json:"agentId"`
	Backend       string      `json:"backend"`
	Target        string      `json:"target"`
	PID           int         `json:"pid"`
	Command       []string    `json:"command"`
	Cwd           string      `json:"cwd"`
	Status        AgentStatus `json:"status"`
	StartedAt     string      `json:"startedAt"`
	ExitedAt      string      `json:"exitedAt"`
	// Nudge lets the daemon type a reminder into the agent's tmux pane when
	// messages wait while the agent sits idle at its prompt.
	Nudge bool `json:"nudge"`
	// Alive is filled in by the daemon from a live process probe.
	Alive *bool `json:"alive,omitempty"`
}

// Session is the resumable CLI session, independent of its current process.
// Environment values are deliberately excluded; callers supply them again.
type Session struct {
	Team               string   `json:"team"`
	Agent              string   `json:"agent"`
	User               string   `json:"user,omitempty"`
	RunID              string   `json:"runId,omitempty"`
	Client             string   `json:"client"`
	SessionID          string   `json:"sessionId"`
	Source             string   `json:"source,omitempty"`
	Cwd                string   `json:"cwd"`
	Command            []string `json:"command"`
	Backend            string   `json:"backend"`
	SkipPermissions    bool     `json:"skipPermissions"`
	KeepAlive          bool     `json:"keepalive"`
	MaxRestarts        int      `json:"maxRestarts"`
	RestartDelayMillis int64    `json:"restartDelayMillis"`
	EnvKeys            []string `json:"envKeys,omitempty"`
	UpdatedAt          string   `json:"updatedAt"`
}

// AgentExit is the outcome of recording an agent's exit.
type AgentExit struct {
	Agent string `json:"agent"`
	// Changed is false when the agent was already recorded as exited.
	Changed  bool   `json:"changed"`
	Released []Task `json:"released"`
}

// BoardMember is one row of the team board.
type BoardMember struct {
	Member
	InboxCount int    `json:"inboxCount"`
	Agent      *Agent `json:"agent,omitempty"`
}

type Board struct {
	Team     Team          `json:"team"`
	Members  []BoardMember `json:"members"`
	Tasks    []Task        `json:"tasks"`
	Stats    TaskStats     `json:"taskSummary"`
	Messages []Message     `json:"messages"`
}

var identifierRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateIdentifier checks names used as team, agent and user keys.
func ValidateIdentifier(value, kind string, allowEmpty bool) error {
	if value == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("invalid %s: value must not be empty", kind)
	}
	if value == "." || value == ".." {
		return fmt.Errorf("invalid %s: '.' and '..' are not allowed", kind)
	}
	if !identifierRE.MatchString(value) {
		return fmt.Errorf("invalid %s: only letters, digits, '.', '_' and '-' are allowed", kind)
	}
	return nil
}
