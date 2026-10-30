package store

import (
	"context"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestPlanSubmitAsksLeaderAndApprovalTellsAgent(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()

	body := strings.Repeat("步", 250)
	p, err := s.SubmitPlan(ctx, "demo", SubmitPlanInput{Agent: "alice", Plan: body})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ID) != 12 || p.Status != model.PlanPending {
		t.Fatalf("plan = %+v", p)
	}
	msgs, _ := s.Receive(ctx, "demo", "leader", "", 10)
	if len(msgs) != 1 || msgs[0].Type != "plan_approval_request" || msgs[0].RequestID != p.ID ||
		msgs[0].Plan != body || msgs[0].Summary != strings.Repeat("步", 200) || msgs[0].From != "alice" {
		t.Fatalf("leader got %+v", msgs)
	}

	approved, err := s.DecidePlan(ctx, "demo", p.ID, true, DecidePlanInput{Agent: "alice", Caller: "leader", Feedback: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != model.PlanApproved || approved.DecidedBy != "leader" || approved.DecidedAt == "" || approved.Feedback != "go" {
		t.Fatalf("approved = %+v", approved)
	}
	msgs, _ = s.Receive(ctx, "demo", "alice", "", 10)
	if len(msgs) != 1 || msgs[0].Type != "plan_approved" || msgs[0].RequestID != p.ID || msgs[0].Feedback != "go" {
		t.Fatalf("alice got %+v", msgs)
	}
	got, err := s.GetPlan(ctx, "demo", p.ID)
	if err != nil || got.Status != model.PlanApproved || got.Body != body {
		t.Fatalf("stored = %+v, %v", got, err)
	}
}

func TestPlanDecisionRules(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice", "bob")
	ctx := context.Background()
	p, err := s.SubmitPlan(ctx, "demo", SubmitPlanInput{Agent: "alice", Plan: "do it", Summary: "short"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   DecidePlanInput
		kind Kind
	}{
		{"not the leader", DecidePlanInput{Agent: "alice", Caller: "bob"}, KindConflict},
		{"wrong agent", DecidePlanInput{Agent: "bob", Caller: "leader"}, KindInvalid},
	}
	for _, c := range cases {
		if _, err := s.DecidePlan(ctx, "demo", p.ID, true, c.in); KindOf(err) != c.kind {
			t.Errorf("%s: err = %v, want kind %d", c.name, err, c.kind)
		}
	}
	if _, err := s.DecidePlan(ctx, "demo", "missing", true, DecidePlanInput{Caller: "leader"}); KindOf(err) != KindNotFound {
		t.Errorf("missing plan: %v", err)
	}
	if _, err := s.DecidePlan(ctx, "demo", p.ID, false, DecidePlanInput{Agent: "alice", Caller: "leader"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DecidePlan(ctx, "demo", p.ID, true, DecidePlanInput{Agent: "alice", Caller: "leader"}); KindOf(err) != KindConflict ||
		!strings.Contains(err.Error(), "already rejected") {
		t.Errorf("deciding twice: %v", err)
	}
	msgs, _ := s.Receive(ctx, "demo", "alice", "", 10)
	if len(msgs) != 1 || msgs[0].Type != "plan_rejected" || msgs[0].Feedback != "" {
		t.Fatalf("alice got %+v", msgs)
	}
	if _, err := s.SubmitPlan(ctx, "demo", SubmitPlanInput{Agent: "alice", Plan: "  "}); KindOf(err) != KindInvalid {
		t.Errorf("empty plan: %v", err)
	}
	if _, err := s.SubmitPlan(ctx, "nope", SubmitPlanInput{Agent: "alice", Plan: "x"}); KindOf(err) != KindNotFound {
		t.Errorf("unknown team: %v", err)
	}
}

func TestListPlansFilters(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice", "bob")
	ctx := context.Background()
	a1, _ := s.SubmitPlan(ctx, "demo", SubmitPlanInput{Agent: "alice", Plan: "a1"})
	a2, _ := s.SubmitPlan(ctx, "demo", SubmitPlanInput{Agent: "alice", Plan: "a2"})
	s.SubmitPlan(ctx, "demo", SubmitPlanInput{Agent: "bob", Plan: "b1"})
	s.DecidePlan(ctx, "demo", a1.ID, true, DecidePlanInput{Caller: "leader"})

	ids := func(plans []model.Plan) (out []string) {
		for _, p := range plans {
			out = append(out, p.Agent+":"+p.Body)
		}
		return out
	}
	all, _ := s.ListPlans(ctx, "demo", "", "")
	alice, _ := s.ListPlans(ctx, "demo", "alice", "")
	pending, _ := s.ListPlans(ctx, "demo", "alice", model.PlanPending)
	if len(all) != 3 || strings.Join(ids(alice), ",") != "alice:a1,alice:a2" || len(pending) != 1 || pending[0].ID != a2.ID {
		t.Fatalf("all=%v alice=%v pending=%v", ids(all), ids(alice), ids(pending))
	}
	if _, err := s.ListPlans(ctx, "demo", "", "done"); KindOf(err) != KindInvalid {
		t.Errorf("bad status: %v", err)
	}
}

func TestPlanRequiredMemberWaitsForApproval(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "bob")
	ctx := context.Background()
	if _, err := s.AddMember(ctx, "demo", AddMemberInput{Name: "alice", PlanRequired: true}); err != nil {
		t.Fatal(err)
	}
	team, _ := s.GetTeam(ctx, "demo")
	for _, m := range team.Members {
		if m.Name == "alice" && !m.PlanRequired || m.Name == "bob" && m.PlanRequired {
			t.Fatalf("member %+v", m)
		}
	}
	task, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "work", Owner: "alice"})
	other, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "free", Owner: "bob"})

	for _, status := range []model.TaskStatus{model.StatusInProgress, model.StatusCompleted} {
		_, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(status), Caller: "alice"})
		if KindOf(err) != KindConflict || !strings.Contains(err.Error(), "team-worker plan submit demo alice") {
			t.Fatalf("%s before approval: %v", status, err)
		}
	}
	// Other changes and other members are not affected.
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Description: ptr("notes"), Caller: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, "demo", other.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "bob"}); err != nil {
		t.Fatal(err)
	}

	p, _ := s.SubmitPlan(ctx, "demo", SubmitPlanInput{Agent: "alice", Plan: "steps"})
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice"}); KindOf(err) != KindConflict {
		t.Fatalf("pending plan must still block: %v", err)
	}
	if _, err := s.DecidePlan(ctx, "demo", p.ID, true, DecidePlanInput{Agent: "alice", Caller: "leader"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice"}); err != nil {
		t.Fatalf("after approval: %v", err)
	}

	if err := s.SetPlanRequired(ctx, "demo", "bob", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, "demo", other.ID, UpdateTaskInput{Status: ptr(model.StatusCompleted), Caller: "bob"}); KindOf(err) != KindConflict {
		t.Fatalf("bob after SetPlanRequired: %v", err)
	}
	if err := s.SetPlanRequired(ctx, "demo", "nobody", "", true); KindOf(err) != KindNotFound {
		t.Fatalf("unknown member: %v", err)
	}
}
