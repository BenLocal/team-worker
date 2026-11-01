package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/benlocal/team-worker/internal/agent"
	"github.com/benlocal/team-worker/internal/agent/builtin"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/store"
	"github.com/benlocal/team-worker/internal/tmux"
)

// Nudger types a one-line reminder into an agent's tmux pane when messages
// wait in its inbox while the agent sits idle at its input prompt. Hooks
// deliver messages while an agent works; this wakes an agent whose turn has
// already ended, which no hook can reach.
//
// Whether the agent waits at its prompt comes from its hooks (see
// model.AgentActivity): the Stop hook knows when it let the turn end. Agents
// whose CLI reports nothing, e.g. Codex without hooks, are judged by their
// adapter's screen rules; an adapter without screen rules is never typed
// into, since a guess could answer a menu.
//
// A worker that sits at its prompt with no unread messages but an
// in-progress task has stalled: its turn ended without the Stop hook, which
// would have kept it working, e.g. on a model API error. It gets
// stallReminders reminders, then the leader is told once.
type Nudger struct {
	Store *store.Store
	Log   *slog.Logger
	// Screen, Title and Type reach the agent's pane; tests replace them.
	Screen func(pane string) (string, error)
	Title  func(pane string) (string, error)
	Type   func(pane, text string) error
	// Adapter returns the adapter that runs an agent's command.
	Adapter func(command []string) agent.Agent
	Now     func() time.Time
	// IdleFor is how long an agent must wait with unread messages (or, read
	// from the screen, keep it unchanged) before a nudge. It leaves time for
	// a CLI extension that wakes the agent itself, as pi's does.
	IdleFor time.Duration
	// MinGap separates nudges for new messages; Repeat re-sends a reminder
	// for messages that are still unread.
	MinGap time.Duration
	Repeat time.Duration
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
	// waitingSince is when a hook-reporting agent was first seen waiting
	// with unread messages.
	waitingSince time.Time
	nudgedAt     time.Time
	nudgedID     int64
	// idleSince is when a hook-reporting agent was first seen waiting,
	// with or without messages.
	idleSince time.Time
	// stallTasks names the in-progress tasks the stall reminders are about;
	// a different set starts the reminders over.
	stallTasks    string
	stallNudges   int
	stallAt       time.Time
	stallReported bool
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
		Adapter:     adapters(log).For,
		Type:        tmux.Paste,
		Now:         time.Now,
		IdleFor:     8 * time.Second,
		MinGap:      20 * time.Second,
		Repeat:      5 * time.Minute,
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
		// A pane id from another machine could name an unrelated pane here.
		if !a.Nudge || a.Backend != "tmux" || !proc.OnServer(a) {
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
	var count int
	var newest int64
	var err error
	if a.Activity != "" {
		// A working agent gets its messages from hooks, and typing into a
		// blocked one would answer its question; only a waiting one is woken.
		if a.Activity != model.ActivityWaiting {
			st.waitingSince, st.idleSince = time.Time{}, time.Time{}
			return
		}
		if st.idleSince.IsZero() {
			st.idleSince = now
		}
		count, newest, err = n.Store.InboxStats(ctx, a.Team, a.Name)
		if err != nil || count == 0 {
			st.waitingSince = time.Time{}
			if err == nil {
				n.checkStall(ctx, a, st, now.Sub(st.idleSince), now)
			}
			return
		}
		if st.waitingSince.IsZero() {
			st.waitingSince = now
		}
		if now.Sub(st.waitingSince) < n.IdleFor {
			return
		}
	} else {
		if !n.screenIdle(a, st, now) {
			return
		}
		if count, newest, err = n.Store.InboxStats(ctx, a.Team, a.Name); err != nil {
			return
		}
		if count == 0 {
			n.checkStall(ctx, a, st, now.Sub(st.stableSince), now)
			return
		}
	}
	fresh := newest > st.nudgedID && now.Sub(st.nudgedAt) >= n.MinGap
	if !fresh && now.Sub(st.nudgedAt) < n.Repeat {
		return
	}
	text := fmt.Sprintf("You have %d new team-worker message(s) in team %s. Run `team-worker inbox receive %s` "+
		"and handle them.", count, a.Team, a.Team)
	if err := n.Type(a.Target, text); err != nil {
		n.Log.Warn("nudge failed", "team", a.Team, "agent", a.Name, "err", err)
		return
	}
	n.Log.Info("nudged agent", "team", a.Team, "agent", a.Name, "messages", count)
	st.nudgedAt, st.nudgedID = now, newest
	// The reminder changes the screen; start the idle clock over.
	st.screen = ""
}

// checkStall reminds a worker that has sat at its prompt for idle, with no
// unread messages, that its in-progress tasks are still open. After
// stallReminders reminders StallRepeat apart it tells the leader once.
func (n *Nudger) checkStall(ctx context.Context, a model.Agent, st *nudgeState, idle time.Duration, now time.Time) {
	if idle < n.StallAfter {
		return
	}
	tasks, err := n.Store.ListTasks(ctx, a.Team, store.TaskFilter{Status: model.StatusInProgress, Owner: a.Name})
	if err != nil {
		return
	}
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
	// The leader waits at its prompt while its workers run; that is no stall.
	leader, err := n.Store.Leader(ctx, a.Team)
	if err != nil || leader.Name == a.Name {
		return
	}
	taskList := strings.Join(lines, "; ")
	if st.stallNudges < stallReminders {
		text := fmt.Sprintf("Your task(s) in team %s are still in_progress: %s. Continue them; if you are waiting "+
			"on someone or cannot go on, mark the task blocked or tell the leader with "+
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
	content := fmt.Sprintf("%s has sat idle at its prompt for %s with in-progress task(s) %s and did not react "+
		"to %d reminders. Check it with `team-worker agent screen %s %s`, then message it or reassign the task.",
		a.Name, idle.Round(time.Minute), taskList, stallReminders, a.Team, a.Name)
	if _, err := n.Store.Send(ctx, a.Team, store.SendInput{From: "team-worker", To: leader.Name, Content: content}); err != nil {
		n.Log.Warn("stall report failed", "team", a.Team, "agent", a.Name, "err", err)
		return
	}
	n.Log.Info("reported stalled agent to the leader", "team", a.Team, "agent", a.Name, "tasks", st.stallTasks)
	st.stallReported = true
}

// screenIdle judges an agent whose CLI reports no activity by its screen:
// unchanged for IdleFor, and its adapter's rules see the input prompt itself.
// The rules' idle fallback (no rule matched) is not enough: an unknown menu
// would look the same.
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
	if now.Sub(st.stableSince) < n.IdleFor {
		return false
	}
	d, ok := n.Adapter(a.Command).DetectScreen(agent.ScreenInput{Screen: screen, Title: title})
	return ok && d.State == agent.ScreenIdle && d.VisibleIdle && !d.SkipStateUpdate
}

// adapters loads the agent adapters once for the daemon; a broken user
// adapter file falls back to the built-in adapters.
func adapters(log *slog.Logger) *agent.Registry {
	r, err := builtin.Load(config.AgentsDir())
	if err != nil {
		log.Warn("agent adapters: using the built-in ones", "err", err)
		return builtin.Registry()
	}
	return r
}
