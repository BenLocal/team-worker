package cli

import (
	"context"
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

func TestWaitReviewCommandsAndScopeChanges(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	var task, offline, got model.Task
	h.json(&task, "task", "create", "demo", "Scheduled observation", "--owner", "alice")
	h.json(&offline, "task", "create", "demo", "Offline assertions", "--owner", "alice")
	request, err := h.api.CreateDecisionRequest(context.Background(), "demo", store.CreateDecisionInput{From: "leader", Summary: "Observation window", Body: "Confirm window"})
	if err != nil {
		t.Fatal(err)
	}
	report := []string{"task", "progress", "demo", task.ID, "--key", "wait", "--summary", "Prepared inputs", "--state", "waiting", "--reason", "Needs window", "--next-action", "Observe", "--request", request.ID, "--wait-scope", "Device sample only", "--independent-work", "Offline assertions assigned separately", "--independent-task", offline.ID}
	h.json(&got, report...)
	h.json(&got, "task", "get", "demo", task.ID)
	if !got.WaitReviewNeeded || got.WaitVersion == "" {
		t.Fatalf("unreviewed wait hidden: %+v", got)
	}
	version := got.WaitVersion
	review := []string{"task", "review-wait", "demo", task.ID, "--version", version, "--summary", "Checked remaining deliverables; offline assertions have their own task"}
	t.Setenv("TEAM_WORKER_AGENT_NAME", "alice")
	if out, ok := h.run(review...); ok {
		t.Fatalf("worker reviewed its own wait: %s", out)
	}
	t.Setenv("TEAM_WORKER_AGENT_NAME", "leader")
	for range 2 {
		h.json(&got, review...)
	}
	h.json(&got, "task", "get", "demo", task.ID)
	if got.WaitReviewNeeded || got.WaitReview == nil || got.WaitReview.ReviewedBy != "leader" {
		t.Fatalf("review missing: %+v", got)
	}
	h.json(&got, report...)
	h.json(&got, "task", "get", "demo", task.ID)
	if got.WaitReviewNeeded {
		t.Fatal("identical report retry invalidated review")
	}
	out, ok := h.run("task", "get", "demo", task.ID)
	if !ok || !strings.Contains(out, "Device sample only") || !strings.Contains(out, offline.ID) || !strings.Contains(out, "Wait review by leader") {
		t.Fatalf("review evidence hidden: %s", out)
	}
	h.json(&got, "task", "update", "demo", task.ID, "--description", "Also verify the parser")
	h.json(&got, "task", "get", "demo", task.ID)
	if !got.WaitReviewNeeded || got.WaitVersion == version {
		t.Fatal("scope change retained old review")
	}
	if out, ok := h.run(review...); ok {
		t.Fatalf("stale review accepted: %s", out)
	}
	if _, err := h.api.DecideRequest(context.Background(), "demo", request.ID, store.DecideInput{Action: "reject", Reply: "Do not run", DecidedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	review[5] = got.WaitVersion
	if out, ok := h.run(review...); ok {
		t.Fatalf("resolved trigger accepted: %s", out)
	}
	h.json(&got, "task", "get", "demo", task.ID)
	if got.ExecutionState != "follow_up" || got.WaitReviewNeeded {
		t.Fatalf("resolved trigger did not require follow-up: %+v", got)
	}
	h.json(&got, "task", "get", "demo", offline.ID)
	if got.ExecutionState == "waiting" || got.Status != model.StatusPending {
		t.Fatalf("independent work was blocked: %+v", got)
	}
}
