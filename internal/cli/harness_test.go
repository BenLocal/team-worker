package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func TestHarnessCLIFlow(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	var started map[string]string
	h.json(&started, "harness", "start", "-g", "add tags", "-t", "demo", "-n", "2")
	if started["team"] != "demo" || started["phase"] != model.HarnessDiscuss || len(started["harness_id"]) != 12 {
		t.Fatalf("start = %v", started)
	}
	run := started["harness_id"]
	// The team was created with the caller as leader.
	if leader, err := h.api.Leader(ctx, "demo"); err != nil || leader.Name != "leader" {
		t.Fatalf("leader = %+v, %v", leader, err)
	}
	if out, ok := h.run("--json", "harness", "start", "-g", "again", "-t", "demo"); ok || !strings.Contains(out, "already has a running harness") {
		t.Fatalf("second start: ok=%v %s", ok, out)
	}

	var st model.HarnessStatus
	h.json(&st, "harness", "status", "demo")
	if st.ID != run || st.CanAdvance || !strings.Contains(st.GateReason, "discussion.md") || st.AgentCount != 2 {
		t.Fatalf("status = %+v", st)
	}
	if out, ok := h.run("--json", "harness", "advance", "demo"); ok || !strings.Contains(out, "Cannot advance") {
		t.Fatalf("advance with closed gate: ok=%v %s", ok, out)
	}

	h.json(&map[string]any{}, "harness", "artifact", "demo", "discussion.md", "--content", "options")
	var adv model.HarnessAdvance
	h.json(&adv, "harness", "advance", "demo")
	if adv.From != model.HarnessDiscuss || adv.Phase != model.HarnessPlan {
		t.Fatalf("advance = %+v", adv)
	}

	spec := filepath.Join(t.TempDir(), "spec.md")
	os.WriteFile(spec, []byte("# Spec\n"), 0o644)
	h.json(&map[string]any{}, "harness", "artifact", "demo", "spec.md", "--file", spec)
	if out, ok := h.runIn("from stdin", "--json", "harness", "artifact", "demo", "notes.md"); !ok || !strings.Contains(out, `"size": 10`) {
		t.Fatalf("artifact from stdin: ok=%v %s", ok, out)
	}
	if out, ok := h.run("harness", "artifacts", "demo", "--show", "spec.md"); !ok || out != "# Spec\n" {
		t.Fatalf("artifacts --show: ok=%v %q", ok, out)
	}
	var arts []model.HarnessArtifact
	h.json(&arts, "harness", "artifacts", "demo")
	if len(arts) != 3 || arts[0].Content != "" {
		t.Fatalf("artifacts = %+v", arts)
	}

	if out, ok := h.run("--json", "harness", "approve", "demo"); ok || !strings.Contains(out, "No pending plan") {
		t.Fatalf("approve without plan: ok=%v %s", ok, out)
	}
	h.json(&map[string]any{}, "team", "add-member", "demo", "planner-1")
	if _, err := h.api.AddHarnessAgent(ctx, "demo", run, store.HarnessAgentInput{Name: "planner-1", Role: model.RolePlanner}); err != nil {
		t.Fatal(err)
	}
	h.json(&map[string]any{}, "plan", "submit", "demo", "planner-1", spec)
	var approved map[string]string
	h.json(&approved, "harness", "approve", "demo")
	if approved["status"] != "approved" || approved["agent"] != "planner-1" {
		t.Fatalf("approve = %v", approved)
	}
	h.json(&adv, "harness", "advance", "demo")
	if adv.Phase != model.HarnessExecute {
		t.Fatalf("advance = %+v", adv)
	}

	var task model.Task
	h.json(&task, "harness", "task", "demo", "build tags", "-d", "do it")
	if task.Metadata["harnessRun"] != run {
		t.Fatalf("harness task = %+v", task)
	}
	h.json(&map[string]any{}, "task", "update", "demo", task.ID, "-s", "completed")
	h.json(&adv, "harness", "advance", "demo")
	h.json(&map[string]any{}, "harness", "artifact", "demo", "eval-report.json", "--content", `{"verdict":"pass"}`)
	h.json(&adv, "harness", "advance", "demo")
	if adv.Phase != model.HarnessShip || adv.Status != model.HarnessCompleted {
		t.Fatalf("final advance = %+v", adv)
	}
	h.json(&st, "harness", "status", "demo")
	if st.Status != model.HarnessCompleted || len(st.History) != 4 {
		t.Fatalf("final status = %+v", st)
	}
	if out, ok := h.run("--json", "harness", "abort", "demo"); ok || !strings.Contains(out, "already completed") {
		t.Fatalf("abort completed run: ok=%v %s", ok, out)
	}

	h.json(&started, "harness", "start", "-g", "second", "-t", "demo")
	var aborted map[string]any
	h.json(&aborted, "harness", "abort", "demo", "-r", "changed my mind")
	if aborted["status"] != "aborted" || aborted["harness_id"] != started["harness_id"] {
		t.Fatalf("abort = %v", aborted)
	}
}

func TestHarnessConductorDrivesARunToShip(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var started map[string]string
	h.json(&started, "harness", "start", "-g", "add tags", "-t", "demo", "-n", "2")
	run := started["harness_id"]

	a := &app{out: &strings.Builder{}, errOut: &strings.Builder{}, newClient: nil}
	a.c = h.api
	var spawned, stopped []string
	// Fake agents use this test's PID, so they look alive until "stopped".
	spawn := func(ctx context.Context, team, name, role, cli, prompt string) error {
		if !strings.Contains(prompt, "Harness run "+run) || cli != "claude" {
			t.Errorf("spawn %s: cli %q prompt %q", name, cli, prompt)
		}
		if _, err := h.api.AddMember(ctx, team, store.AddMemberInput{Name: name, AgentType: role}); err != nil {
			return err
		}
		_, err := h.api.RegisterAgent(ctx, model.Agent{Team: team, Name: name, Backend: "subprocess", PID: os.Getpid()})
		spawned = append(spawned, role)
		return err
	}
	stop := func(ctx context.Context, team, name string) error {
		stopped = append(stopped, name)
		_, err := h.api.AgentExited(ctx, team, name)
		return err
	}
	newConductor := func() *conductor {
		return &conductor{a: a, team: "demo", poll: time.Millisecond, spawn: spawn, stop: stop}
	}
	cd := newConductor()
	tick := func() {
		t.Helper()
		if done, err := cd.tick(ctx); err != nil || done {
			t.Fatalf("tick: done=%v err=%v", done, err)
		}
	}
	status := func() model.HarnessStatus {
		st, err := h.api.HarnessStatus(ctx, "demo", "")
		if err != nil {
			t.Fatal(err)
		}
		return st
	}

	tick()
	if !slices.Equal(spawned, []string{model.RolePlanner}) {
		t.Fatalf("discuss spawned %v", spawned)
	}
	planner := status().Agents[0].Name
	tick() // nothing to do: the planner is alive and the gate is closed
	if len(spawned) != 1 {
		t.Fatalf("respawned: %v", spawned)
	}

	h.api.PutArtifact(ctx, "demo", "", model.ArtifactDiscussion, "options")
	tick()
	if status().Phase != model.HarnessPlan {
		t.Fatalf("phase = %s", status().Phase)
	}
	h.api.PutArtifact(ctx, "demo", "", model.ArtifactSpec, "spec")
	if _, err := h.api.SubmitPlan(ctx, "demo", store.SubmitPlanInput{Agent: planner, Plan: "spec"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.ApproveHarness(ctx, "demo", "", "leader", ""); err != nil {
		t.Fatal(err)
	}
	tick() // plan -> execute
	tick() // planner stopped, two executors started
	if !slices.Equal(stopped, []string{planner}) || !slices.Equal(spawned, []string{model.RolePlanner, model.RoleExecutor, model.RoleExecutor}) {
		t.Fatalf("execute: stopped %v spawned %v", stopped, spawned)
	}
	// A conductor resumed after a crash finds the recorded executors and starts none.
	cd = newConductor()
	tick()
	if len(spawned) != 3 {
		t.Fatalf("resumed conductor spawned %v", spawned)
	}

	task, err := h.api.CreateHarnessTask(ctx, "demo", "", store.CreateTaskInput{Subject: "build"})
	if err != nil {
		t.Fatal(err)
	}
	done := model.StatusCompleted
	h.api.UpdateTask(ctx, "demo", task.ID, store.UpdateTaskInput{Status: &done})
	tick() // execute -> verify
	tick() // executors stopped, evaluator started
	if len(stopped) != 3 || spawned[len(spawned)-1] != model.RoleEvaluator {
		t.Fatalf("verify: stopped %v spawned %v", stopped, spawned)
	}
	h.api.PutArtifact(ctx, "demo", "", model.ArtifactEvalReport, `{"verdict":"pass"}`)
	tick() // verify -> ship, run completed
	if st := status(); st.Status != model.HarnessCompleted {
		t.Fatalf("status = %+v", st)
	}
	// run returns once the run is over, after stopping the evaluator.
	if err := cd.run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 4 {
		t.Fatalf("stopped %v", stopped)
	}
}
