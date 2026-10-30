package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/benlocal/team-worker/internal/agent"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/spawn"
	"github.com/benlocal/team-worker/internal/store"
)

// hookTarget is the agent a hook runs for.
type hookTarget struct {
	team, agent, role, user string
}

func (h *hookTarget) flags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&h.team, "team", "", "Team name (default: TEAM_WORKER_TEAM_NAME)")
	cmd.Flags().StringVar(&h.agent, "agent", "", "Agent name (default: TEAM_WORKER_AGENT_NAME)")
	cmd.Flags().StringVar(&h.role, "role", "", "worker or leader (default: from TEAM_WORKER_AGENT_LEADER)")
}

func (h *hookTarget) resolve() bool {
	id := config.IdentityFromEnv()
	if h.team == "" {
		h.team = id.TeamName
	}
	if h.agent == "" {
		h.agent = id.AgentName
	}
	if h.role == "" {
		h.role = "worker"
		if id.IsLeader {
			h.role = "leader"
		}
	}
	h.user = id.User
	return h.team != "" && h.agent != ""
}

func (h hookTarget) leader() bool { return h.role == "leader" }

// hookInput is the part of the agent CLI's hook JSON that team-worker reads.
type hookInput struct {
	SessionID      string `json:"session_id"`
	StopHookActive bool   `json:"stop_hook_active"`
	Source         string `json:"source"`
	Cwd            string `json:"cwd"`
}

func (a *app) readHookInput() hookInput {
	var in hookInput
	data, _ := io.ReadAll(a.in)
	json.Unmarshal(data, &in)
	return in
}

func (a *app) hookBlock(reason string) {
	printJSONLine(a.out, map[string]string{"decision": "block", "reason": reason})
}

func (a *app) hookContext(event, text string) {
	printJSONLine(a.out, map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": event, "additionalContext": text}})
}

func (a *app) hookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Agent CLI hook handlers that deliver team messages and tasks",
		Long: "Handlers for agent CLI hooks (Claude Code format). spawn installs them for adapters\n" +
			"that support hooks; `hook settings` prints them for a session you start yourself.\n" +
			"Handlers read the hook JSON on stdin, always exit 0, and print nothing on errors.",
	}
	cmd.AddCommand(a.hookStopCmd(), a.hookPostToolCmd(), a.hookSessionStartCmd(), a.hookPromptSubmitCmd(),
		a.hookNotificationCmd(), a.hookSettingsCmd())
	return cmd
}

// reportActivity tells the daemon what the agent is doing, so it types a
// reminder only into an agent that waits at its prompt. Errors are dropped:
// a missed report only delays a reminder.
func (a *app) reportActivity(ctx context.Context, t hookTarget, activity model.AgentActivity) {
	a.client().SetAgentActivity(ctx, t.team, t.agent, os.Getenv("TEAM_WORKER_RUN_ID"), activity)
}

func (a *app) hookStopCmd() *cobra.Command {
	var t hookTarget
	var wait int
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop hook: hand over new messages and work instead of letting the agent stop",
		Long: "Keeps the agent working while there is something for it: unread messages, its own open\n" +
			"tasks, or (for the leader) teammates' progress. Otherwise it waits up to --wait seconds\n" +
			"for new work and then lets the agent stop, telling the leader a worker is idle.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in := a.readHookInput()
			if !t.resolve() {
				return nil
			}
			if wait < 0 {
				wait = agent.WorkerStopWait
				if t.leader() {
					wait = agent.LeaderStopWait
				}
			}
			// Letting the agent stop leaves it at its prompt, where no hook
			// runs until something is typed into its terminal.
			if reason := a.stopReason(cmd.Context(), t, in, time.Duration(wait)*time.Second); reason != "" {
				a.reportActivity(cmd.Context(), t, model.ActivityWorking)
				a.hookBlock(reason)
			} else {
				a.reportActivity(cmd.Context(), t, model.ActivityWaiting)
			}
			return nil
		},
	}
	t.flags(cmd)
	cmd.Flags().IntVar(&wait, "wait", -1, "Seconds to wait for new work before allowing the stop "+
		fmt.Sprintf("(default: %d for workers, %d for the leader)", agent.WorkerStopWait, agent.LeaderStopWait))
	return cmd
}

// stopReason returns why the agent should keep going, or "" to let it stop.
func (a *app) stopReason(ctx context.Context, t hookTarget, in hookInput, wait time.Duration) string {
	c := a.client()
	if msgs, err := c.Receive(ctx, t.team, t.agent, t.user, 20, 0); err == nil && len(msgs) > 0 {
		return formatMessages(t, msgs)
	}
	before, err := a.hookTasks(ctx, t)
	if err != nil {
		return ""
	}
	if t.leader() {
		if countTasks(before).completed == len(before) {
			return ""
		}
	} else if open := openTasks(before); len(open) > 0 && !in.StopHookActive {
		return fmt.Sprintf("You still have %d open task(s) in team %s:\n%s\nContinue with them. If you "+
			"cannot, tell the leader why with `team-worker inbox send %s <leader> \"...\"`.",
			len(open), t.team, taskLines(open), t.team)
	}

	version, err := c.Changes(ctx, 0, 0)
	if err != nil {
		return ""
	}
	deadline := time.Now().Add(wait)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if version, err = c.Changes(ctx, version, min(remaining, 55*time.Second)); err != nil {
			return ""
		}
		if msgs, err := c.Receive(ctx, t.team, t.agent, t.user, 20, 0); err == nil && len(msgs) > 0 {
			return formatMessages(t, msgs)
		}
		after, err := a.hookTasks(ctx, t)
		if err != nil {
			return ""
		}
		if changes := taskChanges(before, after); len(changes) > 0 {
			if t.leader() {
				return leaderProgress(t, after, changes)
			}
			if fresh := newWork(before, after); len(fresh) > 0 {
				return fmt.Sprintf("New work for you in team %s:\n%s\nStart it now.", t.team, taskLines(fresh))
			}
			before = after
		}
	}
	if !t.leader() && len(openTasks(before)) == 0 {
		a.reportIdle(ctx, t)
	}
	return ""
}

// hookTasks returns the tasks a hook watches: the whole team for the
// leader, the agent's own tasks for a worker.
func (a *app) hookTasks(ctx context.Context, t hookTarget) ([]model.Task, error) {
	f := store.TaskFilter{}
	if !t.leader() {
		f.Owner = t.agent
	}
	return a.client().ListTasks(ctx, t.team, f)
}

func openTasks(tasks []model.Task) []model.Task {
	var open []model.Task
	for _, task := range tasks {
		if task.Status == model.StatusPending || task.Status == model.StatusInProgress {
			open = append(open, task)
		}
	}
	return open
}

// newWork lists tasks that became startable: new pending tasks or ones that
// moved from blocked to pending.
func newWork(before, after []model.Task) []model.Task {
	was := map[string]model.TaskStatus{}
	for _, task := range before {
		was[task.ID] = task.Status
	}
	var fresh []model.Task
	for _, task := range after {
		if task.Status == model.StatusPending && was[task.ID] != model.StatusPending {
			fresh = append(fresh, task)
		}
	}
	return fresh
}

// taskChanges describes every task whose status changed or that is new.
func taskChanges(before, after []model.Task) []string {
	was := map[string]model.TaskStatus{}
	for _, task := range before {
		was[task.ID] = task.Status
	}
	var changes []string
	for _, task := range after {
		old, seen := was[task.ID]
		switch {
		case !seen:
			changes = append(changes, fmt.Sprintf("- %s %q (@%s): new, %s", task.ID, task.Subject, task.Owner, task.Status))
		case old != task.Status:
			changes = append(changes, fmt.Sprintf("- %s %q (@%s): %s -> %s", task.ID, task.Subject, task.Owner, old, task.Status))
		}
	}
	return changes
}

func leaderProgress(t hookTarget, tasks []model.Task, changes []string) string {
	n := countTasks(tasks)
	return fmt.Sprintf("Team %s progress: %d/%d tasks completed (%d in progress, %d pending, %d blocked).\n"+
		"Changes:\n%s\nCheck the results that came in, answer or redirect members with `team-worker inbox send`, "+
		"and keep coordinating until every task is completed.",
		t.team, n.completed, n.total, n.inProgress, n.pending, n.blocked, strings.Join(changes, "\n"))
}

func taskLines(tasks []model.Task) string {
	lines := make([]string, len(tasks))
	for i, task := range tasks {
		lines[i] = fmt.Sprintf("- %s %q (%s)", task.ID, task.Subject, task.Status)
	}
	return strings.Join(lines, "\n")
}

func formatMessages(t hookTarget, msgs []model.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You have %d new team message(s) in team %s:\n", len(msgs), t.team)
	shutdown := false
	for _, m := range msgs {
		kind := string(m.Type)
		if m.Type == "shutdown_request" {
			kind += ", request " + m.RequestID
			shutdown = true
		}
		fmt.Fprintf(&b, "\n[%s] from %s (%s):\n%s\n", formatTimestamp(m.Timestamp), m.From, kind, m.Content)
	}
	b.WriteString("\nHandle these messages now, then continue with your work.")
	if shutdown {
		fmt.Fprintf(&b, " For a shutdown request: finish or hand back your current task, run "+
			"`team-worker lifecycle approve-shutdown %s <request-id>`, then exit.", t.team)
	}
	return b.String()
}

// reportIdle tells the leader once per idle stretch that this worker has
// nothing to do.
func (a *app) reportIdle(ctx context.Context, t hookTarget) {
	c := a.client()
	if last, err := c.MessageLog(ctx, t.team, 1, t.agent); err == nil && len(last) == 1 && last[0].Type == "idle" {
		return
	}
	leader, err := c.Leader(ctx, t.team)
	if err != nil || leader.Name == t.agent {
		return
	}
	c.Send(ctx, t.team, store.SendInput{From: t.agent, To: leader.Name, Type: "idle",
		Content: t.agent + " is idle: no open tasks and no new messages"})
}

// postToolInterval throttles inbox checks after tool calls.
const postToolInterval = 15 * time.Second

func (a *app) hookPostToolCmd() *cobra.Command {
	var t hookTarget
	cmd := &cobra.Command{
		Use:   "post-tool-use",
		Short: "PostToolUse hook: add messages that arrived while the agent works",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.readHookInput()
			if !t.resolve() {
				return nil
			}
			// Not throttled: a tool running after a permission prompt ends
			// the blocked state.
			a.reportActivity(cmd.Context(), t, model.ActivityWorking)
			if !throttle(t, postToolInterval) {
				return nil
			}
			msgs, err := a.client().Receive(cmd.Context(), t.team, t.agent, t.user, 20, 0)
			if err == nil && len(msgs) > 0 {
				a.hookContext("PostToolUse", formatMessages(t, msgs))
			}
			return nil
		},
	}
	t.flags(cmd)
	return cmd
}

// throttle reports whether interval has passed since it last returned true
// for this agent, using a marker file's modification time.
func throttle(t hookTarget, interval time.Duration) bool {
	dir := filepath.Join(config.DataDir(), "run", "hooks")
	path := filepath.Join(dir, t.team+"."+t.agent+".post-tool-use")
	if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < interval {
		return false
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return true
	}
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		os.WriteFile(path, nil, 0o600)
	}
	return true
}

func (a *app) hookSessionStartCmd() *cobra.Command {
	var t hookTarget
	cmd := &cobra.Command{
		Use:   "session-start",
		Short: "SessionStart hook: restore the team context after a resume, clear or compaction",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in := a.readHookInput()
			if !t.resolve() {
				return nil
			}
			a.reportActivity(cmd.Context(), t, model.ActivityWorking)
			if runID := os.Getenv("TEAM_WORKER_RUN_ID"); runID != "" && in.SessionID != "" {
				if session, err := a.client().GetSession(cmd.Context(), t.team, t.agent, t.user); err == nil && session.RunID == runID {
					session.SessionID, session.Source = in.SessionID, "hook"
					if _, err := a.client().SaveSession(cmd.Context(), session); err != nil {
						fmt.Fprintf(a.errOut, "capture session: %v\n", err)
					}
				}
			}
			if in.Source == "startup" {
				return nil
			}
			text, err := a.teamContext(cmd.Context(), t, in.Cwd)
			if err == nil {
				a.hookContext("SessionStart", fmt.Sprintf("Your conversation was restarted (%s). "+
					"Your team-worker context:\n\n%s", in.Source, text))
			}
			return nil
		},
	}
	t.flags(cmd)
	return cmd
}

func (a *app) hookPromptSubmitCmd() *cobra.Command {
	var t hookTarget
	cmd := &cobra.Command{
		Use:   "prompt-submit",
		Short: "UserPromptSubmit hook: record that the agent started a turn",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.readHookInput()
			if t.resolve() {
				a.reportActivity(cmd.Context(), t, model.ActivityWorking)
			}
			return nil
		},
	}
	t.flags(cmd)
	return cmd
}

func (a *app) hookNotificationCmd() *cobra.Command {
	var t hookTarget
	var activity string
	cmd := &cobra.Command{
		Use:   "notification",
		Short: "Notification hook: record that the agent waits at its prompt or asks the user",
		Long: "The hook settings pick --activity by notification type: waiting for idle_prompt\n" +
			"(also covers a turn the user interrupted, which runs no Stop hook), blocked for\n" +
			"permission_prompt and elicitation_dialog.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.readHookInput()
			if act := model.AgentActivity(activity); t.resolve() && act.Valid() {
				a.reportActivity(cmd.Context(), t, act)
			}
			return nil
		},
	}
	t.flags(cmd)
	cmd.Flags().StringVar(&activity, "activity", "", "Activity to report: waiting or blocked")
	return cmd
}

// teamContext restores saved instructions and the current task board.
func (a *app) teamContext(ctx context.Context, t hookTarget, cwd string) (string, error) {
	c := a.client()
	team, err := c.GetTeam(ctx, t.team)
	if err != nil {
		return "", err
	}
	prompt, err := c.TeamPrompt(ctx, t.team)
	if err != nil {
		return "", err
	}
	agentInstructions, err := c.AgentPrompt(ctx, t.team, t.agent, t.user)
	if err != nil {
		return "", err
	}
	tasks, err := a.hookTasks(ctx, t)
	if err != nil {
		return "", err
	}
	id := config.IdentityFromEnv()
	if ws := os.Getenv("TEAM_WORKER_WORKSPACE_DIR"); ws != "" {
		cwd = ws
	}
	agentType := id.AgentType
	if m := findMember(team, t.agent, t.user); m != nil {
		agentType = m.AgentType
	}
	taskText := "No tasks on the board for you right now."
	if len(tasks) > 0 {
		label := "Your tasks"
		if t.leader() {
			label = "Team tasks"
		}
		taskText = label + " right now:\n" + taskLines(tasks)
	}
	return spawn.BuildPrompt(spawn.PromptParams{AgentName: t.agent, AgentID: id.AgentID, AgentType: agentType,
		Team: t.team, Leader: leaderOf(team), User: t.user, Workspace: cwd, TeamPrompt: prompt, Task: agentInstructions}) + "\n## Current Tasks\n\n" + taskText + "\n", nil
}

func (a *app) hookSettingsCmd() *cobra.Command {
	var t hookTarget
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Print Claude Code settings that install the team-worker hooks",
		Long: "Print the --settings JSON that spawn passes to Claude Code, so a session you start\n" +
			"yourself can take part in a team, for example as its leader:\n" +
			"  export TEAM_WORKER_AGENT_NAME=leader TEAM_WORKER_TEAM_NAME=demo\n" +
			"  claude --settings \"$(team-worker hook settings --role leader)\"",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !t.resolve() {
				return a.fail(fmt.Errorf("set --team and --agent (or TEAM_WORKER_TEAM_NAME and TEAM_WORKER_AGENT_NAME)"))
			}
			fmt.Fprintln(a.out, agent.ClaudeHookSettings(workerBinary(), t.team, t.agent, t.leader()))
			return nil
		},
	}
	t.flags(cmd)
	return cmd
}
