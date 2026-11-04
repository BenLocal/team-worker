package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

// AnnounceInterval is how often the Announcer looks for new unassigned tasks.
const AnnounceInterval = 2 * time.Second

// Announcer tells each team's leader about pending tasks nobody owns that
// appeared since its last look. The message goes to the leader's inbox, so a
// leader idle at its prompt is woken by the Deliverer like for any message;
// hooks only report new work when a turn ends. Tasks present when a team is
// first seen are not announced: the leader reads the task list on start.
type Announcer struct {
	Store *store.Store
	Log   *slog.Logger
	// seen holds each team's unassigned pending task IDs from the last round.
	seen map[string]map[string]bool
}

func NewAnnouncer(st *store.Store, log *slog.Logger) *Announcer {
	return &Announcer{Store: st, Log: log, seen: map[string]map[string]bool{}}
}

// Run announces new tasks every AnnounceInterval until ctx ends.
func (a *Announcer) Run(ctx context.Context) {
	ticker := time.NewTicker(AnnounceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.Tick(ctx)
		}
	}
}

// Tick looks at every team once, with one query for the tasks and, when
// there is something to announce, one for the leaders.
func (a *Announcer) Tick(ctx context.Context) {
	teams, err := a.Store.ListTeams(ctx)
	if err != nil {
		a.Log.Warn("announce: list teams", "err", err)
		return
	}
	tasks, err := a.Store.UnassignedPending(ctx)
	if err != nil {
		a.Log.Warn("announce: list tasks", "err", err)
		return
	}
	next := map[string]map[string]bool{}
	fresh := map[string][]model.Task{}
	for _, t := range teams {
		seen, known := a.seen[t.Name]
		pending := map[string]bool{}
		for _, task := range tasks[t.Name] {
			pending[task.ID] = true
			if known && !seen[task.ID] {
				fresh[t.Name] = append(fresh[t.Name], task)
			}
		}
		next[t.Name] = pending
	}
	if len(fresh) > 0 {
		leaders, err := a.Store.Leaders(ctx)
		if err != nil {
			// Keep last round's view, so these tasks count as new next time.
			a.Log.Warn("announce: list leaders", "err", err)
			return
		}
		for team, list := range fresh {
			if err := a.announce(ctx, team, leaders[team], list); err != nil {
				a.Log.Warn("announce: new tasks", "team", team, "err", err)
				// Retried next round: the team keeps its previous view.
				next[team] = a.seen[team]
			}
		}
	}
	// Teams that are gone drop out with the new view.
	a.seen = next
}

func (a *Announcer) announce(ctx context.Context, team, leader string, tasks []model.Task) error {
	if leader == "" {
		return fmt.Errorf("team has no members to tell")
	}
	lines := []string{"New unassigned pending tasks:"}
	for _, t := range tasks {
		lines = append(lines, fmt.Sprintf("- %s [%s] %s", t.ID, t.Priority, t.Subject))
	}
	lines = append(lines, fmt.Sprintf("Check `team-worker task list %s --status pending`, then assign, split or take them.", team))
	_, err := a.Store.Send(ctx, team, store.SendInput{From: "team-worker", To: leader, Type: "message", Content: strings.Join(lines, "\n")})
	return err
}
