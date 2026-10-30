// Package harness builds the task prompts of the agents a harness run
// starts. Run state and phase gates live in the store.
package harness

import (
	"fmt"
	"strings"

	"github.com/benlocal/team-worker/internal/model"
)

// PromptParams describes one agent of a run.
type PromptParams struct {
	Team    string
	RunID   string
	Goal    string
	Phase   string
	Role    string
	Agent   string
	Attempt int
}

// RolePrompt is the task prompt for an agent starting in a run's phase. The
// spawn prompt around it already carries the identity and coordination
// protocol, so this only adds what the role must produce and register.
func RolePrompt(p PromptParams) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	t := p.Team
	line("## Harness run %s", p.RunID)
	line("")
	line("Goal: %s", p.Goal)
	line("Phase: %s (attempt %d). Phases: discuss → plan → execute → verify → ship.", p.Phase, p.Attempt)
	line("You are the %s of this run. Check progress with `team-worker harness status %s`.", p.Role, t)
	line("Read registered artifacts with `team-worker harness artifacts %s --show <name>`.", t)
	line("")
	switch p.Role {
	case model.RolePlanner:
		line("1. Discuss: study the goal and the repository, then write discussion.md (open questions, options, risks) and register it:")
		line("   `team-worker harness artifact %s discussion.md --file discussion.md`", t)
		line("2. Plan: write spec.md (scope, design, acceptance criteria that the evaluator can check) and register it:")
		line("   `team-worker harness artifact %s spec.md --file spec.md`", t)
		line("   Then submit it for approval: `team-worker plan submit %s %s spec.md --summary \"<one line>\"`.", t, p.Agent)
		line("   Wait for plan_approved. If you get plan_rejected, revise spec.md, register it again and resubmit.")
		line("3. After approval, split spec.md into independent executor tasks, one command per task:")
		line("   `team-worker harness task %s \"<subject>\" -d \"<what to do and how to check it>\"`", t)
		line("   Leave owners empty unless a task must go to a specific executor. Then tell the leader the tasks are ready.")
	case model.RoleExecutor:
		line("Work on the tasks of this run: `team-worker task list %s --status pending`.", t)
		line("Read spec.md first (`team-worker harness artifacts %s --show spec.md`) and follow it.", t)
		line("Claim a task with `team-worker task update %s <id> --status in_progress`, finish it, commit your changes,", t)
		line("then mark it completed. Repeat until no task of the run is left, and check your inbox for rework requests.")
	case model.RoleEvaluator:
		line("Check the finished work against the acceptance criteria in spec.md (`team-worker harness artifacts %s --show spec.md`).", t)
		line("Run the tests and checks the spec names. Then register your verdict as eval-report.json:")
		line("   `team-worker harness artifact %s eval-report.json --content '{\"verdict\":\"pass\",\"summary\":\"...\"}'`", t)
		line("Use \"verdict\":\"fail\" with concrete \"feedback\" (what is wrong, where, how to check the fix) when any")
		line("criterion is not met; the run then goes back to execute with your feedback.")
	}
	return b.String()
}
