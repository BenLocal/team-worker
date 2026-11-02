package spawn

import (
	_ "embed"
	"strings"
	"text/template"
)

type PromptParams struct {
	AgentName string
	AgentID   string
	AgentType string
	Team      string
	Leader    string
	User      string
	Workspace string
	// Worktrees are the agent's isolated git worktrees, if any.
	Worktrees  []Worktree
	TeamPrompt string
	Task       string
	// Context is what the agent should know about the other agents' git
	// work, from the context inject command.
	Context string
	// PlanRequired adds how to get a plan approved before starting work.
	PlanRequired bool
}

// Worktree is one git worktree an agent works in: its directory, the
// repository it belongs to, its branch and the branch it started from.
type Worktree struct {
	Path, Repo, Branch, Base string
}

// IsLeader reports whether the prompt is for the team's leader, who gets the
// leader protocol instead of the worker protocol.
func (p PromptParams) IsLeader() bool { return p.AgentName != "" && p.AgentName == p.Leader }

// promptText is the agent's first message as a Markdown template, so the
// wording can be read and edited as a document.
//
//go:embed prompt.md
var promptText string

var promptTemplate = template.Must(template.New("prompt.md").Parse(promptText))

// BuildPrompt writes an agent's first message: who it is, where it works,
// the team's shared instructions, its task, and how to coordinate.
func BuildPrompt(p PromptParams) string {
	var b strings.Builder
	// The template is fixed and only reads string fields, so it cannot fail.
	if err := promptTemplate.Execute(&b, p); err != nil {
		panic("spawn: render prompt.md: " + err.Error())
	}
	return b.String()
}
