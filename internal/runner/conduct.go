package runner

import (
	"context"
	"log/slog"
	"time"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/harness"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

// Conductor drives one team's running harness run: each tick reloads the
// run, starts the agents its phase needs, stops those of finished phases
// and advances when the gate opens. It keeps no state of its own, so a
// conductor started again after a crash, or someone approving, advancing or
// aborting the run in between, is no problem.
type Conductor struct {
	Client *client.Client
	Team   string
	Poll   time.Duration
	Log    *slog.Logger
	// Spawn starts one role agent and Stop ends one; NewConductor sets them
	// to the daemon's own spawn and stop, tests replace them.
	Spawn func(ctx context.Context, team, name, role, cli, prompt string) error
	Stop  func(ctx context.Context, team, name string) error
}

// NewConductor conducts team's run with the daemon's spawn and stop: role
// agents get the same identity, workspace, hooks and keepalive as any
// spawned agent.
func NewConductor(e Env, team string, poll time.Duration, log *slog.Logger) *Conductor {
	return &Conductor{Client: e.Client, Team: team, Poll: poll, Log: log,
		Spawn: func(ctx context.Context, team, name, role, cli, prompt string) error {
			req := Request{Team: team, Name: name, AgentType: role, Backend: "tmux", KeepAlive: true,
				Task: prompt, TaskSet: true, Command: []string{cli}, SkipPerms: true}
			if err := ApplyProfile(&req, ""); err != nil {
				return err
			}
			_, err := Spawn(ctx, e, req)
			return err
		},
		Stop: func(ctx context.Context, team, name string) error {
			_, err := e.Client.TerminateAgent(ctx, team, name)
			return err
		},
	}
}

// Run ticks until the run ends or ctx is cancelled, which leaves the run
// running for a later conductor.
func (cd *Conductor) Run(ctx context.Context) {
	for {
		done, err := cd.Tick(ctx)
		if err != nil && ctx.Err() == nil {
			// The daemon may be busy; try again on the next poll.
			cd.Log.Warn("harness conductor", "team", cd.Team, "err", err)
		}
		if done {
			return
		}
		select {
		case <-ctx.Done():
			cd.Log.Info("harness conductor stopped; the run keeps running", "team", cd.Team)
			return
		case <-time.After(cd.Poll):
		}
	}
}

// Tick does one round and reports whether the run has ended.
func (cd *Conductor) Tick(ctx context.Context) (bool, error) {
	c := cd.Client
	st, err := c.HarnessStatus(ctx, cd.Team, "")
	if err != nil {
		return false, err
	}
	if st.Status != model.HarnessRunning {
		stopped := cd.stopAgents(ctx, st.Agents, "")
		cd.Log.Info("harness run ended", "team", cd.Team, "run", st.ID, "status", st.Status, "phase", st.Phase, "stopped", len(stopped))
		return true, nil
	}
	role := model.HarnessPhaseRole[st.Phase]
	for _, name := range cd.stopAgents(ctx, st.Agents, role) {
		cd.Log.Info("harness agent stopped: its phase is over", "team", cd.Team, "agent", name)
	}
	if role != "" {
		want := 1
		if role == model.RoleExecutor {
			want = st.AgentCount
		}
		live := 0
		for _, ag := range st.Agents {
			if ag.Role == role && cd.alive(ctx, ag.Name) {
				live++
			}
		}
		for ; live < want; live++ {
			name := role + "-" + store.NewAgentID()[:6]
			// Record first, so a conductor started after a crash counts this agent.
			if _, err := c.AddHarnessAgent(ctx, cd.Team, st.ID, store.HarnessAgentInput{Name: name, Role: role, Phase: st.Phase}); err != nil {
				return false, err
			}
			prompt := harness.RolePrompt(harness.PromptParams{Team: cd.Team, RunID: st.ID, Goal: st.Goal, Phase: st.Phase,
				Role: role, Agent: name, Attempt: st.Attempt})
			if err := cd.Spawn(ctx, cd.Team, name, role, st.CLI, prompt); err != nil {
				return false, err
			}
			cd.Log.Info("harness agent started", "team", cd.Team, "role", role, "agent", name, "phase", st.Phase)
		}
	}
	if !st.CanAdvance {
		return false, nil
	}
	out, err := c.AdvanceHarness(ctx, cd.Team, st.ID)
	if err != nil {
		return false, err
	}
	cd.Log.Info("harness advanced", "team", cd.Team, "run", out.ID, "from", out.From, "phase", out.Phase,
		"status", out.Status, "rolled_back", out.RolledBack)
	return false, nil
}

// stopAgents stops the run's live agents whose role is not keep.
func (cd *Conductor) stopAgents(ctx context.Context, agents []model.HarnessAgent, keep string) []string {
	stopped := []string{}
	for _, ag := range agents {
		if ag.Role == keep || !cd.alive(ctx, ag.Name) {
			continue
		}
		if err := cd.Stop(ctx, cd.Team, ag.Name); err != nil {
			cd.Log.Warn("harness agent stop failed", "team", cd.Team, "agent", ag.Name, "err", err)
			continue
		}
		stopped = append(stopped, ag.Name)
	}
	return stopped
}

func (cd *Conductor) alive(ctx context.Context, name string) bool {
	ag, err := cd.Client.GetAgent(ctx, cd.Team, name)
	return err == nil && ag.Status == model.AgentRunning && (ag.Alive == nil || *ag.Alive)
}
