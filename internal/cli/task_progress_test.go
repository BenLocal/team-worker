package cli

import (
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func TestTaskProgressAndGoalCommands(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	var goal, old, next, got model.Task
	h.json(&goal, "task", "create", "demo", "Continuous coverage", "--kind", "goal", "--owner", "leader")
	h.json(&old, "task", "create", "demo", "First round", "--parent", goal.ID, "--owner", "alice")
	h.json(&got, "task", "update", "demo", old.ID, "--status", "in_progress")
	for range 2 {
		h.json(&got, "task", "progress", "demo", old.ID, "--key", "batch-1", "--summary", "Three checks passed", "--verification", "Read exported report")
	}
	h.json(&got, "task", "progress", "demo", old.ID, "--key", "wait-input", "--summary", "Batch ready", "--state", "waiting", "--reason", "Needs a sample", "--next-action", "Inspect the next sample")
	h.json(&got, "task", "get", "demo", old.ID)
	if len(got.Progress) != 2 || got.Result != nil || got.ExecutionState != "unregistered" || got.ParentID != goal.ID {
		t.Fatalf("stage history missing: %+v", got)
	}
	out, ok := h.run("task", "get", "demo", old.ID)
	if !ok || !strings.Contains(out, "No registered trigger") || !strings.Contains(out, "Read exported report") {
		t.Fatalf("stage not visible: %s", out)
	}
	h.json(&next, "task", "create", "demo", "Next round", "--parent", goal.ID, "--owner", "alice")
	h.json(&got, "task", "update", "demo", old.ID, "--superseded-by", next.ID, "--result", "Ended early; remaining coverage in next round")
	if got.Status != model.StatusSuperseded || got.SupersededBy != next.ID || len(got.Progress) != 2 || got.LockedBy != "" {
		t.Fatalf("handoff lost context: %+v", got)
	}
	out, ok = h.run("--json", "task", "wait", "demo", "--task", old.ID, "--timeout", "0.1")
	if ok || !strings.Contains(out, "superseded by") {
		t.Fatalf("old round incorrectly passed: %s", out)
	}
	h.run("task", "update", "demo", next.ID, "--status", "completed", "--result", "Remaining checks passed")
	out, ok = h.run("task", "wait", "demo", "--timeout", "0.1")
	if !ok || !strings.Contains(out, "All 1 tasks completed") {
		t.Fatalf("goal kept default wait open: %s", out)
	}
	var stats model.TaskStats
	h.json(&stats, "task", "stats", "demo")
	if stats.Goals != 1 || stats.Superseded != 1 || stats.Completed != 1 || stats.Total != 3 {
		t.Fatalf("mixed counts: %+v", stats)
	}
}

func TestStopHookAllowsRegisteredWaitWithoutIdleNotice(t *testing.T) {
	h, ctx := newHookTeam(t)
	var task model.Task
	h.json(&task, "task", "create", "demo", "Await observation", "--owner", "alice")
	request, err := h.api.CreateDecisionRequest(ctx, "demo", store.CreateDecisionInput{From: "leader", Summary: "Approve observation", Body: "Scope decision"})
	if err != nil {
		t.Fatal(err)
	}
	p := model.TaskProgress{Key: "wait", Summary: "Ready", State: "waiting", Reason: "Needs a decision", NextAction: "Read decision", RequestID: request.ID}
	if _, err = h.api.UpdateTask(ctx, "demo", task.ID, store.UpdateTaskInput{Progress: &p}); err != nil {
		t.Fatal(err)
	}
	_, out := h.hook(`{}`, "stop", "--team", "demo", "--agent", "alice", "--wait", "0")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("valid wait forced another turn: %s", out)
	}
	msgs, err := h.api.Peek(ctx, "demo", "leader", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range msgs {
		if msg.Type == "idle" {
			t.Fatalf("waiting worker reported as having no tasks: %+v", msg)
		}
	}
	if _, err = h.api.DecideRequest(ctx, "demo", request.ID, store.DecideInput{Action: "reject", Reply: "Do not run", DecidedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	res, _ := h.hook(`{}`, "stop", "--team", "demo", "--agent", "alice", "--wait", "0")
	if res.Decision != "block" || !strings.Contains(res.Reason, task.ID) {
		t.Fatalf("resolved wait did not require follow-up: %+v", res)
	}
}
