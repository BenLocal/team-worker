package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

// registerElsewhere records a running tmux agent that a CLI on another machine
// started; its pane id must never be used on this machine.
func registerElsewhere(t *testing.T, h *harness, team, name string) {
	t.Helper()
	h.json(new(map[string]any), "team", "add-member", team, name)
	if _, err := h.api.RegisterAgent(context.Background(), model.Agent{Team: team, Name: name, Backend: "tmux",
		Target: "%999", RunID: "run-1", Host: "other-machine.example"}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentOnAnotherMachineIsNotTouchedLocally(t *testing.T) {
	h := newHarness(t)
	h.json(new(map[string]any), "team", "spawn-team", "demo")
	registerElsewhere(t, h, "demo", "alice")

	out, ok := h.run("--json", "agent", "screen", "demo", "alice")
	if ok || !strings.Contains(out, "runs on host 'other-machine.example'") {
		t.Fatalf("screen = %v %q", ok, out)
	}

	var stop map[string]string
	h.json(&stop, "agent", "stop", "demo", "alice")
	if stop["status"] != "stop_requested" || stop["host"] != "other-machine.example" {
		t.Fatalf("stop = %v", stop)
	}
	ag, err := h.api.GetAgent(context.Background(), "demo", "alice")
	if err != nil {
		t.Fatal(err)
	}
	// Only the request is recorded; the agent's own supervisor ends it and reports the exit.
	if !ag.StopRequested || ag.Status != model.AgentRunning {
		t.Fatalf("agent = %+v", ag)
	}

	out, ok = h.run("--json", "spawn", "tmux", "--team", "demo", "-n", "alice", "--replace", "--", "sh", "-c", "true")
	if ok || !strings.Contains(out, "runs on host 'other-machine.example'") {
		t.Fatalf("spawn --replace = %v %q", ok, out)
	}
}

func TestCleanupSkipsAgentsOnAnotherMachine(t *testing.T) {
	h := newHarness(t)
	h.json(new(map[string]any), "team", "spawn-team", "demo")
	registerElsewhere(t, h, "demo", "alice")
	if out, ok := h.run("--json", "team", "cleanup", "demo", "--force"); !ok {
		t.Fatalf("cleanup failed: %s", out)
	}
	if _, err := h.api.GetTeam(context.Background(), "demo"); err == nil {
		t.Fatal("team still exists")
	}
}

func TestWatchStopEndsChildOnRequest(t *testing.T) {
	old := stopPollInterval
	stopPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { stopPollInterval = old })
	for _, rec := range []model.Agent{{RunID: "run-1", StopRequested: true}, {RunID: "run-2"}} {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go watchStop(ctx, done, cancel, func() (model.Agent, error) { return rec, nil }, "run-1")
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("%+v: child was not cancelled", rec)
		}
		close(done)
	}
	// A running record that still owns the run keeps the child alive.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go watchStop(ctx, done, cancel, func() (model.Agent, error) { return model.Agent{RunID: "run-1"}, nil }, "run-1")
	time.Sleep(50 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("child cancelled without a stop request")
	}
	close(done)
}
