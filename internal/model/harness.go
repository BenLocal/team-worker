package model

// Harness phases, in order. The run completes when it reaches HarnessShip.
const (
	HarnessDiscuss = "discuss"
	HarnessPlan    = "plan"
	HarnessExecute = "execute"
	HarnessVerify  = "verify"
	HarnessShip    = "ship"
)

// HarnessPhases is the fixed phase order of a harness run.
var HarnessPhases = []string{HarnessDiscuss, HarnessPlan, HarnessExecute, HarnessVerify, HarnessShip}

// Harness roles, the agent kind each phase needs.
const (
	RolePlanner   = "planner"
	RoleExecutor  = "executor"
	RoleEvaluator = "evaluator"
)

// HarnessPhaseRole maps a phase to the role of the agents that work in it;
// ship needs none.
var HarnessPhaseRole = map[string]string{
	HarnessDiscuss: RolePlanner,
	HarnessPlan:    RolePlanner,
	HarnessExecute: RoleExecutor,
	HarnessVerify:  RoleEvaluator,
}

// Harness run statuses.
const (
	HarnessRunning   = "running"
	HarnessCompleted = "completed"
	HarnessAborted   = "aborted"
	HarnessFailed    = "failed"
)

// Artifacts the phase gates look for.
const (
	ArtifactDiscussion = "discussion.md"
	ArtifactSpec       = "spec.md"
	ArtifactEvalReport = "eval-report.json"
)

// HarnessRun is one plan-then-execute run of a team. Keys are snake_case
// like ClawTeam's harness state.
type HarnessRun struct {
	ID          string `json:"harness_id"`
	Team        string `json:"team"`
	Goal        string `json:"goal"`
	CLI         string `json:"cli"`
	AgentCount  int    `json:"agent_count"`
	Phase       string `json:"phase"`
	Status      string `json:"status"`
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// HarnessEvent is one entry of a run's history.
type HarnessEvent struct {
	Phase  string `json:"phase"`
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
	At     string `json:"at"`
}

// HarnessAgent is an agent the conductor started for a run.
type HarnessAgent struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	Phase     string `json:"phase"`
	CreatedAt string `json:"created_at"`
}

// HarnessArtifact is a named deliverable registered for a run. Content is
// left out of listings.
type HarnessArtifact struct {
	Name      string `json:"name"`
	Size      int    `json:"size"`
	Content   string `json:"content,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// HarnessStatus is a run with its gate state, artifacts, history and agents.
type HarnessStatus struct {
	HarnessRun
	CanAdvance bool           `json:"can_advance"`
	GateReason string         `json:"gate_reason,omitempty"`
	Artifacts  []string       `json:"artifacts"`
	History    []HarnessEvent `json:"history"`
	Agents     []HarnessAgent `json:"agents"`
}

// HarnessAdvance is the outcome of advancing a run.
type HarnessAdvance struct {
	ID         string `json:"harness_id"`
	From       string `json:"from"`
	Phase      string `json:"phase"`
	Status     string `json:"status"`
	RolledBack bool   `json:"rolled_back,omitempty"`
	Attempt    int    `json:"attempt"`
}
