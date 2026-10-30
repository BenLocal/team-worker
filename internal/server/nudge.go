package server

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/benlocal/team-worker/internal/agent"
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
// model.AgentActivity): the Stop hook knows when it let the turn end. Only
// agents whose CLI reports nothing, e.g. one without hooks, are judged by
// reading their screen.
type Nudger struct {
	Store *store.Store
	Log   *slog.Logger
	// Capture and Type reach the agent's pane; tests replace them.
	Capture func(pane string) (string, error)
	Type    func(pane, text string) error
	Now     func() time.Time
	// IdleFor is how long an agent must wait with unread messages (or, read
	// from the screen, keep it unchanged) before a nudge. It leaves time for
	// a CLI extension that wakes the agent itself, as pi's does.
	IdleFor time.Duration
	// MinGap separates nudges for new messages; Repeat re-sends a reminder
	// for messages that are still unread.
	MinGap time.Duration
	Repeat time.Duration

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
}

func NewNudger(st *store.Store, log *slog.Logger) *Nudger {
	return &Nudger{
		Store:   st,
		Log:     log,
		Capture: func(pane string) (string, error) { return tmux.Capture(pane, 0) },
		Type:    tmux.Paste,
		Now:     time.Now,
		IdleFor: 8 * time.Second,
		MinGap:  20 * time.Second,
		Repeat:  5 * time.Minute,
		state:   map[string]*nudgeState{},
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
		if a.Activity == model.ActivityWaiting {
			count, newest, err = n.Store.InboxStats(ctx, a.Team, a.Name)
		}
		if a.Activity != model.ActivityWaiting || err != nil || count == 0 {
			st.waitingSince = time.Time{}
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
		if count, newest, err = n.Store.InboxStats(ctx, a.Team, a.Name); err != nil || count == 0 {
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

// screenIdle judges an agent whose CLI reports no activity by its screen:
// unchanged for IdleFor and showing an input prompt rather than a menu.
func (n *Nudger) screenIdle(a model.Agent, st *nudgeState, now time.Time) bool {
	screen, err := n.Capture(a.Target)
	if err != nil {
		return false
	}
	if screen != st.screen {
		st.screen, st.stableSince = screen, now
		return false
	}
	// A working CLI redraws its spinner; a menu or permission question has no
	// input prompt. Typing into either would be wrong, so wait for a quiet prompt.
	return now.Sub(st.stableSince) >= n.IdleFor && agent.InputPromptVisible(screen)
}
