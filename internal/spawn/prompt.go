package spawn

import (
	"fmt"
	"strings"
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

// BuildPrompt writes an agent's first message: who it is, where it works,
// the team's shared instructions, its task, and how to coordinate.
func BuildPrompt(p PromptParams) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	line("## Identity\n")
	line("- Name: %s", p.AgentName)
	line("- ID: %s", p.AgentID)
	if p.User != "" {
		line("- User: %s", p.User)
	}
	line("- Type: %s", p.AgentType)
	line("- Team: %s", p.Team)
	line("- Leader: %s", p.Leader)
	if p.Workspace != "" {
		line("\n## Workspace\n")
		line("- Your working directory is %s. It is yours alone; keep your files there unless the task says otherwise.", p.Workspace)
	}
	if p.TeamPrompt != "" {
		line("\n## Team Instructions\n")
		line("%s", p.TeamPrompt)
	}
	if p.Task != "" {
		line("\n## Task\n")
		line("%s", p.Task)
	}

	t, me, lead := p.Team, p.AgentName, p.Leader
	line("\n## Coordination Protocol\n")
	line("- Use `team-worker task list %s --owner %s` to see your tasks.", t, me)
	line("- If that list is empty, check `team-worker task list %s --status pending` and your inbox before declaring yourself idle.", t)
	line("- Starting a task: `team-worker task update %s <task-id> --status in_progress`", t)
	line("- If you changed files in a git repository, commit them before marking the task completed.")
	line("- Finishing a task: `team-worker task update %s <task-id> --status completed`", t)
	line("- When you finish a task, report the result to the leader:")
	line("  `team-worker inbox send %s %s \"<task-id> done: <brief summary>\"`", t, lead)
	line("- If you know what your work cost, report it: `team-worker cost report %s --input-tokens <N> --output-tokens <N> --cost-cents <N>`.", t)
	line("- If you are blocked or need help, message the leader:")
	line("  `team-worker inbox send %s %s \"Need help: <description>\"`", t, lead)
	line("- Do not exit after the first task unless the leader explicitly tells you to stop.")
	line("\n## Worker Loop Protocol\n")
	line("- After finishing your current task batch, re-check `team-worker task list %s --owner %s`.", t, me)
	line("- Then wait for new instructions with `team-worker inbox receive %s --wait 60`.", t)
	line("- If there is no work, notify the leader with `team-worker lifecycle idle %s` and keep checking for new work.", t)
	line("- When the leader sends a shutdown_request, finish or hand back your current task, run")
	line("  `team-worker lifecycle approve-shutdown %s <request-id>`, then exit.", t)
	return b.String()
}
