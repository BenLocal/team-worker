package store

import (
	"context"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

func TestTaskStagesPreserveResultsAndRetry(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	task, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Observe", Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	final := &model.TaskResult{Summary: "Existing delivery", Artifacts: []string{"reports/final.txt"}}
	if _, err = s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice", Result: final}); err != nil {
		t.Fatal(err)
	}
	p := model.TaskProgress{Key: "batch-1", Summary: "First batch checked", Artifacts: []string{"reports/one.txt"}, Verification: "Three cases passed"}
	for range 2 {
		if _, err = s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p, Caller: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	p.Key = "batch-2"
	p.Summary = "Second batch checked"
	if _, err = s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p, Caller: "leader"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTask(ctx, "demo", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Progress) != 2 || got.Result.Summary != final.Summary || got.LockedBy != "alice" || got.Owner != "alice" || got.Progress[0].ReportedBy != "alice" {
		t.Fatalf("lost report or ownership: %+v", got)
	}
	p.Summary = "Changed contents"
	if _, err = s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p}); KindOf(err) != KindConflict {
		t.Fatalf("retry conflict: %v", err)
	}
}

func TestTaskWaitingReferencesStayLive(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	task, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Observe", Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	cp, err := s.CreateCheckpoint(ctx, "demo", CreateCheckpointInput{TaskID: task.ID, Key: "sample", DueAt: s.Now().Add(time.Hour), ResultDueAt: s.Now().Add(2 * time.Hour), AckTimeoutSeconds: 300, EscalateAfterSeconds: 600, Action: "Sample", ExpectedEvidence: "Report", Caller: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	p := model.TaskProgress{Key: "waiting", State: "waiting", Summary: "Batch ready", Reason: "Observation window", NextAction: "Collect sample", CheckpointID: cp.ID}
	if _, err = s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTask(ctx, "demo", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextCheckpoint == nil || got.WaitCheckpoint == nil || got.WaitCheckpoint.Status != "scheduled" {
		t.Fatalf("missing trigger: %+v", got)
	}
	if _, err = s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "cancel", Reason: "Window changed", Caller: "alice"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetTask(ctx, "demo", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextCheckpoint != nil || got.WaitCheckpoint.Status != "canceled" {
		t.Fatalf("stale trigger: %+v", got)
	}
	other, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Other", Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateTask(ctx, "demo", other.ID, UpdateTaskInput{Progress: &p}); err == nil {
		t.Fatal("accepted another task's checkpoint")
	}
}

func TestTaskSupersessionPreservesUnfinishedAcceptance(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	old, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Old round", Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "New round", Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Accept", BlockedBy: []string{old.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateTask(ctx, "demo", old.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice"}); err != nil {
		t.Fatal(err)
	}
	cp, err := s.CreateCheckpoint(ctx, "demo", CreateCheckpointInput{TaskID: old.ID, Key: "old-observation", DueAt: s.Now().Add(time.Hour), ResultDueAt: s.Now().Add(2 * time.Hour), AckTimeoutSeconds: 300, EscalateAfterSeconds: 600, Action: "Sample", ExpectedEvidence: "Evidence", Caller: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	input := UpdateTaskInput{Status: ptr(model.StatusSuperseded), SupersededBy: &next.ID, Result: &model.TaskResult{Summary: "Old round ended early; remaining duration moves to new round"}}
	got, err := s.UpdateTask(ctx, "demo", old.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateTask(ctx, "demo", old.ID, input); err != nil {
		t.Fatalf("handoff retry: %v", err)
	}
	closedCP, err := s.GetCheckpoint(ctx, "demo", cp.ID)
	if err != nil || closedCP.Status != "canceled" {
		t.Fatalf("old trigger still active: %+v %v", closedCP, err)
	}
	if got.Status != model.StatusSuperseded || got.LockedBy != "" {
		t.Fatalf("not closed: %+v", got)
	}
	got, err = s.GetTask(ctx, "demo", dep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusBlocked || len(got.BlockedBy) != 1 || got.BlockedBy[0] != next.ID {
		t.Fatalf("premature acceptance: %+v", got)
	}
	if _, err = s.UpdateTask(ctx, "demo", next.ID, UpdateTaskInput{Status: ptr(model.StatusCompleted)}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetTask(ctx, "demo", dep.ID)
	if err != nil || got.Status != model.StatusPending {
		t.Fatalf("handoff never resolved: %+v %v", got, err)
	}
	if _, err = s.UpdateTask(ctx, "demo", old.ID, UpdateTaskInput{Status: ptr(model.StatusCompleted)}); KindOf(err) != KindConflict {
		t.Fatalf("reopened superseded round: %v", err)
	}
}

func TestGoalHierarchyRejectsCyclesAndCountsSeparately(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	goal, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Ongoing coverage", Kind: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Batch", ParentID: goal.ID})
	if err != nil {
		t.Fatal(err)
	}
	nested, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Nested", Kind: "goal", ParentID: goal.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateTask(ctx, "demo", goal.ID, UpdateTaskInput{ParentID: &nested.ID}); err == nil {
		t.Fatal("accepted parent cycle")
	}
	if _, err = s.UpdateTask(ctx, "demo", goal.ID, UpdateTaskInput{Kind: ptr("task")}); err == nil {
		t.Fatal("detached existing children")
	}
	if _, err = s.UpdateTask(ctx, "demo", child.ID, UpdateTaskInput{ParentID: &child.ID}); err == nil {
		t.Fatal("accepted self parent")
	}
	st, err := s.TaskStats(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if st.Goals != 2 || st.Pending != 1 {
		t.Fatalf("goals counted as executions: %+v", st)
	}
}

func TestSupersessionRejectsCyclesAndResolvesLaterDependencies(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	old, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Old"})
	if err != nil {
		t.Fatal(err)
	}
	downstream, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Dependent", BlockedBy: []string{old.ID}})
	if err != nil {
		t.Fatal(err)
	}
	input := UpdateTaskInput{Status: ptr(model.StatusSuperseded), SupersededBy: &downstream.ID, Result: &model.TaskResult{Summary: "Hand off"}}
	if _, err = s.UpdateTask(ctx, "demo", old.ID, input); err == nil {
		t.Fatal("accepted cyclic handoff")
	}
	next, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Replacement"})
	if err != nil {
		t.Fatal(err)
	}
	input.SupersededBy = &next.ID
	if _, err = s.UpdateTask(ctx, "demo", old.ID, input); err != nil {
		t.Fatal(err)
	}
	later, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Later dependent", BlockedBy: []string{old.ID, next.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(later.BlockedBy) != 1 || later.BlockedBy[0] != next.ID {
		t.Fatalf("stuck on archived round: %+v", later)
	}
}

func TestWaitingRequestDecisionAndEmptyArtifactRetry(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Wait for approval"})
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.CreateDecisionRequest(ctx, "demo", CreateDecisionInput{From: "leader", Summary: "Allow a test?", Body: "Bounded test"})
	if err != nil {
		t.Fatal(err)
	}
	p := model.TaskProgress{Key: "await-approval", State: "waiting", Summary: "Ready", Reason: "New test scope", NextAction: "Read decision", RequestID: request.ID}
	for range 2 {
		if _, err = s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetTask(ctx, "demo", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WaitingRequest == nil || got.ExecutionState != "waiting" || len(got.Progress) != 1 {
		t.Fatalf("missing decision wait: %+v", got)
	}
	if _, err = s.DecideRequest(ctx, "demo", request.ID, DecideInput{Action: "reject", Reply: "Do not run", DecidedBy: "operator"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetTask(ctx, "demo", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionState != "follow_up" || got.WaitingRequest.Status != "rejected" {
		t.Fatalf("decision was not reflected: %+v", got)
	}
	if got.Status != model.StatusPending || len(got.Progress) != 1 {
		t.Fatalf("decision executed work or rewrote history: %+v", got)
	}
}

func TestTaskTrackingUpgradeAndSnapshot(t *testing.T) {
	path := legacyDB(t, 21)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	old, err := s.GetTask(ctx, "legacy", "t1")
	if err != nil || len(old.Progress) != 0 || old.SupersededBy != "" {
		t.Fatalf("legacy upgrade: %+v %v", old, err)
	}
	newTeam(t, s)
	goal, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Coverage", Kind: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Batch", ParentID: goal.ID})
	if err != nil {
		t.Fatal(err)
	}
	p := model.TaskProgress{Key: "stage", Summary: "Evidence checked"}
	if _, err = s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.CreateSnapshot(ctx, "demo", "stage")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "later"
	if _, err = s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RestoreSnapshot(ctx, "demo", snap.ID, false); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTask(ctx, "demo", task.ID)
	if err != nil || got.ParentID != goal.ID || len(got.Progress) != 1 || got.Progress[0].Key != "stage" {
		t.Fatalf("snapshot lost tracking: %+v %v", got, err)
	}
}

func TestTaskProgressRetryDoesNotRefreshActivity(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Observe"})
	if err != nil {
		t.Fatal(err)
	}
	p := model.TaskProgress{Key: "sample", Summary: "One observation"}
	first, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p})
	if err != nil {
		t.Fatal(err)
	}
	now := s.Now()
	s.Now = func() time.Time { return now.Add(time.Hour) }
	again, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p})
	if err != nil {
		t.Fatal(err)
	}
	if again.UpdatedAt != first.UpdatedAt || len(again.Progress) != 1 || again.Progress[0].ReportedAt != first.Progress[0].ReportedAt {
		t.Fatalf("retry manufactured activity: %+v", again)
	}
}

func TestWaitReviewValidatesEvidenceAndReferences(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	task, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Observe", Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	request, err := s.CreateDecisionRequest(ctx, "demo", CreateDecisionInput{From: "leader", Summary: "Window", Body: "Confirm window"})
	if err != nil {
		t.Fatal(err)
	}
	p := model.TaskProgress{Key: "legacy", State: "waiting", Summary: "Prepared", Reason: "Window", NextAction: "Observe", RequestID: request.ID}
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p}); err != nil {
		t.Fatal(err)
	}
	read := func() model.Task {
		t.Helper()
		got, err := s.GetTask(ctx, "demo", task.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	review := func(got model.Task) error {
		_, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Caller: "leader", WaitReview: &model.TaskWaitReview{Version: got.WaitVersion, Summary: "Checked remaining work"}})
		return err
	}
	if err := review(read()); KindOf(err) != KindInvalid {
		t.Fatalf("legacy wait without scope reviewed: %v", err)
	}
	p.Key, p.WaitScope, p.IndependentWork = "scoped", "Device sample", "Offline report passed"
	for _, id := range []string{task.ID, "missing"} {
		p.IndependentTasks = []string{id}
		if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p}); KindOf(err) != KindInvalid {
			t.Fatalf("invalid independent task %s accepted: %v", id, err)
		}
	}
	p.IndependentTasks = nil
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p}); err != nil {
		t.Fatal(err)
	}
	current := read()
	for _, in := range []UpdateTaskInput{
		{Caller: "leader", WaitReview: &model.TaskWaitReview{Version: current.WaitVersion}},
		{Caller: "leader", WaitReview: &model.TaskWaitReview{Version: current.WaitVersion, Summary: "Checked"}, Description: ptr("Changed scope")},
		{WaitReview: &model.TaskWaitReview{Version: current.WaitVersion, Summary: "Checked"}},
	} {
		if _, err := s.UpdateTask(ctx, "demo", task.ID, in); KindOf(err) != KindInvalid {
			t.Fatalf("invalid review accepted: %v", err)
		}
	}
	if err := review(read()); err != nil {
		t.Fatal(err)
	}
	if read().WaitReviewNeeded {
		t.Fatal("review did not settle wait")
	}
	p.Key = "revised-wait"
	p.WaitScope = "Device sample and new runtime check"
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p}); err != nil {
		t.Fatal(err)
	}
	if !read().WaitReviewNeeded {
		t.Fatal("new report reused old review")
	}
	if err := review(read()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Owner: ptr("leader")}); err != nil {
		t.Fatal(err)
	}
	if !read().WaitReviewNeeded {
		t.Fatal("reassignment reused old review")
	}
}

func TestWaitReviewRejectsDueCheckpoint(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	task, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Observe", Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	now := s.Now()
	cp, err := s.CreateCheckpoint(ctx, "demo", CreateCheckpointInput{TaskID: task.ID, Key: "sample", DueAt: now.Add(time.Hour), ResultDueAt: now.Add(2 * time.Hour), AckTimeoutSeconds: 300, EscalateAfterSeconds: 600, Action: "Sample", ExpectedEvidence: "Report", Caller: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	p := model.TaskProgress{Key: "wait", Summary: "Prepared", State: "waiting", Reason: "Window", NextAction: "Observe", CheckpointID: cp.ID, WaitScope: "Device sample", IndependentWork: "Offline checks complete in reports/offline.txt"}
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Progress: &p}); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetTask(ctx, "demo", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return cp.DueAt }
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Caller: "leader", WaitReview: &model.TaskWaitReview{Version: current.WaitVersion, Summary: "Reviewed"}}); KindOf(err) != KindInvalid {
		t.Fatalf("due checkpoint accepted as future wait: %v", err)
	}
}
