package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

func TestBoardOverview(t *testing.T) {
	h := newHarness(t)
	var empty []model.TeamOverview
	h.json(&empty, "board", "overview")
	if len(empty) != 0 {
		t.Fatalf("%+v", empty)
	}
	h.run("team", "spawn-team", "alpha", "-n", "boss", "-d", "first")
	h.run("team", "add-member", "alpha", "dev")
	h.run("team", "spawn-team", "beta")
	h.run("task", "create", "alpha", "one")
	h.run("task", "create", "alpha", "two")
	h.run("inbox", "send", "alpha", "dev", "hello")
	var got []model.TeamOverview
	h.json(&got, "board", "overview")
	want := []model.TeamOverview{
		{Name: "alpha", Description: "first", Leader: "boss", Members: 2, Tasks: 2, PendingMessages: 1},
		{Name: "beta", Leader: "leader", Members: 1},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v", got)
	}
}

func TestBoardLiveAndUpdate(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	h.run("task", "create", "demo", "write docs")
	out, ok := h.run("--json", "board", "live", "demo", "--count", "2", "-i", "10ms")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !ok || len(lines) != 2 {
		t.Fatalf("%v %q", ok, out)
	}
	var b model.Board
	if err := json.Unmarshal([]byte(lines[1]), &b); err != nil || b.Stats.Total != 1 {
		t.Fatalf("%v %+v", err, b)
	}
	if out, ok := h.run("board", "live", "demo", "--count", "1"); !ok || !strings.Contains(out, "\x1b[2J") ||
		!strings.Contains(out, "write docs") {
		t.Fatalf("human live: %v %q", ok, out)
	}
	if out, ok := h.run("board", "live", "nope", "--count", "1"); ok {
		t.Fatalf("unknown team: %s", out)
	}

	var up map[string]any
	h.json(&up, "board", "update", "demo")
	if up["status"] != "up_to_date" || up["team"] != "demo" || up["agent"] != nil {
		t.Fatalf("%v", up)
	}
	if out, ok := h.run("board", "update", "nope"); ok {
		t.Fatalf("update of an unknown team: %s", out)
	}
}

func TestIdentitySet(t *testing.T) {
	h := newHarness(t)
	out, ok := h.run("identity", "set", "--agent-name", "alice", "--team", "it's")
	want := "export TEAM_WORKER_AGENT_NAME='alice'\nexport TEAM_WORKER_TEAM_NAME='it'\\''s'\n"
	if !ok || out != want {
		t.Fatalf("%v %q", ok, out)
	}
	var j map[string][]string
	h.json(&j, "identity", "set", "--agent-id", "x1")
	if len(j["exports"]) != 1 || j["exports"][0] != "export TEAM_WORKER_AGENT_ID='x1'" {
		t.Fatalf("%v", j)
	}
	if _, ok := h.run("identity", "set"); ok {
		t.Fatal("no options must fail")
	}
}

func TestHarnessContracts(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	if out, ok := h.run("harness", "start", "-t", "demo", "-g", "ship it"); !ok {
		t.Fatalf("start: %s", out)
	}
	for _, name := range []string{"sprint-contract-001.json", "notes.md", "sprint-contract-002.json"} {
		if out, ok := h.run("harness", "artifact", "demo", name, "--content", "{}"); !ok {
			t.Fatalf("artifact %s: %s", name, out)
		}
	}
	var got []model.HarnessArtifact
	h.json(&got, "harness", "contracts", "demo")
	if len(got) != 2 || !strings.HasPrefix(got[0].Name, "sprint-contract") || !strings.HasPrefix(got[1].Name, "sprint-contract") {
		t.Fatalf("%+v", got)
	}
}

func TestFindZombies(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	yes, no := true, false
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339Nano) }
	agents := []model.Agent{
		{Name: "old", PID: 7, Backend: "tmux", Status: model.AgentRunning, StartedAt: at(3 * time.Hour), Alive: &yes, Activity: "working"},
		{Name: "young", Status: model.AgentRunning, StartedAt: at(time.Hour), Alive: &yes},
		{Name: "dead", Status: model.AgentRunning, StartedAt: at(5 * time.Hour), Alive: &no},
		{Name: "unknown", Status: model.AgentRunning, StartedAt: at(5 * time.Hour)},
		{Name: "exited", Status: model.AgentExited, StartedAt: at(5 * time.Hour), Alive: &yes},
	}
	got := findZombies(agents, 2, now)
	if len(got) != 1 || got[0].AgentName != "old" || got[0].RunningHours != 3 || got[0].PID != 7 ||
		got[0].Activity != "working" || got[0].SpawnedAt != float64(now.Add(-3*time.Hour).Unix()) {
		t.Fatalf("%+v", got)
	}
}

func TestCheckZombiesWithoutAgents(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	var res map[string]any
	h.json(&res, "lifecycle", "check-zombies", "--team", "demo")
	if res["team"] != "demo" || len(res["zombies"].([]any)) != 0 {
		t.Fatalf("%v", res)
	}
	if _, ok := h.run("lifecycle", "check-zombies"); ok {
		t.Fatal("a team is required")
	}
}

func TestRunCreatesTeamWithAgentAsLeader(t *testing.T) {
	h := newHarness(t)
	var res model.SpawnResult
	h.json(&res, "run", "true", "Fix the login bug", "--backend", "subprocess", "-t", "solo")
	if !strings.HasPrefix(res.AgentName, "true-") {
		t.Fatalf("%+v", res)
	}
	var team model.Team
	h.json(&team, "team", "status", "solo")
	if leaderOf(team) != res.AgentName || len(team.Members) != 1 || team.Members[0].AgentType != "true" {
		t.Fatalf("%+v", team)
	}
	out, ok := h.run("agent", "prompt", "show", "solo", res.AgentName)
	if !ok || !strings.Contains(out, "Fix the login bug") {
		t.Fatalf("instructions: %v %s", ok, out)
	}
}
