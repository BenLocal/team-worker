package store

import (
	"context"
	"github.com/benlocal/team-worker/internal/model"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCheckpointDeliveryAndExplicitAcknowledgment(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "worker")
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	task, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Observe", Owner: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	in := CreateCheckpointInput{Key: "sample-1", TaskID: task.ID, Caller: "worker", DueAt: now.Add(time.Minute), AckTimeoutSeconds: 60, ResultDueAt: now.Add(5 * time.Minute), EscalateAfterSeconds: 60, Action: "Read sample", ExpectedEvidence: "sample log"}
	cp, err := s.CreateCheckpoint(ctx, "demo", in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateCheckpoint(ctx, "demo", in)
	if err != nil || again.ID != cp.ID {
		t.Fatalf("retry: %+v %v", again, err)
	}
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	now = in.DueAt
	for range 2 {
		if err := s.AdvanceCheckpoints(ctx); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := s.Receive(ctx, "demo", "worker", "", 50)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("delivery: %v %v", msgs, err)
	}
	cp, err = s.GetCheckpoint(ctx, "demo", cp.ID)
	if err != nil || cp.Status != "notified" {
		t.Fatalf("consumption must not acknowledge: %+v %v", cp, err)
	}
	now = now.Add(time.Minute)
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	msgs, err = s.Receive(ctx, "demo", "leader", "", 50)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("leader alert: %v %v", msgs, err)
	}
	now = now.Add(time.Minute)
	for range 2 {
		if err := s.AdvanceCheckpoints(ctx); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err = s.Receive(ctx, "demo", OperatorInbox, "", 50)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("operator alert: %v %v", msgs, err)
	}
	cp, err = s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "ack", Caller: "worker"})
	if err != nil || cp.Token == "" {
		t.Fatalf("ack: %+v %v", cp, err)
	}
	done := CheckpointUpdateInput{Action: "complete", Caller: "worker", Token: cp.Token, Outcome: "passed", Summary: "Sample collected", Evidence: "artifacts/sample.log"}
	for range 2 {
		cp, err = s.UpdateCheckpoint(ctx, "demo", cp.ID, done)
		if err != nil {
			t.Fatal(err)
		}
	}
	if cp.Status != "completed" || cp.LeaderAlertAt == "" || cp.OperatorAlertAt == "" {
		t.Fatalf("late completion lost alerts: %+v", cp)
	}
}

func checkpointFixture(t *testing.T) (*Store, *time.Time, CreateCheckpointInput) {
	t.Helper()
	s := newTestStore(t)
	newTeam(t, s, "worker", "other")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	task, err := s.CreateTask(context.Background(), "demo", CreateTaskInput{Subject: "Observe", Owner: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	return s, &now, CreateCheckpointInput{Key: "sample", TaskID: task.ID, Caller: "worker", DueAt: now.Add(time.Minute), AckTimeoutSeconds: 60, ResultDueAt: now.Add(5 * time.Minute), EscalateAfterSeconds: 60, Action: "Read sample", ExpectedEvidence: "sample log"}
}

func TestCheckpointReassignmentRejectsStaleResults(t *testing.T) {
	s, now, in := checkpointFixture(t)
	ctx := context.Background()
	cp, err := s.CreateCheckpoint(ctx, "demo", in)
	if err != nil {
		t.Fatal(err)
	}
	*now = in.DueAt
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	cp, err = s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "ack", Caller: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	token := cp.Token
	if _, err := s.UpdateTask(ctx, "demo", in.TaskID, UpdateTaskInput{Owner: ptr("other")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, "demo", in.TaskID, UpdateTaskInput{Owner: ptr("worker")}); err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "complete", Caller: "worker", Token: token, Outcome: "passed", Summary: "old run", Evidence: "old.log"})
	if KindOf(err) != KindConflict {
		t.Fatalf("old owner must not resurrect canceled checkpoint: %v", err)
	}
	in.Key = "replacement"
	replacement, err := s.CreateCheckpoint(ctx, "demo", in)
	if err != nil {
		t.Fatal(err)
	}
	*now = in.ResultDueAt.Add(time.Hour)
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	cp, err = s.GetCheckpoint(ctx, "demo", cp.ID)
	if err != nil || cp.Status != "canceled" || cp.LeaderAlertAt != "" {
		t.Fatalf("old checkpoint fired: %+v %v", cp, err)
	}
	if _, err := s.UpdateTask(ctx, "demo", in.TaskID, UpdateTaskInput{Status: ptr(model.StatusCompleted)}); err != nil {
		t.Fatal(err)
	}
	replacement, err = s.GetCheckpoint(ctx, "demo", replacement.ID)
	if err != nil || replacement.Status != "canceled" {
		t.Fatalf("completed task retained checkpoint: %+v %v", replacement, err)
	}
}

func TestCheckpointResultDeadlineAndIdempotentAck(t *testing.T) {
	s, now, in := checkpointFixture(t)
	ctx := context.Background()
	cp, err := s.CreateCheckpoint(ctx, "demo", in)
	if err != nil {
		t.Fatal(err)
	}
	*now = in.DueAt
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	cp, err = s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "ack", Caller: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "ack", Caller: "worker"})
	if err != nil || again.Token != cp.Token {
		t.Fatalf("ack retry: %+v %v", again, err)
	}
	// Acknowledgment suppresses the acknowledgment deadline, not the result deadline.
	*now = in.ResultDueAt.Add(-time.Millisecond)
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Peek(ctx, "demo", "leader", "")
	if err != nil || len(msgs) != 0 {
		t.Fatalf("premature alert: %v %v", msgs, err)
	}
	*now = in.ResultDueAt
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	cp, err = s.GetCheckpoint(ctx, "demo", cp.ID)
	if err != nil || cp.TimeoutReason != "result overdue" {
		t.Fatalf("result timeout: %+v %v", cp, err)
	}
}

func TestCheckpointValidationAndOwnership(t *testing.T) {
	s, _, valid := checkpointFixture(t)
	ctx := context.Background()
	for name, change := range map[string]func(*CreateCheckpointInput){
		"missing key":      func(in *CreateCheckpointInput) { in.Key = " " },
		"missing action":   func(in *CreateCheckpointInput) { in.Action = " " },
		"missing evidence": func(in *CreateCheckpointInput) { in.ExpectedEvidence = "" },
		"zero timeout":     func(in *CreateCheckpointInput) { in.AckTimeoutSeconds = 0 },
		"overflow":         func(in *CreateCheckpointInput) { in.EscalateAfterSeconds = 1 << 62 },
		"early result":     func(in *CreateCheckpointInput) { in.ResultDueAt = in.DueAt },
		"wrong caller":     func(in *CreateCheckpointInput) { in.Caller = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			in := valid
			change(&in)
			if _, err := s.CreateCheckpoint(ctx, "demo", in); err == nil {
				t.Fatal("accepted invalid checkpoint")
			}
		})
	}
	cp, err := s.CreateCheckpoint(ctx, "demo", valid)
	if err != nil {
		t.Fatal(err)
	}
	changed := valid
	changed.Action = "different"
	if _, err := s.CreateCheckpoint(ctx, "demo", changed); KindOf(err) != KindConflict {
		t.Fatalf("key conflict: %v", err)
	}
	for _, in := range []CheckpointUpdateInput{
		{Action: "ack", Caller: "worker"},
		{Action: "ack", Caller: "other"},
		{Action: "complete", Caller: "worker", Token: "wrong", Outcome: "passed", Summary: "done", Evidence: "log"},
		{Action: "cancel", Caller: "other", Reason: "no"},
		{Action: "cancel", Caller: "worker"},
		{Action: "unknown", Caller: "worker"},
	} {
		if _, err := s.UpdateCheckpoint(ctx, "demo", cp.ID, in); err == nil {
			t.Fatalf("accepted invalid update: %+v", in)
		}
	}
	for range 2 {
		if _, err := s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "cancel", Caller: "leader", Reason: "superseded"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckpointRestartRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	newTeam(t, s, "worker")
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	task, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Observe", Owner: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	cp, err := s.CreateCheckpoint(ctx, "demo", CreateCheckpointInput{Key: "sample", TaskID: task.ID, DueAt: now.Add(time.Minute), AckTimeoutSeconds: 60, ResultDueAt: now.Add(time.Hour), EscalateAfterSeconds: 60, Action: "Read", ExpectedEvidence: "log"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	s.Now = func() time.Time { return now }
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Now = func() time.Time { return now }
	for range 2 {
		if err := s.AdvanceCheckpoints(ctx); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := s.Receive(ctx, "demo", "worker", "", 50)
	if err != nil || len(messages) != 1 {
		t.Fatalf("restart lost or duplicated delivery: %v %v", messages, err)
	}
	cp, err = s.GetCheckpoint(ctx, "demo", cp.ID)
	if err != nil || cp.LeaderAlertAt == "" {
		t.Fatalf("restart missed timeout: %+v %v", cp, err)
	}
}

func TestCheckpointConcurrentTicksAndAcknowledgments(t *testing.T) {
	s, now, in := checkpointFixture(t)
	ctx := context.Background()
	cp, err := s.CreateCheckpoint(ctx, "demo", in)
	if err != nil {
		t.Fatal(err)
	}
	*now = in.DueAt
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := s.AdvanceCheckpoints(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	msgs, err := s.Receive(ctx, "demo", "worker", "", 50)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("concurrent delivery: %v %v", msgs, err)
	}
	tokens := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			cp, err := s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "ack", Caller: "worker"})
			if err != nil {
				t.Error(err)
				return
			}
			tokens <- cp.Token
		})
	}
	wg.Wait()
	close(tokens)
	token := ""
	for got := range tokens {
		if token != "" && got != token {
			t.Fatal("concurrent ack changed token")
		}
		token = got
	}
}

func TestCheckpointIdleTickDoesNotNotifyHub(t *testing.T) {
	s := newTestStore(t)
	calls := 0
	s.OnChange = func() { calls++ }
	if err := s.AdvanceCheckpoints(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("empty tick notified watchers")
	}
}

func TestCheckpointMigratesExistingDatabase(t *testing.T) {
	s, err := Open(legacyDB(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.UpdateTask(ctx, "legacy", "t1", UpdateTaskInput{Owner: ptr("lead")}); err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC()
	cp, err := s.CreateCheckpoint(ctx, "legacy", CreateCheckpointInput{Key: "sample", TaskID: "t1", DueAt: due, AckTimeoutSeconds: 60, ResultDueAt: due.Add(time.Hour), EscalateAfterSeconds: 60, Action: "Read sample", ExpectedEvidence: "sample log"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCheckpoint(ctx, "legacy", cp.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointTransactionRollbackDoesNotLoseNotification(t *testing.T) {
	s, now, in := checkpointFixture(t)
	ctx := context.Background()
	cp, err := s.CreateCheckpoint(ctx, "demo", in)
	if err != nil {
		t.Fatal(err)
	}
	*now = in.DueAt
	if _, err := s.db.Exec("CREATE TRIGGER fail_checkpoint_message BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'injected failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCheckpoints(ctx); err == nil {
		t.Fatal("injected delivery failure ignored")
	}
	cp, err = s.GetCheckpoint(ctx, "demo", cp.ID)
	if err != nil || cp.Status != "scheduled" {
		t.Fatalf("state advanced without delivery: %+v %v", cp, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_checkpoint_message"); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	messages, err := s.Peek(ctx, "demo", "worker", "")
	if err != nil || len(messages) != 1 {
		t.Fatalf("retry: %v %v", messages, err)
	}
}

func TestCheckpointSnapshotRestoresState(t *testing.T) {
	s, now, in := checkpointFixture(t)
	ctx := context.Background()
	cp, err := s.CreateCheckpoint(ctx, "demo", in)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.CreateSnapshot(ctx, "demo", "before observation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "cancel", Caller: "worker", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreSnapshot(ctx, "demo", snap.ID, false); err != nil {
		t.Fatal(err)
	}
	*now = in.DueAt
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	cp, err = s.GetCheckpoint(ctx, "demo", cp.ID)
	if err != nil || cp.Status != "notified" {
		t.Fatalf("snapshot: %+v %v", cp, err)
	}
}

func TestCheckpointLateAckCannotEraseMissedDeadline(t *testing.T) {
	s, now, in := checkpointFixture(t)
	ctx := context.Background()
	cp, err := s.CreateCheckpoint(ctx, "demo", in)
	if err != nil {
		t.Fatal(err)
	}
	*now = in.DueAt
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	*now = in.DueAt.Add(2 * time.Minute)
	// No scheduler tick: the request itself must preserve the missed deadline.
	cp, err = s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "ack", Caller: "worker"})
	if err != nil || cp.TimeoutReason != "acknowledgment overdue" {
		t.Fatalf("late ack erased timeout: %+v %v", cp, err)
	}
}

func TestCheckpointLateCompletionCannotEraseMissedDeadline(t *testing.T) {
	s, now, in := checkpointFixture(t)
	ctx := context.Background()
	cp, err := s.CreateCheckpoint(ctx, "demo", in)
	if err != nil {
		t.Fatal(err)
	}
	*now = in.DueAt
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	cp, err = s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "ack", Caller: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	*now = in.ResultDueAt.Add(time.Second)
	cp, err = s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "complete", Caller: "worker", Token: cp.Token, Outcome: "passed", Summary: "late sample", Evidence: "log"})
	if err != nil || cp.TimeoutReason != "result overdue" {
		t.Fatalf("late completion erased timeout: %+v %v", cp, err)
	}
}

func TestCheckpointRequiresExistingOwner(t *testing.T) {
	s, _, in := checkpointFixture(t)
	ctx := context.Background()
	if _, err := s.UpdateTask(ctx, "demo", in.TaskID, UpdateTaskInput{Owner: ptr("missing")}); err != nil {
		t.Fatal(err)
	}
	in.Caller = "leader"
	if _, err := s.CreateCheckpoint(ctx, "demo", in); KindOf(err) != KindInvalid {
		t.Fatalf("unknown owner: %v", err)
	}
}

func TestCheckpointRepeatedAckPersistsTimeoutOnce(t *testing.T) {
	s, now, in := checkpointFixture(t)
	ctx := context.Background()
	cp, err := s.CreateCheckpoint(ctx, "demo", in)
	if err != nil {
		t.Fatal(err)
	}
	*now = in.DueAt
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "ack", Caller: "worker"}); err != nil {
		t.Fatal(err)
	}
	*now = in.ResultDueAt
	for range 2 {
		if _, err := s.UpdateCheckpoint(ctx, "demo", cp.ID, CheckpointUpdateInput{Action: "ack", Caller: "worker"}); err != nil {
			t.Fatal(err)
		}
	}
	cp, err = s.GetCheckpoint(ctx, "demo", cp.ID)
	if err != nil || cp.LeaderAlertAt == "" {
		t.Fatalf("ack retry did not persist timeout: %+v %v", cp, err)
	}
	msgs, err := s.Peek(ctx, "demo", "leader", "")
	if err != nil || len(msgs) != 1 {
		t.Fatalf("ack retry duplicated alert: %v %v", msgs, err)
	}
}

func TestCheckpointEscalatesToLeaderInbox(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateTeam(ctx, CreateTeamInput{Name: "demo", LeaderName: "leader", User: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMember(ctx, "demo", AddMemberInput{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "Observe", Owner: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	if _, err := s.CreateCheckpoint(ctx, "demo", CreateCheckpointInput{Key: "sample", TaskID: task.ID, DueAt: now, AckTimeoutSeconds: 1, ResultDueAt: now.Add(time.Minute), EscalateAfterSeconds: 1, Action: "Read", ExpectedEvidence: "log"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := s.AdvanceCheckpoints(ctx); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Peek(ctx, "demo", "leader", "one")
	if err != nil || len(msgs) != 1 {
		t.Fatalf("namespaced leader alert: %v %v", msgs, err)
	}
}
