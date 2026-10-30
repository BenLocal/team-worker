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
type Nudger struct {
	Store *store.Store
	Log   *slog.Logger
	// Capture and Type reach the agent's pane; tests replace them.
	Capture func(pane string) (string, error)
	Type    func(pane, text string) error
	Now     func() time.Time
	// IdleFor is how long the screen must stay unchanged before a nudge.
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
	nudgedAt    time.Time
	nudgedID    int64
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
	screen, err := n.Capture(a.Target)
	if err != nil {
		return
	}
	if screen != st.screen {
		st.screen, st.stableSince = screen, now
		return
	}
	count, newest, err := n.Store.InboxStats(ctx, a.Team, a.Name)
	if err != nil || count == 0 {
		return
	}
	// A working CLI redraws its spinner; a menu or permission question has no
	// input prompt. Typing into either would be wrong, so wait for a quiet prompt.
	if now.Sub(st.stableSince) < n.IdleFor || !agent.InputPromptVisible(screen) {
		return
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
