package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestPlanApprovalFlow(t *testing.T) {
	h := newHarness(t)
	h.json(&map[string]any{}, "team", "spawn-team", "demo")
	var added struct {
		Member model.Member `json:"member"`
	}
	h.json(&added, "team", "add-member", "demo", "alice", "--plan-required")
	if !added.Member.PlanRequired {
		t.Fatalf("add-member --plan-required: %+v", added.Member)
	}
	var task model.Task
	h.json(&task, "task", "create", "demo", "build", "-o", "alice")

	// Blocked before any approved plan.
	t.Setenv("TEAM_WORKER_AGENT_NAME", "alice")
	out, ok := h.run("--json", "task", "update", "demo", task.ID, "-s", "in_progress")
	if ok || !strings.Contains(out, "plan submit demo alice") {
		t.Fatalf("update before approval: ok=%v %s", ok, out)
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	os.WriteFile(planFile, []byte("1. write code\n2. test it\n"), 0o600)
	var submitted map[string]string
	h.json(&submitted, "plan", "submit", "demo", "alice", planFile, "-s", "two steps")
	id := submitted["planId"]
	if submitted["status"] != "submitted" || submitted["agent"] != "alice" || len(id) != 12 {
		t.Fatalf("submit: %v", submitted)
	}

	// Only the leader may decide.
	out, ok = h.run("--json", "plan", "approve", "demo", id, "alice")
	if ok || !strings.Contains(out, "Only the team leader") {
		t.Fatalf("approve as alice: ok=%v %s", ok, out)
	}
	t.Setenv("TEAM_WORKER_AGENT_NAME", "leader")
	var leaderInbox []model.Message
	h.json(&leaderInbox, "inbox", "receive", "demo")
	if len(leaderInbox) != 1 || leaderInbox[0].Type != "plan_approval_request" ||
		leaderInbox[0].Plan != "1. write code\n2. test it\n" || leaderInbox[0].Summary != "two steps" {
		t.Fatalf("leader inbox: %+v", leaderInbox)
	}
	var decided map[string]string
	h.json(&decided, "plan", "approve", "demo", id, "alice", "-f", "looks good")
	if decided["status"] != "approved" || decided["planId"] != id {
		t.Fatalf("approve: %v", decided)
	}
	out, ok = h.run("--json", "plan", "reject", "demo", id, "alice")
	if ok || !strings.Contains(out, "already approved") {
		t.Fatalf("reject after approve: ok=%v %s", ok, out)
	}

	var shown model.Plan
	h.json(&shown, "plan", "show", "demo", id)
	if shown.Status != model.PlanApproved || shown.DecidedBy != "leader" || shown.Feedback != "looks good" || shown.Summary != "two steps" {
		t.Fatalf("show: %+v", shown)
	}
	var listed []model.Plan
	h.json(&listed, "plan", "list", "demo", "--status", "approved", "--agent", "alice")
	if len(listed) != 1 || listed[0].ID != id {
		t.Fatalf("list: %+v", listed)
	}

	t.Setenv("TEAM_WORKER_AGENT_NAME", "alice")
	var aliceInbox []model.Message
	h.json(&aliceInbox, "inbox", "receive", "demo")
	if len(aliceInbox) != 1 || aliceInbox[0].Type != "plan_approved" || aliceInbox[0].Feedback != "looks good" {
		t.Fatalf("alice inbox: %+v", aliceInbox)
	}
	if out, ok := h.run("task", "update", "demo", task.ID, "-s", "in_progress"); !ok {
		t.Fatalf("update after approval: %s", out)
	}
}

func TestPlanRequiredSpawnSetsEnvAndPrompt(t *testing.T) {
	h := newHarness(t)
	h.json(&map[string]any{}, "team", "spawn-team", "demo")
	out, ok := h.run("--json", "spawn", "subprocess", "--team", "demo", "-n", "alice", "--plan-required", "--dry-run", "--", "true")
	if !ok {
		t.Fatalf("dry run: %s", out)
	}
	var result struct {
		Prompt string `json:"prompt"`
		Env    []struct {
			Name string `json:"name"`
		} `json:"env"`
	}
	json.Unmarshal([]byte(out), &result)
	hasEnv := false
	for _, e := range result.Env {
		hasEnv = hasEnv || e.Name == "TEAM_WORKER_PLAN_MODE_REQUIRED"
	}
	if !hasEnv || !strings.Contains(result.Prompt, "## Plan approval") || !strings.Contains(result.Prompt, "team-worker plan submit demo alice") {
		t.Fatalf("dry run lacks plan approval: env=%v prompt=%q", result.Env, result.Prompt)
	}

	t.Setenv("TEAM_WORKER_PLAN_MODE_REQUIRED", "1")
	var id map[string]any
	h.json(&id, "identity", "show")
	if id["planModeRequired"] != true {
		t.Fatalf("identity: %v", id)
	}
}
