package model

import "slices"

// EventType describes an event user hooks can subscribe to. Names follow
// ClawTeam's event classes where the meaning matches.
type EventType struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Fields      []string `json:"fields"`
}

// EventTypes lists every event the daemon emits. Every event also carries
// team_name and timestamp.
var EventTypes = []EventType{
	{"AfterWorkerSpawn", "An agent run started (spawn, resume or keepalive restart)", []string{"agent_name", "agent_id", "backend", "target"}},
	{"WorkerExit", "An agent run ended; its in-progress tasks went back to pending", []string{"agent_name", "exit_code", "abandoned_tasks"}},
	{"WorkerCrash", "An agent run ended with a non-zero exit code without being asked to stop", []string{"agent_name", "exit_code", "error"}},
	{"AgentIdle", "An agent told its leader it has no work", []string{"agent_name"}},
	{"MemberJoined", "A member was added to a team", []string{"agent_name", "agent_type", "user"}},
	{"MemberLeft", "A member was removed from a team", []string{"agent_name"}},
	{"TaskCreated", "A task was created", []string{"task_id", "subject", "owner"}},
	{"AfterTaskUpdate", "A task was updated", []string{"task_id", "old_status", "new_status", "owner"}},
	{"TaskCompleted", "A task changed to completed", []string{"task_id", "owner", "duration_seconds"}},
	{"BudgetExceeded", "The team's spending reached its budget", []string{"spent_cents", "budget_cents", "enforce"}},
	{"PhaseTransition", "A harness run moved to another phase", []string{"run_id", "from_phase", "to_phase"}},
	{"TeamShutdown", "A team was deleted (team cleanup)", []string{}},
}

// LookupEventType returns the event type with the given name.
func LookupEventType(name string) (EventType, bool) {
	i := slices.IndexFunc(EventTypes, func(t EventType) bool { return t.Name == name })
	if i < 0 {
		return EventType{}, false
	}
	return EventTypes[i], true
}

// EventHook is a user hook; field names follow ClawTeam's HookDef. Action
// is always "shell" and Enabled always true: team-worker has no Python
// hooks and removes hooks instead of disabling them.
type EventHook struct {
	ID       string `json:"id"`
	Event    string `json:"event"`
	Action   string `json:"action"`
	Command  string `json:"command"`
	Priority int    `json:"priority"`
	Enabled  bool   `json:"enabled"`
	// Team limits the hook to one team; empty matches every team.
	Team      string `json:"team,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// Matches reports whether the hook runs for an event of the given type and team.
func (h EventHook) Matches(event, team string) bool {
	return h.Event == event && (h.Team == "" || h.Team == team)
}

// Event is one recorded event. Payload holds type, team_name, timestamp and
// the type's fields, as hooks receive it.
type Event struct {
	ID      int64          `json:"id"`
	Type    string         `json:"type"`
	Team    string         `json:"team_name"`
	Payload map[string]any `json:"payload"`
}

// HookResult is the outcome of running one hook.
type HookResult struct {
	HookID   string `json:"hook_id"`
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	// Output is the command's combined stdout and stderr, truncated.
	Output     string `json:"output"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// HookTestResult is what `event hook test` reports.
type HookTestResult struct {
	Event   string         `json:"event"`
	Payload map[string]any `json:"payload"`
	Results []HookResult   `json:"results"`
}
