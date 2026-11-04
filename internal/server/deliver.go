package server

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
	"github.com/benlocal/team-worker/internal/tmux"
)

// Deliverer runs one goroutine per running tmux agent on this machine. Each
// takes the agent's new messages as soon as they reach its inbox and pastes
// them into the agent's pane, as ClawTeam's runtime watch does, without
// reading the screen. Only an agent whose hooks report it working is left to
// its hooks. A sender's messages are pasted at most once every
// Throttle; later ones stay in the inbox and go with the next paste, which
// also lets a hook that runs meanwhile take them.
type Deliverer struct {
	Store *store.Store
	Hub   *Hub
	Log   *slog.Logger
	// Paste and Alive reach the agent's pane; tests replace them.
	Paste func(pane, text string) error
	Alive func(pane string) bool
	Now   func() time.Time
	// Throttle separates pastes of one sender's messages. StartupGrace keeps
	// pastes away from an agent whose CLI may still be taking its first
	// prompt.
	Throttle     time.Duration
	StartupGrace time.Duration

	mu      sync.Mutex
	workers map[string]context.CancelFunc
	wg      sync.WaitGroup
}

func NewDeliverer(st *store.Store, hub *Hub, log *slog.Logger) *Deliverer {
	return &Deliverer{Store: st, Hub: hub, Log: log, Paste: tmux.Paste, Alive: paneAlive, Now: time.Now,
		Throttle: 30 * time.Second, StartupGrace: 15 * time.Second, workers: map[string]context.CancelFunc{}}
}

// DeliverInterval is how often the daemon looks for agents that started or
// stopped.
const DeliverInterval = 2 * time.Second

// Run keeps one goroutine per agent run until ctx ends, then waits for them.
func (d *Deliverer) Run(ctx context.Context) {
	defer d.wg.Wait()
	ticker := time.NewTicker(DeliverInterval)
	defer ticker.Stop()
	for {
		d.Sync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sync starts a goroutine for each new agent run and stops those of runs
// that ended. A run is keyed by its pane too, since a keepalive restart may
// reuse the run in another pane.
func (d *Deliverer) Sync(ctx context.Context) {
	agents, err := d.Store.RunningAgents(ctx)
	if err != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	want := map[string]model.Agent{}
	for _, a := range agents {
		if a.Nudge && a.Backend == "tmux" && a.Target != "" {
			want[a.Team+"/"+a.Name+"/"+a.RunID+"/"+a.Target] = a
		}
	}
	for key, cancel := range d.workers {
		if _, ok := want[key]; !ok {
			cancel()
			delete(d.workers, key)
		}
	}
	for key, a := range want {
		if _, ok := d.workers[key]; ok {
			continue
		}
		wctx, cancel := context.WithCancel(ctx)
		d.workers[key] = cancel
		d.wg.Go(func() { d.watch(wctx, a) })
	}
}

// watch delivers one agent run's messages whenever the store changes, and
// again when messages held back by the throttle or the startup grace are due.
func (d *Deliverer) watch(ctx context.Context, a model.Agent) {
	last := map[string]time.Time{}
	for {
		// Taken before reading the inbox, so a message sent meanwhile wakes us.
		changed, _ := d.Hub.Changed()
		retry := d.deliver(ctx, a, last)
		var timer *time.Timer
		var due <-chan time.Time
		if retry > 0 {
			timer = time.NewTimer(retry)
			due = timer.C
		}
		select {
		case <-ctx.Done():
		case <-changed:
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// deliver pastes a's due messages and returns when to look again for
// messages it held back; 0 means on the next change only.
func (d *Deliverer) deliver(ctx context.Context, run model.Agent, last map[string]time.Time) time.Duration {
	// Re-read the agent: its activity changes during the run, and each change
	// is a write that wakes the goroutine.
	a, err := d.Store.GetAgent(ctx, run.Team, run.Name)
	if err != nil || a.RunID != run.RunID || a.Target != run.Target {
		return 0
	}
	// Hooks hand messages to a working agent, at its next tool call or in
	// its Stop hook. A paste taken past a Stop hook that waits for messages
	// would sit unread until the hook times out, minutes later.
	if a.Activity == model.ActivityWorking {
		return 0
	}
	now := d.Now()
	if started, err := time.Parse(time.RFC3339Nano, a.StartedAt); err == nil {
		if wait := d.StartupGrace - now.Sub(started); wait > 0 {
			return wait
		}
	}
	waiting, err := d.Store.Peek(ctx, a.Team, a.Name, "")
	if err != nil || len(waiting) == 0 {
		return 0
	}
	var next time.Duration
	due := false
	for _, m := range waiting {
		if left := d.Throttle - now.Sub(last[m.From]); left <= 0 {
			due = true
		} else if next == 0 || left < next {
			next = left
		}
	}
	if !due {
		return next
	}
	// Taking messages for a pane that is gone would lose them.
	if !d.Alive(a.Target) {
		return time.Second
	}
	msgs, err := d.Store.Receive(ctx, a.Team, a.Name, "", 50)
	if err != nil || len(msgs) == 0 {
		return 0
	}
	if err := d.Paste(a.Target, model.FormatMessages(a.Team, msgs)); err != nil {
		d.Log.Warn("message paste failed", "team", a.Team, "agent", a.Name, "messages", len(msgs), "err", err)
		return 0
	}
	d.Log.Info("pasted messages", "team", a.Team, "agent", a.Name, "messages", len(msgs))
	for _, m := range msgs {
		last[m.From] = now
	}
	return 0
}

func paneAlive(pane string) bool {
	exists, dead, _ := tmux.PaneState(pane)
	return exists && !dead
}
