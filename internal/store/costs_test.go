package store

import (
	"context"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestCostSummaryCoversTeamWhileEventsFilterByAgent(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice", "bob")
	ctx := context.Background()
	for _, e := range []model.CostEvent{
		{AgentName: "alice", InputTokens: 100, OutputTokens: 10, CostCents: 12.5},
		{AgentName: "bob", InputTokens: 50, OutputTokens: 5, CostCents: 2},
	} {
		if _, err := s.ReportCost(ctx, "demo", e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RecordSessionUsage(ctx, "demo", model.CostEvent{AgentName: "alice", SessionID: "s1", InputTokens: 1000, OutputTokens: 40}); err != nil {
		t.Fatal(err)
	}
	v, err := s.Costs(ctx, "demo", "bob")
	if err != nil {
		t.Fatal(err)
	}
	sum := v.Summary
	if sum.TotalCostCents != 14.5 || sum.TotalInputTokens != 1150 || sum.TotalOutputTokens != 55 || sum.EventCount != 3 ||
		sum.ByAgent["alice"] != 12.5 || sum.ByAgentTokens["alice"] != (model.TokenCount{InputTokens: 1100, OutputTokens: 50}) {
		t.Fatalf("summary = %+v", sum)
	}
	if len(v.Events) != 1 || v.Events[0].AgentName != "bob" || v.Events[0].Source != "report" || len(v.Events[0].ID) != 8 {
		t.Fatalf("events = %+v", v.Events)
	}
	if _, err := s.ReportCost(ctx, "missing", model.CostEvent{AgentName: "x"}); KindOf(err) != KindNotFound {
		t.Fatalf("report to missing team: %v", err)
	}
	if _, err := s.ReportCost(ctx, "demo", model.CostEvent{AgentName: "alice", CostCents: -1}); KindOf(err) != KindInvalid {
		t.Fatalf("negative cost: %v", err)
	}
}

func TestSessionUsageReplacesEarlierCapture(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	first, err := s.RecordSessionUsage(ctx, "demo", model.CostEvent{AgentName: "alice", SessionID: "s1", InputTokens: 10, OutputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RecordSessionUsage(ctx, "demo", model.CostEvent{AgentName: "alice", SessionID: "s1", InputTokens: 30, OutputTokens: 3, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("capture got a new row: %s then %s", first.ID, second.ID)
	}
	if _, err := s.RecordSessionUsage(ctx, "demo", model.CostEvent{AgentName: "alice", SessionID: "s2", InputTokens: 5}); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Costs(ctx, "demo", "")
	if v.Summary.EventCount != 2 || v.Summary.TotalInputTokens != 35 {
		t.Fatalf("summary = %+v", v.Summary)
	}
}

func budgetMessages(t *testing.T, s *Store) []string {
	t.Helper()
	msgs, err := s.Peek(context.Background(), "demo", "leader", "")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		if m.From == "team-worker" {
			out = append(out, m.Content)
		}
	}
	return out
}

func TestBudgetAlertsLeaderOnceAndOnlyWarnsByDefault(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	if _, err := s.SetBudget(ctx, "demo", 100, false); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.ReportCost(ctx, "demo", model.CostEvent{AgentName: "alice", CostCents: 60}); err != nil {
			t.Fatal(err)
		}
	}
	msgs := budgetMessages(t, s)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "$1.20 of $1.00") || !strings.Contains(msgs[0], "only warn") {
		t.Fatalf("alerts = %q", msgs)
	}
	task, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "work"})
	if _, err := s.UpdateTask(ctx, "demo", task.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice"}); err != nil {
		t.Fatalf("warn-only budget blocked a claim: %v", err)
	}
	// Setting the budget again re-arms the alert, which fires at once when spent.
	if _, err := s.SetBudget(ctx, "demo", 150, true); err != nil {
		t.Fatal(err)
	}
	if msgs := budgetMessages(t, s); len(msgs) != 2 || !strings.Contains(msgs[1], "blocked") {
		t.Fatalf("alerts after re-arming = %q", msgs)
	}
}

func TestEnforcedBudgetBlocksNewClaims(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	busy, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "busy"})
	idle, _ := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "idle"})
	if _, err := s.UpdateTask(ctx, "demo", busy.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetBudget(ctx, "demo", 50, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.Budget(ctx, "demo"); b.Exhausted {
		t.Fatal("budget exhausted before any cost")
	}
	if _, err := s.ReportCost(ctx, "demo", model.CostEvent{AgentName: "alice", CostCents: 50}); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.Budget(ctx, "demo"); !b.Exhausted || b.SpentCents != 50 {
		t.Fatalf("budget = %+v", b)
	}
	_, err := s.UpdateTask(ctx, "demo", idle.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice"})
	if KindOf(err) != KindConflict || !strings.Contains(err.Error(), "budget is exhausted") {
		t.Fatalf("claim with spent budget: %v", err)
	}
	// Work already under way can still be finished.
	if _, err := s.UpdateTask(ctx, "demo", busy.ID, UpdateTaskInput{Status: ptr(model.StatusCompleted), Caller: "alice"}); err != nil {
		t.Fatalf("completing a running task: %v", err)
	}
	// An unlimited budget never blocks.
	if _, err := s.SetBudget(ctx, "demo", 0, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, "demo", idle.ID, UpdateTaskInput{Status: ptr(model.StatusInProgress), Caller: "alice"}); err != nil {
		t.Fatalf("claim with no budget: %v", err)
	}
}
