package store

import (
	"context"
	"slices"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

// teamState is what a restore must bring back, read through the public API.
type teamState struct {
	prompt   string
	members  []string
	tasks    map[string]model.TaskStatus
	deps     map[string][]string
	messages []string
	plans    int
	budget   float64
	costs    int
}

func readState(t *testing.T, s *Store) teamState {
	t.Helper()
	ctx := context.Background()
	team, err := s.GetTeam(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	st := teamState{tasks: map[string]model.TaskStatus{}, deps: map[string][]string{}}
	if st.prompt, err = s.TeamPrompt(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	for _, m := range team.Members {
		st.members = append(st.members, m.Name)
	}
	tasks, err := s.ListTasks(ctx, "demo", TaskFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		st.tasks[task.Subject] = task.Status
		st.deps[task.Subject] = task.BlockedBy
	}
	msgs, err := s.Peek(ctx, "demo", "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		st.messages = append(st.messages, m.Content)
	}
	plans, err := s.ListPlans(ctx, "demo", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st.plans = len(plans)
	b, err := s.Budget(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	st.budget = b.BudgetCents
	c, err := s.Costs(ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	st.costs = len(c.Events)
	return st
}

func equalState(a, b teamState) bool {
	if a.prompt != b.prompt || !slices.Equal(a.members, b.members) || !slices.Equal(a.messages, b.messages) ||
		a.plans != b.plans || a.budget != b.budget || a.costs != b.costs || len(a.tasks) != len(b.tasks) {
		return false
	}
	for k, v := range a.tasks {
		if b.tasks[k] != v || !slices.Equal(a.deps[k], b.deps[k]) {
			return false
		}
	}
	return true
}

func seedTeam(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	newTeam(t, s, "dev")
	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetTeamPrompt(ctx, "demo", "be careful"))
	first, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "first"})
	if err != nil {
		t.Fatal(err)
	}
	must(s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "second", BlockedBy: []string{first.ID}}))
	must(s.Send(ctx, "demo", SendInput{From: "leader", To: "dev", Content: "hello"}))
	must(s.SubmitPlan(ctx, "demo", SubmitPlanInput{Agent: "dev", Plan: "do it"}))
	must(s.SetBudget(ctx, "demo", 500, false))
	must(s.ReportCost(ctx, "demo", model.CostEvent{AgentName: "dev", CostCents: 12.5, InputTokens: 3}))
}

func TestSnapshotRestoreBringsBackState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedTeam(t, s)
	before := readState(t, s)

	meta, err := s.CreateSnapshot(ctx, "demo", "before change")
	if err != nil {
		t.Fatal(err)
	}
	// Two unread messages: "hello" to dev, and the plan notice to the leader.
	if meta.TaskCount != 2 || meta.MemberCount != 2 || meta.MessageCount != 2 || meta.PlanCount != 1 ||
		meta.CostEventCount != 1 || meta.TeamName != "demo" || meta.Tag != "before change" {
		t.Fatalf("meta: %+v", meta)
	}

	// Change everything the snapshot holds.
	s.SetTeamPrompt(ctx, "demo", "changed")
	s.AddMember(ctx, "demo", AddMemberInput{Name: "late"})
	s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "third"})
	tasks, _ := s.ListTasks(ctx, "demo", TaskFilter{})
	for _, task := range tasks {
		if task.Subject == "first" {
			if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusCompleted)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	s.Receive(ctx, "demo", "dev", "", 10)
	s.SubmitPlan(ctx, "demo", SubmitPlanInput{Agent: "dev", Plan: "again"})
	s.SetBudget(ctx, "demo", 900, false)
	if equalState(before, readState(t, s)) {
		t.Fatal("the changes did not change the state")
	}

	dry, err := s.RestoreSnapshot(ctx, "demo", meta.ID, true)
	if err != nil || !dry.DryRun || dry.Tasks != 2 || dry.Inboxes != 2 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if equalState(before, readState(t, s)) {
		t.Fatal("a dry run restored")
	}

	if _, err := s.RestoreSnapshot(ctx, "demo", meta.ID, false); err != nil {
		t.Fatal(err)
	}
	if after := readState(t, s); !equalState(before, after) {
		t.Fatalf("restored state differs:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestRestoreRefusesWhileAgentsRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedTeam(t, s)
	meta, err := s.CreateSnapshot(ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "dev", Backend: "tmux"}); err != nil {
		t.Fatal(err)
	}
	s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "third"})
	if _, err := s.RestoreSnapshot(ctx, "demo", meta.ID, false); KindOf(err) != KindConflict {
		t.Fatalf("want conflict, got %v", err)
	}
	if tasks, _ := s.ListTasks(ctx, "demo", TaskFilter{}); len(tasks) != 3 {
		t.Fatalf("a refused restore changed tasks: %d", len(tasks))
	}
	if _, err := s.AgentExited(ctx, "demo", "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreSnapshot(ctx, "demo", meta.ID, false); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRecreatesDeletedTeam(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedTeam(t, s)
	before := readState(t, s)
	meta, err := s.CreateSnapshot(ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteTeam(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSnapshots(ctx, "demo")
	if err != nil || len(list) != 1 {
		t.Fatalf("snapshots after team deletion: %v %v", list, err)
	}
	if _, err := s.RestoreSnapshot(ctx, "demo", meta.ID, false); err != nil {
		t.Fatal(err)
	}
	if after := readState(t, s); !equalState(before, after) {
		t.Fatalf("before %+v\nafter  %+v", before, after)
	}
}

func TestSnapshotIDsListAndDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	newTeam(t, s)
	a, err := s.CreateSnapshot(ctx, "demo", "x/y z")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateSnapshot(ctx, "demo", "x/y z")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || len(a.ID) < len("20060102T150405-x-y-z") {
		t.Fatalf("ids %q %q", a.ID, b.ID)
	}
	list, err := s.ListSnapshots(ctx, "demo")
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %v", list, err)
	}
	if err := s.DeleteSnapshot(ctx, "demo", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSnapshot(ctx, "demo", a.ID); KindOf(err) != KindNotFound {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := s.RestoreSnapshot(ctx, "demo", a.ID, true); KindOf(err) != KindNotFound {
		t.Fatalf("restore of a deleted snapshot: %v", err)
	}
	if _, err := s.CreateSnapshot(ctx, "nope", ""); KindOf(err) != KindNotFound {
		t.Fatalf("snapshot of an unknown team: %v", err)
	}
}

func TestRestoreSkipsColumnsTheSchemaLacks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedTeam(t, s)
	meta, err := s.CreateSnapshot(ctx, "demo", "")
	if err != nil {
		t.Fatal(err)
	}
	// A snapshot from a newer version may carry columns this schema lacks.
	if _, err := s.db.Exec(`UPDATE snapshots SET bundle = json_set(bundle, '$.tables.tasks[0].future_col', 1, '$.team.future_col', 'x') WHERE id = ?`, meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreSnapshot(ctx, "demo", meta.ID, false); err != nil {
		t.Fatal(err)
	}
}
