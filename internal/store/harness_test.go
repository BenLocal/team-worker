package store

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func mustAdvance(t *testing.T, s *Store, want string) model.HarnessAdvance {
	t.Helper()
	out, err := s.AdvanceHarness(context.Background(), "demo", "")
	if err != nil {
		t.Fatalf("advance to %s: %v", want, err)
	}
	if out.Phase != want {
		t.Fatalf("advanced to %s, want %s (%+v)", out.Phase, want, out)
	}
	return out
}

func blockedAdvance(t *testing.T, s *Store, reason string) {
	t.Helper()
	_, err := s.AdvanceHarness(context.Background(), "demo", "")
	if KindOf(err) != KindConflict || !strings.Contains(err.Error(), reason) {
		t.Fatalf("advance should be blocked by %q, got %v", reason, err)
	}
}

func put(t *testing.T, s *Store, name, content string) {
	t.Helper()
	if _, err := s.PutArtifact(context.Background(), "demo", "", name, content); err != nil {
		t.Fatal(err)
	}
}

// runTasks returns the run's tasks of one attempt.
func runTasks(t *testing.T, s *Store, run string, attempt int) []model.Task {
	t.Helper()
	all, err := s.ListTasks(context.Background(), "demo", TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Task
	for _, task := range all {
		if task.Metadata["harnessRun"] == run && task.Metadata["harnessAttempt"] == float64(attempt) {
			out = append(out, task)
		}
	}
	return out
}

func completeTask(t *testing.T, s *Store, id string) {
	t.Helper()
	done := model.StatusCompleted
	if _, err := s.UpdateTask(context.Background(), "demo", id, UpdateTaskInput{Status: &done}); err != nil {
		t.Fatal(err)
	}
}

// toVerify takes a fresh run through discuss, plan and execute.
func toVerify(t *testing.T, s *Store, run model.HarnessRun) {
	t.Helper()
	ctx := context.Background()
	put(t, s, model.ArtifactDiscussion, "notes")
	mustAdvance(t, s, model.HarnessPlan)
	put(t, s, model.ArtifactSpec, "spec")
	if _, err := s.AddHarnessAgent(ctx, "demo", run.ID, HarnessAgentInput{Name: "planner-1", Role: model.RolePlanner}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SubmitPlan(ctx, "demo", SubmitPlanInput{Agent: "planner-1", Plan: "spec"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveHarnessPlan(ctx, "demo", "", "leader", ""); err != nil {
		t.Fatal(err)
	}
	mustAdvance(t, s, model.HarnessExecute)
	task, err := s.CreateHarnessTask(ctx, "demo", "", CreateTaskInput{Subject: "build"})
	if err != nil {
		t.Fatal(err)
	}
	completeTask(t, s, task.ID)
	mustAdvance(t, s, model.HarnessVerify)
}

func TestHarnessGatesTakeARunToShip(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "planner-1", "exec-1")
	ctx := context.Background()

	run, err := s.StartHarness(ctx, "demo", StartHarnessInput{Goal: "add tags"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Phase != model.HarnessDiscuss || run.Attempt != 1 || run.MaxAttempts != 2 || run.AgentCount != 3 || run.CLI != "claude" {
		t.Fatalf("run = %+v", run)
	}
	if _, err := s.StartHarness(ctx, "demo", StartHarnessInput{Goal: "again"}); KindOf(err) != KindConflict {
		t.Fatalf("second running run: %v", err)
	}

	blockedAdvance(t, s, model.ArtifactDiscussion)
	put(t, s, model.ArtifactDiscussion, "options")
	mustAdvance(t, s, model.HarnessPlan)

	blockedAdvance(t, s, model.ArtifactSpec)
	put(t, s, model.ArtifactSpec, "spec")
	blockedAdvance(t, s, "approve")

	// Only a plan from the run's planner counts, and only the leader approves it.
	if _, err := s.ApproveHarnessPlan(ctx, "demo", "", "leader", ""); KindOf(err) != KindNotFound {
		t.Fatalf("approve without a planner plan: %v", err)
	}
	if _, err := s.SubmitPlan(ctx, "demo", SubmitPlanInput{Agent: "exec-1", Plan: "not the planner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddHarnessAgent(ctx, "demo", "", HarnessAgentInput{Name: "planner-1", Role: model.RolePlanner}); err != nil {
		t.Fatal(err)
	}
	plan, err := s.SubmitPlan(ctx, "demo", SubmitPlanInput{Agent: "planner-1", Plan: "spec"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveHarnessPlan(ctx, "demo", "", "exec-1", ""); KindOf(err) != KindConflict {
		t.Fatalf("non-leader approval: %v", err)
	}
	approved, err := s.ApproveHarnessPlan(ctx, "demo", "", "leader", "ok")
	if err != nil || approved.ID != plan.ID || approved.Status != model.PlanApproved {
		t.Fatalf("approve = %+v, %v", approved, err)
	}
	if msgs, _ := s.Receive(ctx, "demo", "planner-1", "", 10); len(msgs) != 1 || msgs[0].Type != "plan_approved" {
		t.Fatalf("planner inbox %+v", msgs)
	}
	mustAdvance(t, s, model.HarnessExecute)

	blockedAdvance(t, s, "No tasks")
	if _, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "unrelated"}); err != nil {
		t.Fatal(err)
	}
	blockedAdvance(t, s, "No tasks")
	task, err := s.CreateHarnessTask(ctx, "demo", "", CreateTaskInput{Subject: "build"})
	if err != nil {
		t.Fatal(err)
	}
	blockedAdvance(t, s, "0 of 1")
	completeTask(t, s, task.ID)
	mustAdvance(t, s, model.HarnessVerify)

	blockedAdvance(t, s, model.ArtifactEvalReport)
	put(t, s, model.ArtifactEvalReport, "not json")
	blockedAdvance(t, s, "not valid JSON")
	put(t, s, model.ArtifactEvalReport, `{"verdict":"maybe"}`)
	blockedAdvance(t, s, "verdict")
	put(t, s, model.ArtifactEvalReport, `{"verdict":"pass","summary":"all good"}`)
	if out := mustAdvance(t, s, model.HarnessShip); out.Status != model.HarnessCompleted {
		t.Fatalf("ship = %+v", out)
	}

	st, err := s.HarnessStatus(ctx, "demo", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != model.HarnessCompleted || st.CanAdvance || len(st.History) != 4 || st.History[3].Phase != model.HarnessVerify ||
		len(st.Artifacts) != 3 || len(st.Agents) != 1 {
		t.Fatalf("status = %+v", st)
	}
	// A finished run frees the team for a new one.
	if _, err := s.StartHarness(ctx, "demo", StartHarnessInput{Goal: "next"}); err != nil {
		t.Fatal(err)
	}
}

func TestHarnessFailedEvaluationRollsBackUntilAttemptsRunOut(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "planner-1", "exec-1")
	ctx := context.Background()
	run, err := s.StartHarness(ctx, "demo", StartHarnessInput{Goal: "add tags", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddHarnessAgent(ctx, "demo", run.ID, HarnessAgentInput{Name: "exec-1", Role: model.RoleExecutor}); err != nil {
		t.Fatal(err)
	}
	toVerify(t, s, run)

	put(t, s, model.ArtifactEvalReport, `{"verdict":"fail","feedback":"tests fail in tags_test.go"}`)
	st, _ := s.HarnessStatus(ctx, "demo", "")
	if !st.CanAdvance || !strings.Contains(st.GateReason, "returns the run to execute") {
		t.Fatalf("status before rollback = %+v", st)
	}
	out := mustAdvance(t, s, model.HarnessExecute)
	if !out.RolledBack || out.Attempt != 2 || out.Status != model.HarnessRunning {
		t.Fatalf("rollback = %+v", out)
	}
	msgs, _ := s.Receive(ctx, "demo", "exec-1", "", 10)
	if len(msgs) != 1 || msgs[0].From != "harness" || !strings.Contains(msgs[0].Content, "tests fail in tags_test.go") {
		t.Fatalf("executor inbox %+v", msgs)
	}
	st, _ = s.HarnessStatus(ctx, "demo", "")
	if st.Attempt != 2 || slices.Contains(st.Artifacts, model.ArtifactEvalReport) ||
		st.History[len(st.History)-1].Action != "rolled_back" {
		t.Fatalf("status after rollback = %+v", st)
	}

	// The rework task belongs to attempt 2 and keeps the gate closed.
	rework := runTasks(t, s, run.ID, 2)
	if len(rework) != 1 || !strings.Contains(rework[0].Description, "tests fail") {
		t.Fatalf("rework tasks %+v", rework)
	}
	blockedAdvance(t, s, "0 of 1")
	completeTask(t, s, rework[0].ID)
	mustAdvance(t, s, model.HarnessVerify)

	put(t, s, model.ArtifactEvalReport, `{"verdict":"fail","summary":"still broken"}`)
	out, err = s.AdvanceHarness(ctx, "demo", "")
	if err != nil || out.Status != model.HarnessFailed || out.Phase != model.HarnessVerify {
		t.Fatalf("last failure = %+v, %v", out, err)
	}
	st, _ = s.HarnessStatus(ctx, "demo", run.ID)
	if st.Status != model.HarnessFailed || st.History[len(st.History)-1].Note != "still broken" {
		t.Fatalf("failed status = %+v", st)
	}
}

func TestHarnessAbortAndArtifactRules(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	if _, err := s.HarnessStatus(ctx, "demo", ""); KindOf(err) != KindNotFound {
		t.Fatalf("status without runs: %v", err)
	}
	if _, err := s.StartHarness(ctx, "demo", StartHarnessInput{}); KindOf(err) != KindInvalid {
		t.Fatalf("empty goal: %v", err)
	}
	run, err := s.StartHarness(ctx, "demo", StartHarnessInput{Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"..", "a/b", "", strings.Repeat("x", 129)} {
		if _, err := s.PutArtifact(ctx, "demo", "", name, "x"); KindOf(err) != KindInvalid {
			t.Fatalf("artifact name %q: %v", name, err)
		}
	}
	if _, err := s.PutArtifact(ctx, "demo", "", "big.txt", strings.Repeat("x", MaxArtifactLength+1)); KindOf(err) != KindInvalid {
		t.Fatalf("oversized artifact: %v", err)
	}
	put(t, s, "notes.md", "v1")
	put(t, s, "notes.md", "v2")
	if a, err := s.GetArtifact(ctx, "demo", run.ID, "notes.md"); err != nil || a.Content != "v2" || a.Size != 2 {
		t.Fatalf("artifact = %+v, %v", a, err)
	}

	st, err := s.AbortHarness(ctx, "demo", "", "wrong goal")
	if err != nil || st.Status != model.HarnessAborted || st.History[0].Action != "aborted" || st.History[0].Note != "wrong goal" {
		t.Fatalf("abort = %+v, %v", st, err)
	}
	if _, err := s.AbortHarness(ctx, "demo", run.ID, ""); KindOf(err) != KindConflict {
		t.Fatalf("second abort: %v", err)
	}
	if _, err := s.AdvanceHarness(ctx, "demo", run.ID); KindOf(err) != KindConflict {
		t.Fatalf("advance aborted run: %v", err)
	}
	if _, err := s.CreateHarnessTask(ctx, "demo", run.ID, CreateTaskInput{Subject: "late"}); KindOf(err) != KindConflict {
		t.Fatalf("task for aborted run: %v", err)
	}
	if _, err := s.StartHarness(ctx, "demo", StartHarnessInput{Goal: "better goal"}); err != nil {
		t.Fatal(err)
	}
	if runs, _ := s.ListHarness(ctx, "demo"); len(runs) != 2 || runs[0].Goal != "better goal" {
		t.Fatalf("runs = %+v", runs)
	}
	// Deleting the team removes its runs.
	if _, err := s.DeleteTeam(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM harness_runs").Scan(&n)
	if n != 0 {
		t.Fatalf("%d runs left after team delete", n)
	}
}
