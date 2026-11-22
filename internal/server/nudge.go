package server

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
	"github.com/benlocal/team-worker/internal/tmux"
)

// Nudger reminds a worker that has stalled: it sits at its prompt with no
// unread messages but an in-progress task, because its turn ended without
// the Stop hook, which would have kept it working, e.g. on a model API error.
// It gets stallReminders reminders typed into its tmux pane, then the leader
// is told once. Messages themselves reach agents through the Deliverer.
//
// Whether the agent waits at its prompt comes from its hooks (see
// model.AgentActivity). Agents whose CLI reports nothing, e.g. Codex or dsh,
// are taken to wait once their screen stops changing; the screen is not
// parsed.
type Nudger struct {
	Store *store.Store
	Log   *slog.Logger
	// Screen, Title and Type reach the agent's pane; tests replace them.
	Screen func(pane string) (string, error)
	Title  func(pane string) (string, error)
	Type   func(pane, text string) error
	Now    func() time.Time
	// IdleFor is how long a screen must stay unchanged for an agent without
	// hook reports to count as waiting.
	IdleFor time.Duration
	// StallAfter is how long a worker with an in-progress task must sit at
	// its prompt before a reminder; StallRepeat separates the reminders and
	// the final report to the leader.
	StallAfter  time.Duration
	StallRepeat time.Duration

	mu    sync.Mutex
	state map[string]*nudgeState
}

type nudgeState struct {
	screen      string
	stableSince time.Time
	// idleSince is when a hook-reporting agent was first seen waiting.
	idleSince time.Time
	// stallTasks names the in-progress tasks the stall reminders are about;
	// a different set starts the reminders over.
	stallTasks    string
	stallNudges   int
	stallAt       time.Time
	stallReported bool
	// blockedSince is when the agent was first seen on a question or
	// permission prompt; blockedReported is set once the leader was told.
	blockedSince    time.Time
	blockedReported bool
}

// stallReminders is how many reminders a stalled worker gets before the
// leader is told.
const stallReminders = 2

func NewNudger(st *store.Store, log *slog.Logger) *Nudger {
	return &Nudger{
		Store:       st,
		Log:         log,
		Screen:      tmux.Screen,
		Title:       tmux.Title,
		Type:        tmux.Paste,
		Now:         time.Now,
		IdleFor:     8 * time.Second,
		StallAfter:  10 * time.Minute,
		StallRepeat: 30 * time.Minute,
		state:       map[string]*nudgeState{},
	}
}

// NudgeInterval is how often the daemon checks idle agents.
const NudgeInterval = 2 * time.Second

func (n *Nudger) Run(ctx context.Context) {
	ticker := time.NewTicker(NudgeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.Tick(ctx)
		}
	}
}

// Tick checks every running agent once.
func (n *Nudger) Tick(ctx context.Context) {
	agents, err := n.Store.RunningAgents(ctx)
	if err != nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	seen := map[string]bool{}
	for _, a := range agents {
		if !a.Nudge || a.Backend != "tmux" {
			continue
		}
		key := a.Team + "/" + a.Name
		seen[key] = true
		n.check(ctx, a, key)
	}
	for key := range n.state {
		if !seen[key] {
			delete(n.state, key)
		}
	}
}

func (n *Nudger) check(ctx context.Context, a model.Agent, key string) {
	st := n.state[key]
	if st == nil {
		st = &nudgeState{}
		n.state[key] = st
	}
	now := n.Now()
	if a.Activity != model.ActivityBlocked {
		st.blockedSince, st.blockedReported = time.Time{}, false
	}
	var idle time.Duration
	if a.Activity == model.ActivityBlocked {
		st.idleSince = time.Time{}
		n.checkBlocked(ctx, a, st, now)
		return
	}
	if a.Activity != "" {
		// A working agent is busy, and typing into a blocked one would answer
		// its question; only a waiting one can have stalled.
		if a.Activity != model.ActivityWaiting {
			st.idleSince = time.Time{}
			return
		}
		if st.idleSince.IsZero() {
			st.idleSince = now
		}
		idle = now.Sub(st.idleSince)
	} else {
		if !n.screenIdle(a, st, now) {
			return
		}
		idle = now.Sub(st.stableSince)
	}
	// Unread messages are the Deliverer's to paste; they wake the agent.
	if count, _, err := n.Store.InboxStats(ctx, a.Team, a.Name); err != nil || count > 0 {
		return
	}
	n.checkStall(ctx, a, st, idle, now)
}

// checkStall reminds a worker that has sat at its prompt for idle, with no
// unread messages, that its in-progress tasks are still open. After
// stallReminders reminders StallRepeat apart it tells the leader once.
func (n *Nudger) checkStall(ctx context.Context, a model.Agent, st *nudgeState, idle time.Duration, now time.Time) {
	if idle < n.StallAfter {
		return
	}
	leader, err := n.Store.Leader(ctx, a.Team)
	if err != nil {
		return
	}
	if leader.Name == a.Name {
		n.checkCoordination(ctx, a, st, now)
		return
	}
	tasks, err := n.Store.ListTasks(ctx, a.Team, store.TaskFilter{Owner: a.Name})
	if err != nil {
		return
	}
	executions := tasks[:0]
	for _, task := range tasks {
		if task.Kind != "goal" && task.Status != model.StatusCompleted && task.Status != model.StatusSuperseded && task.ExecutionState != "waiting" && !(task.Status == model.StatusBlocked && len(task.BlockedBy) > 0) {
			executions = append(executions, task)
		}
	}
	tasks = executions
	ids := make([]string, len(tasks))
	lines := make([]string, len(tasks))
	for i, task := range tasks {
		ids[i] = task.ID
		lines[i] = fmt.Sprintf("%s %q", task.ID, task.Subject)
	}
	if set := strings.Join(ids, ","); set != st.stallTasks {
		st.stallTasks, st.stallNudges, st.stallAt, st.stallReported = set, 0, time.Time{}, false
	}
	if len(tasks) == 0 || st.stallReported || (st.stallNudges > 0 && now.Sub(st.stallAt) < n.StallRepeat) {
		return
	}

	taskList := strings.Join(lines, "; ")
	if st.stallNudges < stallReminders {
		text := fmt.Sprintf("Your open execution task(s) in team %s need follow-up: %s. Continue them; if you are waiting "+
			"on someone or a future event, record `task progress --state waiting --reason ... --next-action ...` with its registered checkpoint or request; otherwise report the current stage and continue authorized work. Tell the leader with "+
			"`team-worker inbox send %s %s \"...\"`.", a.Team, taskList, a.Team, leader.Name)
		if err := n.Type(a.Target, text); err != nil {
			n.Log.Warn("stall reminder failed", "team", a.Team, "agent", a.Name, "err", err)
			return
		}
		n.Log.Info("reminded stalled agent", "team", a.Team, "agent", a.Name, "tasks", st.stallTasks)
		st.stallNudges, st.stallAt = st.stallNudges+1, now
		st.screen = ""
		return
	}
	content := fmt.Sprintf("%s has sat idle at its prompt for %s with open execution task(s) %s and did not react "+
		"to %d reminders. Check it with `team-worker agent screen %s %s`, then message it or reassign the task.",
		a.Name, idle.Round(time.Minute), taskList, stallReminders, a.Team, a.Name)
	if _, err := n.Store.Send(ctx, a.Team, store.SendInput{From: "team-worker", To: leader.Name, Content: content}); err != nil {
		n.Log.Warn("stall report failed", "team", a.Team, "agent", a.Name, "err", err)
		return
	}
	n.Log.Info("reported stalled agent to the leader", "team", a.Team, "agent", a.Name, "tasks", st.stallTasks)
	st.stallReported = true
}

// checkCoordination asks an idle leader to account for executable or unexplained work.
// Unreviewed waits need a scope review; reviewed waits and completed batches may rest.
func (n *Nudger) checkCoordination(ctx context.Context, a model.Agent, st *nudgeState, now time.Time) {
	agents, err := n.Store.ListAgents(ctx, a.Team)
	if err != nil {
		return
	}
	for _, peer := range agents {
		if peer.Name != a.Name && peer.Status == model.AgentRunning && peer.Activity == model.ActivityWorking {
			return
		}
	}
	tasks, err := n.Store.ListTasks(ctx, a.Team, store.TaskFilter{})
	if err != nil {
		return
	}
	owners := map[string]bool{}
	issues := []string{}
	for _, task := range tasks {
		if task.Kind == "goal" || task.Status == model.StatusCompleted || task.Status == model.StatusSuperseded {
			continue
		}
		if task.NeedsWaitReview() {
			issues = append(issues, task.ID+" @"+task.Owner+" waiting scope needs review (version "+model.TaskWaitVersion(task)+")")
		}
		if task.Owner == "" && task.Status == model.StatusPending {
			issues = append(issues, task.ID+" unassigned")
		} else {
			owners[task.Owner] = true
		}
	}
	for owner := range owners {
		work := model.SummarizeMemberWork(tasks, owner, now)
		if work.State != "needs_review" && work.State != "assigned" {
			continue
		}
		for _, task := range tasks {
			if task.Owner == owner && task.Kind != "goal" && task.Status != model.StatusCompleted && task.Status != model.StatusSuperseded && !(task.Status == model.StatusBlocked && len(task.BlockedBy) > 0) && task.ExecutionState != "waiting" {
				issues = append(issues, task.ID+" @"+owner)
			}
		}
	}
	sort.Strings(issues)
	signature := "coord:" + strings.Join(issues, ",")
	if len(issues) == 0 {
		st.stallTasks = ""
		st.stallNudges = 0
		st.stallReported = false
		return
	}
	if signature != st.stallTasks {
		st.stallTasks = signature
		st.stallNudges = 0
		st.stallAt = time.Time{}
		st.stallReported = false
	}
	if st.stallReported {
		return
	}
	if st.stallNudges == 0 {
		text := fmt.Sprintf("Idle coordination review for team %s: %s. For each waiting scope, inspect the remaining deliverables and evidence, split out independent authorized work, then use `team-worker task review-wait` with the current wait version and review evidence only if the remaining work truly needs the trigger. Merely filling in waiting fields does not complete this review. Check whether authorized independent work can proceed. Before ending the turn, record each open task's stage or waiting reason, next action and registered checkpoint/request with `team-worker task progress`. Respect scheduled windows; do not run future observations early. Members with completed batches may remain on standby.", a.Team, strings.Join(issues, "; "))
		if err := n.Type(a.Target, text); err != nil {
			n.Log.Warn("coordination reminder failed", "team", a.Team, "err", err)
			return
		}
		st.stallNudges = 1
		st.stallAt = now
		return
	}
	if now.Sub(st.stallAt) < n.StallRepeat {
		return
	}
	content := fmt.Sprintf("Idle coordination remains unresolved in team %s after a reminder: %s. The leader has not accounted for these open assignments; inspect progress and waiting records before changing execution.", a.Team, strings.Join(issues, "; "))
	if _, err := n.Store.Send(ctx, a.Team, store.SendInput{From: "team-worker", To: store.OperatorInbox, Content: content}); err != nil {
		n.Log.Warn("coordination escalation failed", "team", a.Team, "err", err)
		return
	}
	st.stallReported = true
}

// checkBlocked tells the leader once when an agent has sat on a question or
// permission prompt for StallAfter: nothing typed reaches it there, and only
// someone at its terminal can answer. A blocked leader is escalated to the
// operator inbox so a human can intervene.
func (n *Nudger) checkBlocked(ctx context.Context, a model.Agent, st *nudgeState, now time.Time) {
	if st.blockedSince.IsZero() {
		st.blockedSince = now
	}
	if st.blockedReported || now.Sub(st.blockedSince) < n.StallAfter {
		return
	}
	leader, err := n.Store.Leader(ctx, a.Team)
	if err != nil {
		return
	}
	if leader.Name == a.Name {
		content := fmt.Sprintf("%s (team leader) has been stopped at a question or permission prompt for %s. Look at it with "+
			"`team-worker agent screen %s %s`; someone at its terminal has to answer, or restart it.",
			a.Name, now.Sub(st.blockedSince).Round(time.Minute), a.Team, a.Name)
		if _, err := n.Store.Send(ctx, a.Team, store.SendInput{From: "team-worker", To: store.OperatorInbox, Content: content}); err != nil {
			n.Log.Warn("blocked leader report failed", "team", a.Team, "agent", a.Name, "err", err)
			return
		}
		n.Log.Info("reported blocked leader to operator", "team", a.Team, "agent", a.Name)
		st.blockedReported = true
		return
	}
	content := fmt.Sprintf("%s has been stopped at a question or permission prompt for %s. Look at it with "+
		"`team-worker agent screen %s %s`; someone at its terminal has to answer, or restart it.",
		a.Name, now.Sub(st.blockedSince).Round(time.Minute), a.Team, a.Name)
	if _, err := n.Store.Send(ctx, a.Team, store.SendInput{From: "team-worker", To: leader.Name, Content: content}); err != nil {
		n.Log.Warn("blocked report failed", "team", a.Team, "agent", a.Name, "err", err)
		return
	}
	n.Log.Info("reported blocked agent to the leader", "team", a.Team, "agent", a.Name)
	st.blockedReported = true
}

// screenIdle judges an agent whose CLI reports no activity by its screen:
// a working CLI redraws its progress, so a screen unchanged for IdleFor means
// the agent is waiting.
func (n *Nudger) screenIdle(a model.Agent, st *nudgeState, now time.Time) bool {
	screen, err := n.Screen(a.Target)
	if err != nil {
		return false
	}
	title, _ := n.Title(a.Target)
	if seen := screen + "\x00" + title; seen != st.screen {
		st.screen, st.stableSince = seen, now
		return false
	}
	return now.Sub(st.stableSince) >= n.IdleFor
}
