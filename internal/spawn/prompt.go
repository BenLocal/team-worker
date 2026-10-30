package spawn

import (
	_ "embed"
	"strings"
	"text/template"
)

type PromptParams struct {
	AgentName  string
	AgentID    string
	AgentType  string
	Team       string
	Leader     string
	User       string
	Workspace  string
	TeamPrompt string
	Task       string
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
