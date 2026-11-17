package runner

import (
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/spawn"
)

func CoordinationVersion() string { return spawn.ProtocolVersion() }

// CoordinationContext renders the same role-specific rules as a fresh spawn.
func CoordinationContext(a model.Agent, m model.Member, leader, teamPrompt, agentPrompt string) string {
	return spawn.BuildPrompt(spawn.PromptParams{AgentName: a.Name, AgentID: a.AgentID, AgentType: m.AgentType, Team: a.Team, Leader: leader, User: m.User, Workspace: a.Cwd, TeamPrompt: teamPrompt, Task: agentPrompt, PlanRequired: m.PlanRequired})
}
