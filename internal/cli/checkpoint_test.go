package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func TestCheckpointCLI(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.api.CreateTeam(ctx, store.CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}
	task, err := h.api.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "Observe", Owner: "leader"})
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	args := []string{"checkpoint", "create", "demo", task.ID, "--key", "sample", "--at", due.Format(time.RFC3339), "--result-by", due.Add(time.Hour).Format(time.RFC3339), "--action", "Read sample", "--expect", "sample log"}
	var cp, again model.Checkpoint
	h.json(&cp, args...)
	h.json(&again, args...)
	if cp.ID == "" || cp.ID != again.ID {
		t.Fatalf("registration retry: %+v %+v", cp, again)
	}
	var list []model.Checkpoint
	h.json(&list, "checkpoint", "list", "demo")
	if len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	h.json(&again, "checkpoint", "show", "demo", cp.ID)
	if again.Action != "Read sample" {
		t.Fatalf("show: %+v", again)
	}
	out, ok := h.run("checkpoint", "show", "demo", cp.ID)
	if !ok || !strings.Contains(out, "Read sample") {
		t.Fatalf("human output: %s", out)
	}
	if _, ok := h.run("--json", "checkpoint", "ack", "demo", cp.ID); ok {
		t.Fatal("ack before notification succeeded")
	}
	if _, ok := h.run("--json", "checkpoint", "complete", "demo", cp.ID, "--token", "wrong", "--outcome", "passed", "--summary", "done", "--evidence", "log"); ok {
		t.Fatal("invalid completion succeeded")
	}
	h.json(&again, "checkpoint", "cancel", "demo", cp.ID, "--reason", "superseded")
	if again.Status != "canceled" {
		t.Fatalf("cancel: %+v", again)
	}
	args[7] = "2026-01-01T00:00:00"
	if _, ok := h.run(append([]string{"--json"}, args...)...); ok {
		t.Fatal("accepted timezone-free time")
	}
}

func TestCheckpointCLIUsesMemberInboxAndOperatorDefault(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.api.CreateTeam(ctx, store.CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"one", "two"} {
		if _, err := h.api.AddMember(ctx, "demo", store.AddMemberInput{Name: "worker", User: user}); err != nil {
			t.Fatal(err)
		}
	}
	task, err := h.api.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "Observe", Owner: "one_worker"})
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(time.Hour)
	args := []string{"checkpoint", "create", "demo", task.ID, "--key", "sample", "--at", due.Format(time.RFC3339), "--result-by", due.Add(time.Hour).Format(time.RFC3339), "--action", "Read", "--expect", "log"}
	t.Setenv("TEAM_WORKER_AGENT_NAME", "worker")
	t.Setenv("TEAM_WORKER_USER", "one")
	var cp model.Checkpoint
	h.json(&cp, args...)
	if cp.CreatedBy != "one_worker" {
		t.Fatalf("wrong identity: %+v", cp)
	}
	t.Setenv("TEAM_WORKER_AGENT_NAME", "")
	args[5] = "operator-sample"
	h.json(&cp, args...)
	if cp.CreatedBy != "operator" {
		t.Fatalf("outside agent: %+v", cp)
	}
	h.json(&cp, "checkpoint", "cancel", "demo", cp.ID, "--caller", "leader", "--reason", "superseded")
	if cp.Status != "canceled" {
		t.Fatal("explicit caller failed")
	}
}

func TestCheckpointOperatorIdentityHasNoUserPrefix(t *testing.T) {
	t.Setenv("TEAM_WORKER_AGENT_NAME", "operator")
	t.Setenv("TEAM_WORKER_USER", "example")
	if got := checkpointCallerName(""); got != "operator" {
		t.Fatalf("operator identity = %q", got)
	}
}
