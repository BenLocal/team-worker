package model

import "time"

// EnvLayer is one named source of environment variables. Values may use
// ${NAME} to take a variable from the caller's environment.
type EnvLayer struct {
	Name  string
	Set   map[string]string
	Unset []string
}

// SpawnRequest starts one agent on the daemon's machine. User is the
// caller's; Profile names a runtime profile in the daemon's config.json.
type SpawnRequest struct {
	RequiredEnvKeys []string      `json:"requiredEnvKeys,omitempty"`
	RunID           string        `json:"-"`
	ResumeID        string        `json:"resumeId,omitempty"`
	KeepAlive       bool          `json:"keepalive,omitempty"`
	MaxRestarts     int           `json:"maxRestarts,omitempty"`
	RestartDelay    time.Duration `json:"restartDelay,omitempty"`
	Team            string        `json:"team"`
	Name            string        `json:"name"`
	AgentID         string        `json:"agentId,omitempty"`
	AgentType       string        `json:"agentType,omitempty"`
	Task            string        `json:"task,omitempty"`
	TaskSet         bool          `json:"taskSet,omitempty"`
	Backend         string        `json:"backend"`
	Command         []string      `json:"command,omitempty"`
	Cwd             string        `json:"cwd,omitempty"`
	// EnvLayers come from the template, the template agent and --env, in that order.
	EnvLayers []EnvLayer `json:"envLayers,omitempty"`
	SkipPerms bool       `json:"skipPermissions,omitempty"`
	Replace   bool       `json:"replace,omitempty"`
	DryRun    bool       `json:"dryRun,omitempty"`
	// PlanRequired makes the member wait for an approved plan before working.
	PlanRequired bool   `json:"planRequired,omitempty"`
	User         string `json:"user,omitempty"`
	Profile      string `json:"profile,omitempty"`
}

// SpawnResult describes a started agent, or for a dry run what would start.
type SpawnResult struct {
	Status    string   `json:"status"`
	Backend   string   `json:"backend"`
	AgentName string   `json:"agentName"`
	AgentID   string   `json:"agentId"`
	Target    string   `json:"target"`
	PID       int      `json:"pid"`
	Workspace string   `json:"workspace"`
	Command   []string `json:"command"`
	Adapter   string   `json:"adapter"`
	// Dry runs also report the full start: final arguments, prompt and
	// where each environment variable comes from (never its value).
	Args   []string    `json:"args,omitempty"`
	Prompt string      `json:"prompt,omitempty"`
	Env    []EnvOrigin `json:"env,omitempty"`
}

type EnvOrigin struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
}

// ResumeRequest resumes an agent's exact saved CLI session. Nil fields keep
// what the session saved; Task, when set, replaces the saved instructions.
type ResumeRequest struct {
	Team         string         `json:"team"`
	Name         string         `json:"name"`
	User         string         `json:"user,omitempty"`
	Task         *string        `json:"task,omitempty"`
	KeepAlive    *bool          `json:"keepalive,omitempty"`
	MaxRestarts  *int           `json:"maxRestarts,omitempty"`
	RestartDelay *time.Duration `json:"restartDelay,omitempty"`
	EnvLayers    []EnvLayer     `json:"envLayers,omitempty"`
	Profile      string         `json:"profile,omitempty"`
	Replace      bool           `json:"replace,omitempty"`
	DryRun       bool           `json:"dryRun,omitempty"`
}

// LaunchRequest creates a team from a template and spawns its agents. The
// template is TemplateText when set (a file the client read), else the
// daemon's template called Template.
type LaunchRequest struct {
	Template     string     `json:"template"`
	TemplateText string     `json:"templateText,omitempty"`
	Goal         string     `json:"goal,omitempty"`
	Team         string     `json:"team,omitempty"`
	Backend      string     `json:"backend,omitempty"`
	Cwd          string     `json:"cwd,omitempty"`
	TeamPrompt   string     `json:"teamPrompt,omitempty"`
	EnvLayers    []EnvLayer `json:"envLayers,omitempty"`
	Profile      string     `json:"profile,omitempty"`
	NoSkip       bool       `json:"noSkipPermissions,omitempty"`
	// Command replaces the template's agent command for every agent.
	Command []string `json:"command,omitempty"`
	User    string   `json:"user,omitempty"`
}

// LaunchedAgent is one agent of a launch; Error says why it did not start.
type LaunchedAgent struct {
	SpawnResult
	Error string `json:"error,omitempty"`
}

// LaunchResult reports a launch; Status is partial when an agent failed.
type LaunchResult struct {
	Status   string          `json:"status"`
	Team     string          `json:"team"`
	Template string          `json:"template"`
	Backend  string          `json:"backend"`
	Agents   []LaunchedAgent `json:"agents"`
}

// Conducting reports a team's harness conductor in the daemon. Started is
// false when one was already running.
type Conducting struct {
	Team       string `json:"team"`
	Run        string `json:"run,omitempty"`
	Conducting bool   `json:"conducting"`
	Started    bool   `json:"started"`
}

// ProfileTest is the result of running a profile once with a short prompt.
type ProfileTest struct {
	Profile    string   `json:"profile"`
	Agent      string   `json:"agent"`
	Command    []string `json:"command"`
	ReturnCode int      `json:"returncode"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr"`
}
