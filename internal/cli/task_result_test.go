package cli

import (
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestTaskResultReport(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	var task model.Task
	h.json(&task, "task", "create", "demo", "Export report", "--owner", "alice")
	var got map[string]any
	h.json(&got, "task", "update", "demo", task.ID, "--status", "completed",
		"--result", "Exported the report", "--artifact", "reports/output.csv", "--artifact", "reports/check.txt",
		"--verification", "Passed: 12 rows checked")
	h.json(&got, "task", "get", "demo", task.ID)
	r, ok := got["result"].(map[string]any)
	if !ok || r["summary"] != "Exported the report" || r["verification"] != "Passed: 12 rows checked" || r["reportedAt"] == "" {
		t.Fatalf("result not persisted: %#v", got)
	}
	if files, ok := r["artifacts"].([]any); !ok || len(files) != 2 || files[0] != "reports/output.csv" {
		t.Fatalf("artifacts: %#v", r)
	}
	h.run("task", "update", "demo", task.ID, "--subject", "Updated title")
	out, ok := h.run("task", "get", "demo", task.ID)
	if !ok || !strings.Contains(out, "Exported the report") || !strings.Contains(out, "reports/check.txt") || !strings.Contains(out, "Passed: 12 rows checked") {
		t.Fatalf("result not visible after unrelated update: %s", out)
	}
}

func TestTaskResultsForAnotherAgent(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	var old, latest, legacy, draft model.Task
	h.json(&old, "task", "create", "demo", "Earlier report", "--owner", "alice")
	h.run("task", "update", "demo", old.ID, "--status", "completed", "--result", "Earlier findings")
	h.json(&latest, "task", "create", "demo", "Latest report", "--owner", "alice", "--description", "Evidence in reports/check.txt")
	h.run("task", "update", "demo", latest.ID, "--status", "completed", "--result", "Latest findings", "--artifact", "reports/check.txt", "--verification", "Three checks passed")
	h.json(&legacy, "task", "create", "demo", "Legacy report", "--owner", "bob", "--description", "Existing task notes")
	h.run("task", "update", "demo", legacy.ID, "--status", "completed")
	h.json(&draft, "task", "create", "demo", "Unfinished", "--owner", "alice")
	h.run("task", "update", "demo", draft.ID, "--result", "Draft findings")
	var results []model.Task
	h.json(&results, "task", "results", "demo")
	if len(results) != 2 {
		t.Fatalf("latest per owner: %+v", results)
	}
	byOwner := map[string]model.Task{}
	for _, task := range results {
		byOwner[task.Owner] = task
	}
	if byOwner["alice"].ID != latest.ID || byOwner["alice"].Result == nil || byOwner["alice"].Result.Summary != "Latest findings" || byOwner["alice"].Result.Verification != "Three checks passed" || len(byOwner["alice"].Result.Artifacts) != 1 {
		t.Fatalf("latest result: %+v", byOwner["alice"])
	}
	if byOwner["bob"].ID != legacy.ID || byOwner["bob"].Description != "Existing task notes" || byOwner["bob"].Result != nil {
		t.Fatalf("legacy context: %+v", byOwner["bob"])
	}
	h.json(&results, "task", "results", "demo", "--owner", "alice", "--all")
	if len(results) != 2 || results[0].ID != latest.ID || results[1].ID != old.ID {
		t.Fatalf("owner history: %+v", results)
	}
	h.json(&results, "task", "results", "demo", "--all")
	if len(results) != 3 {
		t.Fatalf("all history: %+v", results)
	}
	h.json(&results, "task", "results", "demo", "--owner", "nobody")
	if results == nil || len(results) != 0 {
		t.Fatalf("empty results: %+v", results)
	}
	out, ok := h.run("task", "results", "demo", "--owner", "alice")
	if !ok || !strings.Contains(out, "Latest findings") || strings.Contains(out, "Earlier findings") {
		t.Fatalf("text results: %s", out)
	}
	// The leader can organize another owner's result without taking ownership.
	h.json(&latest, "task", "update", "demo", latest.ID, "--result", "Organized findings", "--artifact", "reports/check.txt", "--verification", "Three checks passed")
	if latest.Owner != "alice" {
		t.Fatalf("report changed owner: %+v", latest)
	}
}
