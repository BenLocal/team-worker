package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newTeam(t *testing.T, s *Store, members ...string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.CreateTeam(ctx, CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		if _, err := s.AddMember(ctx, "demo", AddMemberInput{Name: m}); err != nil {
			t.Fatal(err)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func TestCreateTeamRejectsDuplicate(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	_, err := s.CreateTeam(context.Background(), CreateTeamInput{Name: "demo", LeaderName: "leader"})
	if KindOf(err) != KindConflict {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestTaskOnUnknownTeamIsNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.CreateTask(context.Background(), "nope", CreateTaskInput{Subject: "x"})
	if KindOf(err) != KindNotFound {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestCompletingTaskUnblocksDependents(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	a, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "a"})
	b, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "b"})
	c, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "c", BlockedBy: []string{a.ID, b.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != model.StatusBlocked {
		t.Fatalf("want blocked, got %s", c.Status)
	}

	if _, err := s.UpdateTask(ctx, "demo", a.ID, UpdateTaskInput{Status: ptr(model.StatusCompleted)}); err != nil {
		t.Fatal(err)
	}
	c, _ = s.GetTask(ctx, "demo", c.ID)
	if c.Status != model.StatusBlocked || len(c.BlockedBy) != 1 || c.BlockedBy[0] != b.ID {
		t.Fatalf("after a: status=%s blockedBy=%v", c.Status, c.BlockedBy)
	}

	if _, err := s.UpdateTask(ctx, "demo", b.ID, UpdateTaskInput{Status: ptr(model.StatusCompleted)}); err != nil {
		t.Fatal(err)
	}
	c, _ = s.GetTask(ctx, "demo", c.ID)
	if c.Status != model.StatusPending || len(c.BlockedBy) != 0 {
		t.Fatalf("after b: status=%s blockedBy=%v", c.Status, c.BlockedBy)
	}
}

func TestDependencyCyclesAreRejected(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	a, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "a"})
	b, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "b", BlockedBy: []string{a.ID}})
	c, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "c", BlockedBy: []string{b.ID}})

	_, err := s.UpdateTask(ctx, "demo", a.ID, UpdateTaskInput{AddBlockedBy: []string{c.ID}})
	if KindOf(err) != KindInvalid {
		t.Fatalf("want cycle rejection, got %v", err)
	}
	_, err = s.UpdateTask(ctx, "demo", a.ID, UpdateTaskInput{AddBlockedBy: []string{a.ID}})
	if KindOf(err) != KindInvalid {
		t.Fatalf("want self-block rejection, got %v", err)
	}
	a, _ = s.GetTask(ctx, "demo", a.ID)
	if len(a.BlockedBy) != 0 || a.Status != model.StatusPending {
		t.Fatalf("rejected update changed task: %+v", a)
	}
}

func TestInProgressTakesLockAndRecordsDuration(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	start := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return start }
	task, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "a"})

	task, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{
		Status: ptr(model.StatusInProgress), Caller: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if task.LockedBy != "alice" || task.Owner != "alice" || task.StartedAt == "" {
		t.Fatalf("lock not taken: %+v", task)
	}

	_, err = s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{
		Status: ptr(model.StatusInProgress), Caller: "bob"})
	if KindOf(err) != KindConflict {
		t.Fatalf("want lock conflict, got %v", err)
	}
	if _, err := s.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", Backend: "tmux"}); err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{
		Status: ptr(model.StatusInProgress), Caller: "bob"})
	if KindOf(err) != KindConflict {
		t.Fatalf("unknown probe result must keep the lock, got %v", err)
	}
	s.Probe = func(model.Agent) (bool, bool) { return false, true }
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{
		Status: ptr(model.StatusInProgress), Caller: "bob"}); err != nil {
		t.Fatalf("dead owner should release lock: %v", err)
	}

	s.Now = func() time.Time { return start.Add(90 * time.Second) }
	task, err = s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusCompleted)})
	if err != nil {
		t.Fatal(err)
	}
	if task.LockedBy != "" || task.Metadata["duration_seconds"] != 90.0 {
		t.Fatalf("completion: lockedBy=%q metadata=%v", task.LockedBy, task.Metadata)
	}
	stats, _ := s.TaskStats(ctx, "demo")
	if stats.Completed != 1 || stats.AvgDurationSeconds != 90 {
		t.Fatalf("stats: %+v", stats)
	}
}

func TestListTasksFiltersAndSorts(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "low", Priority: model.PriorityLow, Owner: "alice"})
	s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "urgent", Priority: model.PriorityUrgent})
	s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "mine", Owner: "alice"})

	mine, _ := s.ListTasks(ctx, "demo", TaskFilter{Owner: "alice"})
	if len(mine) != 2 || mine[0].Subject != "low" || mine[1].Subject != "mine" {
		t.Fatalf("owner filter: %+v", mine)
	}
	sorted, _ := s.ListTasks(ctx, "demo", TaskFilter{SortPriority: true})
	if sorted[0].Subject != "urgent" || sorted[2].Subject != "low" {
		t.Fatalf("priority sort: %v %v %v", sorted[0].Subject, sorted[1].Subject, sorted[2].Subject)
	}
}

func TestReceiveConsumesInOrder(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	for _, c := range []string{"one", "two", "three"} {
		if _, err := s.Send(ctx, "demo", SendInput{From: "leader", To: "alice", Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	peek, _ := s.Peek(ctx, "demo", "alice", "")
	if len(peek) != 3 {
		t.Fatalf("peek: %d", len(peek))
	}
	got, _ := s.Receive(ctx, "demo", "alice", "", 2)
	if len(got) != 2 || got[0].Content != "one" || got[1].Content != "two" {
		t.Fatalf("receive: %+v", got)
	}
	got, _ = s.Receive(ctx, "demo", "alice", "", 10)
	if len(got) != 1 || got[0].Content != "three" {
		t.Fatalf("second receive: %+v", got)
	}
	got, _ = s.Receive(ctx, "demo", "alice", "", 10)
	if len(got) != 0 {
		t.Fatalf("inbox should be empty: %+v", got)
	}
	log, _ := s.MessageLog(ctx, "demo", 2, "")
	if len(log) != 2 || log[0].Content != "two" || log[1].Content != "three" {
		t.Fatalf("log keeps newest, oldest first: %+v", log)
	}
}

func TestBroadcastSkipsSender(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice", "bob")
	ctx := context.Background()
	sent, err := s.Broadcast(ctx, "demo", BroadcastInput{From: "alice", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[0].To != "leader" || sent[1].To != "bob" {
		t.Fatalf("broadcast: %+v", sent)
	}
	if got, _ := s.Peek(ctx, "demo", "alice", ""); len(got) != 0 {
		t.Fatalf("sender received own broadcast")
	}
}

func TestUserScopedInbox(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	s.AddMember(ctx, "demo", AddMemberInput{Name: "dev", User: "ann"})
	s.AddMember(ctx, "demo", AddMemberInput{Name: "dev", User: "ben"})
	s.AddMember(ctx, "demo", AddMemberInput{Name: "qa", User: "ann"})

	s.Send(ctx, "demo", SendInput{From: "leader", To: "qa", Content: "unique name"})
	if got, _ := s.Receive(ctx, "demo", "qa", "ann", 10); len(got) != 1 {
		t.Fatalf("unique member should resolve to ann_qa: %+v", got)
	}

	// An ambiguous name falls back to the raw inbox key.
	s.Send(ctx, "demo", SendInput{From: "leader", To: "dev", Content: "ambiguous"})
	if got, _ := s.Peek(ctx, "demo", "dev", "ben"); len(got) != 0 {
		t.Fatalf("ben_dev should not see the ambiguous message")
	}
}

func TestDeleteTeamCascades(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "a"})
	s.Send(ctx, "demo", SendInput{From: "leader", To: "alice", Content: "x"})
	if ok, err := s.DeleteTeam(ctx, "demo"); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	var n int
	for _, table := range []string{"members", "tasks", "messages", "events"} {
		s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
		if n != 0 {
			t.Fatalf("%s left %d rows", table, n)
		}
	}
}

func TestAgentExitedReleasesTasksOnce(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	s.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", Backend: "tmux", Target: "%1"})
	task, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "build", Owner: "alice"})
	done, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "done", Owner: "alice"})
	s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice"})
	s.UpdateTask(ctx, "demo", done.ID, UpdateTaskInput{Status: ptr(model.StatusCompleted)})

	res, err := s.AgentExited(ctx, "demo", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || len(res.Released) != 1 || res.Released[0].ID != task.ID {
		t.Fatalf("exit: %+v", res)
	}
	got, _ := s.GetTask(ctx, "demo", task.ID)
	if got.Status != model.StatusPending || got.LockedBy != "" {
		t.Fatalf("task not released: %+v", got)
	}
	msgs, _ := s.Peek(ctx, "demo", "leader", "")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "Reset 1 task(s) to pending: build") {
		t.Fatalf("leader notification: %+v", msgs)
	}
	a, _ := s.GetAgent(ctx, "demo", "alice")
	if a.Status != model.AgentExited || a.ExitedAt == "" {
		t.Fatalf("agent record: %+v", a)
	}

	again, _ := s.AgentExited(ctx, "demo", "alice")
	if again.Changed {
		t.Fatalf("second exit report must be a no-op: %+v", again)
	}
	if msgs, _ := s.Peek(ctx, "demo", "leader", ""); len(msgs) != 1 {
		t.Fatalf("leader notified twice")
	}
	// The exited holder's lock no longer blocks others.
	s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice"})
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{
		Status: ptr(model.StatusInProgress), Caller: "bob"}); err != nil {
		t.Fatalf("lock of exited agent should be taken over: %v", err)
	}
}

func TestBoardCountsInboxes(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	s.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", Backend: "subprocess", PID: 1})
	s.Send(ctx, "demo", SendInput{From: "leader", To: "alice", Content: "a"})
	s.Send(ctx, "demo", SendInput{From: "leader", To: "alice", Content: "b"})
	b, err := s.Board(ctx, "demo", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Members) != 2 || b.Members[1].InboxCount != 2 || b.Members[1].Agent == nil || b.Members[0].Agent != nil {
		t.Fatalf("board members: %+v", b.Members)
	}
	if len(b.Messages) != 2 {
		t.Fatalf("board messages: %+v", b.Messages)
	}
}
