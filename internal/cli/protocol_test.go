package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestAgentProtocolReadAckAndRoleContext(t *testing.T) {
	h := newHarness(t)
	must := func(args ...string) string {
		t.Helper()
		out, ok := h.run(args...)
		if !ok {
			t.Fatalf("%v failed: %s", args, out)
		}
		return out
	}
	must("team", "spawn-team", "--no-start", "demo")
	must("team", "add-member", "demo", "alice")
	must("team", "prompt", "set", "demo", "Shared bounded scope")
	must("agent", "prompt", "set", "demo", "alice", "Preserve delivered evidence")
	a, err := h.api.RegisterAgent(context.Background(), model.Agent{Team: "demo", Name: "alice", RunID: "run-one", PID: 123})
	if err != nil {
		t.Fatal(err)
	}
	out := must("--json", "agent", "protocol", "show", "demo", "alice", "--run-id", "")
	var p model.ProtocolContext
	if err = json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	if p.State != nil || !strings.Contains(p.Content, "Worker Loop Protocol") || !strings.Contains(p.Content, "Shared bounded scope") || !strings.Contains(p.Content, "Preserve delivered evidence") {
		t.Fatalf("bad inspection context: %+v", p)
	}
	if _, ok := h.run("agent", "protocol", "ack", "demo", "alice", "--run-id", a.RunID, "--version", p.Version); ok {
		t.Fatal("ack accepted without reading")
	}
	must("agent", "protocol", "show", "demo", "alice", "--run-id", a.RunID)
	if _, ok := h.run("agent", "protocol", "ack", "demo", "alice", "--run-id", "old-run", "--version", p.Version); ok {
		t.Fatal("old run accepted")
	}
	if _, ok := h.run("agent", "protocol", "ack", "demo", "alice", "--run-id", a.RunID, "--version", strings.Repeat("0", 64)); ok {
		t.Fatal("old revision accepted")
	}
	must("agent", "protocol", "ack", "demo", "alice", "--run-id", a.RunID, "--version", p.Version)
	must("agent", "protocol", "ack", "demo", "alice", "--run-id", a.RunID, "--version", p.Version)
	got, err := h.api.GetAgent(context.Background(), "demo", "alice")
	if err != nil || got.RunID != a.RunID || got.PID != 123 || got.Protocol.AckedAt == "" {
		t.Fatalf("run disturbed or no receipt: %+v %v", got, err)
	}
	if _, err = h.api.RegisterAgent(context.Background(), model.Agent{Team: "demo", Name: "leader", RunID: "leader-run"}); err != nil {
		t.Fatal(err)
	}
	p, err = h.api.AgentProtocol(context.Background(), "demo", "leader", nil)
	if err != nil || !strings.Contains(p.Content, "Leader Protocol") || strings.Contains(p.Content, "Worker Loop Protocol") {
		t.Fatalf("wrong leader rules: %+v %v", p, err)
	}
}
