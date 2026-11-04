package store

import (
	"context"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func addHook(t *testing.T, s *Store, event, team string) model.EventHook {
	t.Helper()
	h, err := s.AddEventHook(context.Background(), EventHookInput{Event: event, Command: "true", Team: team})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// pending returns the recorded events and forgets them, so each step of a
// test sees only its own events.
func pending(t *testing.T, s *Store) []model.Event {
	t.Helper()
	ctx := context.Background()
	events, err := s.PendingEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if err := s.EventDone(ctx, e.ID); err != nil {
			t.Fatal(err)
		}
	}
	return events
}

func types(events []model.Event) []string {
	out := []string{}
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEventsAreRecordedOnlyForHookedTypesAndTeams(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	task, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "build"})
	if got := pending(t, s); len(got) != 0 {
		t.Fatalf("recorded without hooks: %v", types(got))
	}

	addHook(t, s, "TaskCreated", "other")
	addHook(t, s, "AfterTaskUpdate", "")
	addHook(t, s, "TaskCompleted", "demo")
	s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "test"})
	s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice"})
	s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusCompleted), Caller: "alice"})
	s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Description: ptr("again")})

	got := pending(t, s)
	want := []string{"AfterTaskUpdate", "AfterTaskUpdate", "TaskCompleted", "AfterTaskUpdate"}
	if !sameStrings(types(got), want) {
		t.Fatalf("events %v, want %v", types(got), want)
	}
	start, done := got[0].Payload, got[2].Payload
	if start["old_status"] != "pending" || start["new_status"] != "in_progress" || start["owner"] != "alice" ||
		start["task_id"] != task.ID || start["team_name"] != "demo" || start["type"] != "AfterTaskUpdate" || start["timestamp"] == "" {
		t.Fatalf("update payload: %v", start)
	}
	if done["task_id"] != task.ID || done["owner"] != "alice" {
		t.Fatalf("completed payload: %v", done)
	}
	if _, ok := done["duration_seconds"].(float64); !ok {
		t.Fatalf("duration: %v", done)
	}
}

func TestFailedWriteRecordsNoEvent(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice", "bob")
	ctx := context.Background()
	addHook(t, s, "AfterTaskUpdate", "")
	task, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "build"})
	s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice"})
	s.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", Backend: "tmux", Target: "%1"})
	pending(t, s)

	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "bob"}); KindOf(err) != KindConflict {
		t.Fatalf("expected a lock conflict, got %v", err)
	}
	if got := pending(t, s); len(got) != 0 {
		t.Fatalf("a rolled-back update recorded %v", types(got))
	}
}

func TestAgentEvents(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	for _, e := range []string{"AfterWorkerSpawn", "WorkerExit", "WorkerCrash", "AfterTaskUpdate"} {
		addHook(t, s, e, "")
	}
	run := model.Agent{Team: "demo", Name: "alice", AgentID: "a1", Backend: "tmux", Target: "%1", RunID: "r1"}
	s.RegisterAgent(ctx, run)
	s.RegisterAgent(ctx, run) // the same run registering again is not a new spawn
	task, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "build"})
	s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice"})
	got := pending(t, s)
	if !sameStrings(types(got), []string{"AfterWorkerSpawn", "AfterTaskUpdate"}) {
		t.Fatalf("spawn events: %v", types(got))
	}
	if p := got[0].Payload; p["agent_name"] != "alice" || p["agent_id"] != "a1" || p["backend"] != "tmux" || p["target"] != "%1" {
		t.Fatalf("spawn payload: %v", p)
	}

	if _, err := s.AgentExitedRun(ctx, "demo", "alice", "r1", 137); err != nil {
		t.Fatal(err)
	}
	got = pending(t, s)
	if !sameStrings(types(got), []string{"AfterTaskUpdate", "WorkerExit", "WorkerCrash"}) {
		t.Fatalf("crash events: %v", types(got))
	}
	if p := got[0].Payload; p["old_status"] != "in_progress" || p["new_status"] != "pending" {
		t.Fatalf("released task payload: %v", p)
	}
	exit, crash := got[1].Payload, got[2].Payload
	abandoned, _ := exit["abandoned_tasks"].([]any)
	if exit["exit_code"] != float64(137) || len(abandoned) != 1 || abandoned[0] != task.ID {
		t.Fatalf("exit payload: %v", exit)
	}
	if crash["exit_code"] != float64(137) || crash["error"] != "exit code 137" {
		t.Fatalf("crash payload: %v", crash)
	}
	if _, err := s.AgentExitedRun(ctx, "demo", "alice", "r1", 137); err != nil {
		t.Fatal(err)
	}
	if got := pending(t, s); len(got) != 0 {
		t.Fatalf("a repeated exit report recorded %v", types(got))
	}

	// An agent asked to stop exits without crashing, whatever its code.
	run.RunID = "r2"
	s.RegisterAgent(ctx, run)
	if _, err := s.RequestAgentStop(ctx, "demo", "alice", "r2"); err != nil {
		t.Fatal(err)
	}
	s.AgentExitedRun(ctx, "demo", "alice", "r2", 143)
	if got := pending(t, s); !sameStrings(types(got), []string{"AfterWorkerSpawn", "WorkerExit"}) {
		t.Fatalf("stopped agent events: %v", types(got))
	}
}

func TestMemberTeamIdleAndBudgetEvents(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	for _, e := range []string{"MemberJoined", "MemberLeft", "AgentIdle", "BudgetExceeded", "TeamShutdown"} {
		addHook(t, s, e, "")
	}
	s.AddMember(ctx, "demo", AddMemberInput{Name: "alice", AgentType: "coder"})
	s.Send(ctx, "demo", SendInput{From: "alice", To: "leader", Type: "idle", Content: "alice is idle"})
	s.Send(ctx, "demo", SendInput{From: "alice", To: "leader", Content: "hello"})
	s.RemoveMember(ctx, "demo", "alice")
	s.RemoveMember(ctx, "demo", "alice")
	s.SetBudget(ctx, "demo", 100, false)
	s.ReportCost(ctx, "demo", model.CostEvent{AgentName: "leader", CostCents: 150})
	s.ReportCost(ctx, "demo", model.CostEvent{AgentName: "leader", CostCents: 10})
	if ok, err := s.DeleteTeam(ctx, "demo"); !ok || err != nil {
		t.Fatal(ok, err)
	}

	got := pending(t, s)
	want := []string{"MemberJoined", "AgentIdle", "MemberLeft", "BudgetExceeded", "TeamShutdown"}
	if !sameStrings(types(got), want) {
		t.Fatalf("events %v, want %v", types(got), want)
	}
	if p := got[0].Payload; p["agent_name"] != "alice" || p["agent_type"] != "coder" {
		t.Fatalf("joined payload: %v", p)
	}
	if p := got[3].Payload; p["spent_cents"] != float64(150) || p["budget_cents"] != float64(100) || p["enforce"] != false {
		t.Fatalf("budget payload: %v", p)
	}
	if got[4].Team != "demo" {
		t.Fatalf("shutdown team: %+v", got[4])
	}
}

func TestEventHookManagement(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, in := range []EventHookInput{
		{Event: "NoSuchEvent", Command: "true"},
		{Event: "WorkerExit", Command: "  "},
		{Event: "WorkerExit", Command: "true", Action: "python"},
		{Event: "WorkerExit", Command: "true", Team: "bad name"},
	} {
		if _, err := s.AddEventHook(ctx, in); KindOf(err) != KindInvalid {
			t.Fatalf("%+v accepted: %v", in, err)
		}
	}
	late, _ := s.AddEventHook(ctx, EventHookInput{Event: "WorkerExit", Command: "echo late", Priority: 5})
	first, _ := s.AddEventHook(ctx, EventHookInput{Event: "WorkerExit", Command: "echo first", Priority: -1})
	s.AddEventHook(ctx, EventHookInput{Event: "TaskCompleted", Command: "echo late"})
	hooks, err := s.ListEventHooks(ctx)
	if err != nil || len(hooks) != 3 || hooks[0].ID != first.ID || hooks[2].ID != late.ID ||
		hooks[0].Action != "shell" || !hooks[0].Enabled {
		t.Fatalf("%v %+v", err, hooks)
	}

	if n, err := s.RemoveEventHooks(ctx, "WorkerExit", "echo late"); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if n, _ := s.RemoveEventHooks(ctx, "WorkerExit", ""); n != 1 {
		t.Fatalf("removed %d", n)
	}
	if hooks, _ := s.ListEventHooks(ctx); len(hooks) != 1 || hooks[0].Event != "TaskCompleted" {
		t.Fatalf("left %+v", hooks)
	}

	payload, matching, err := s.TestEvent(ctx, "TaskCompleted", "demo", "bob")
	if err != nil || len(matching) != 1 || payload["type"] != "TaskCompleted" || payload["team_name"] != "demo" {
		t.Fatalf("%v %v %v", err, matching, payload)
	}
	if _, ok := payload["agent_name"]; ok {
		t.Fatalf("TaskCompleted has no agent_name: %v", payload)
	}
	if _, _, err := s.TestEvent(ctx, "Nope", "demo", ""); KindOf(err) != KindInvalid {
		t.Fatal(err)
	}
}
