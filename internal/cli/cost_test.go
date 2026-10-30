package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestCostReportShowAndEnforcedBudget(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	h.run("team", "add-member", "demo", "alice")

	var ev map[string]any
	h.json(&ev, "cost", "report", "demo", "-a", "alice", "--input-tokens", "100", "--output-tokens", "20", "--cost-cents", "30", "--model", "m1")
	if ev["agentName"] != "alice" || ev["inputTokens"] != float64(100) || ev["costCents"] != float64(30) || ev["source"] != "report" || ev["reportedAt"] == "" {
		t.Fatalf("report = %v", ev)
	}
	if out, ok := h.run("cost", "report", "nope", "--cost-cents", "1"); ok {
		t.Fatalf("report to a missing team succeeded: %s", out)
	}

	var set map[string]any
	h.json(&set, "cost", "budget", "demo", "0.5", "--enforce")
	if set["status"] != "set" || set["budgetDollars"] != 0.5 || set["enforce"] != true {
		t.Fatalf("budget = %v", set)
	}
	out, _ := h.run("cost", "report", "demo", "-a", "alice", "--cost-cents", "25")
	if !strings.Contains(out, "WARNING: Budget exceeded! Spent $0.55 / $0.50") {
		t.Fatalf("human report output %q", out)
	}

	var view struct {
		Summary struct {
			TotalCostCents float64            `json:"totalCostCents"`
			ByAgent        map[string]float64 `json:"byAgent"`
			EventCount     int                `json:"eventCount"`
		} `json:"summary"`
		BudgetCents   float64 `json:"budget_cents"`
		BudgetEnforce bool    `json:"budgetEnforce"`
		Events        []model.CostEvent
	}
	h.json(&view, "cost", "show", "demo")
	if view.Summary.TotalCostCents != 55 || view.Summary.EventCount != 2 || view.BudgetCents != 50 || !view.BudgetEnforce || len(view.Events) != 2 {
		t.Fatalf("show = %+v", view)
	}

	var task model.Task
	h.json(&task, "task", "create", "demo", "next")
	if out, ok := h.run("--json", "task", "update", "demo", task.ID, "-s", "in_progress"); ok || !strings.Contains(out, "budget is exhausted") {
		t.Fatalf("claim with a spent budget: ok=%v %s", ok, out)
	}
	out, ok := h.run("--json", "spawn", "subprocess", "--team", "demo", "-n", "bob", "--task", "x", "--", "true")
	if ok || !strings.Contains(out, "budget is exhausted") {
		t.Fatalf("spawn with a spent budget: ok=%v %s", ok, out)
	}

	h.json(&set, "cost", "budget", "demo", "0")
	if _, ok := h.run("task", "update", "demo", task.ID, "-s", "in_progress"); !ok {
		t.Fatal("claim still blocked after removing the budget")
	}
}

func TestCostSyncRecordsClaudeSessionTokens(t *testing.T) {
	h := newHarness(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	h.run("team", "spawn-team", "demo")
	ctx := context.Background()
	if _, err := h.api.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", AgentID: "a1", RunID: "run1", Backend: "subprocess"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.SaveSession(ctx, model.Session{Team: "demo", Agent: "alice", RunID: "run1", Client: "claude", SessionID: "sess-1", Command: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	transcript := filepath.Join(home, ".claude", "projects", "-work", "sess-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(output int) {
		line := `{"type":"assistant","message":{"id":"m1","model":"claude-x","usage":{"input_tokens":5,"cache_read_input_tokens":95,"output_tokens":` +
			strconv.Itoa(output) + `}}}` + "\n"
		if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(7)
	var res struct{ Recorded []model.CostEvent }
	h.json(&res, "cost", "sync", "demo")
	if len(res.Recorded) != 1 || res.Recorded[0].InputTokens != 100 || res.Recorded[0].OutputTokens != 7 || res.Recorded[0].Model != "claude-x" {
		t.Fatalf("sync = %+v", res)
	}
	// A later capture of the same session replaces the totals.
	write(9)
	h.json(&res, "cost", "sync", "demo", "-a", "alice")
	var view model.CostView
	h.json(&view, "cost", "show", "demo")
	if view.Summary.EventCount != 1 || view.Summary.TotalOutputTokens != 9 || view.Summary.ByAgentTokens["alice"].InputTokens != 100 {
		t.Fatalf("show after resync = %+v", view.Summary)
	}
}
