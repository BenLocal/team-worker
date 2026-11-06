package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func TestDecisionRequestRoutes(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewServer(New(st, nil))
	t.Cleanup(ts.Close)
	c := client.NewHTTP(ts.URL, ts.Client())
	ctx := context.Background()
	if _, err := c.CreateTeam(ctx, store.CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}
	r, err := c.CreateDecisionRequest(ctx, "demo", store.CreateDecisionInput{From: "leader", Summary: "Pick one",
		Options: []model.DecisionOption{{Key: "A", Label: "a"}, {Key: "B", Label: "b"}}, Recommended: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DecideRequest(ctx, "demo", r.ID, store.DecideInput{Action: "approve", Choice: "Z"}); err == nil ||
		!strings.Contains(err.Error(), "Choose one of the options: A, B") {
		t.Fatalf("bad choice: %v", err)
	}
	// The AI reviewer decides inside the daemon only.
	var apiErr *client.APIError
	if _, err := c.DecideRequest(ctx, "demo", r.ID, store.DecideInput{Action: "approve", Choice: "A", DecidedBy: "auto"}); !errors.As(err, &apiErr) || apiErr.Status != 400 {
		t.Fatalf("decide as auto over HTTP: %v", err)
	}
	decided, err := c.DecideRequest(ctx, "demo", r.ID, store.DecideInput{Action: "approve", Choice: "B"})
	if err != nil || decided.DecidedBy != "operator" || decided.Status != model.DecisionApproved {
		t.Fatalf("decide: %+v %v", decided, err)
	}
	if _, err := c.DecideRequest(ctx, "demo", r.ID, store.DecideInput{Action: "reject", Reply: "x"}); !errors.As(err, &apiErr) || apiErr.Status != 409 {
		t.Fatalf("decide twice: %v", err)
	}
	list, err := c.ListDecisionRequests(ctx, "demo", model.DecisionApproved)
	if err != nil || len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("list: %+v %v", list, err)
	}
	if got, err := c.GetDecisionRequest(ctx, "demo", r.ID); err != nil || got.Choice != "B" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if a, err := c.SetAutoReview(ctx, "demo", model.AutoReview{Enabled: true, Model: "m1"}); err != nil || !a.Enabled {
		t.Fatalf("set auto review: %+v %v", a, err)
	}
	if a, err := c.AutoReview(ctx, "demo"); err != nil || !a.Enabled || a.Model != "m1" {
		t.Fatalf("auto review: %+v %v", a, err)
	}
}

func newReviewFixture(t *testing.T, enabled bool) (*AutoReviewer, *store.Store, *[]string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hub := NewHub()
	st.OnChange = hub.Notify
	ctx := context.Background()
	if _, err := st.CreateTeam(ctx, store.CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}
	st.SetTeamPrompt(ctx, "demo", "You may refactor code in the repo.")
	st.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "refactor storage"})
	st.Send(ctx, "demo", store.SendInput{From: "alice", To: "leader", Content: "storage layer is messy"})
	if _, err := st.SetAutoReview(ctx, "demo", model.AutoReview{Enabled: enabled, Model: "group/x"}); err != nil {
		t.Fatal(err)
	}
	var prompts []string
	a := NewAutoReviewer(st, hub, slog.New(slog.DiscardHandler))
	a.Review = func(context.Context, string, string) (string, error) { return "", errors.New("no reviewer set") }
	return a, st, &prompts
}

func createABC(t *testing.T, st *store.Store, human bool) model.DecisionRequest {
	t.Helper()
	r, err := st.CreateDecisionRequest(context.Background(), "demo", store.CreateDecisionInput{From: "leader", Summary: "Storage layout",
		Body: "We can keep, move or split it.", HumanOnly: human, Recommended: "C",
		Options: []model.DecisionOption{{Key: "A", Label: "keep"}, {Key: "B", Label: "move"}, {Key: "C", Label: "split"}}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAutoReviewerApproves(t *testing.T) {
	a, st, prompts := newReviewFixture(t, true)
	ctx := context.Background()
	r := createABC(t, st, false)
	var gotModel string
	a.Review = func(_ context.Context, m, prompt string) (string, error) {
		gotModel = m
		*prompts = append(*prompts, prompt)
		return "Thinking {not json}\n```json\n{\"decision\":\"approve\",\"choice\":\"C\",\"reply\":\"split it\",\"reason\":\"allowed refactor\",\"extra\":{\"a\":1}}\n```\n", nil
	}
	if err := a.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetDecisionRequest(ctx, "demo", r.ID)
	if got.Status != model.DecisionApproved || got.Choice != "C" || got.Reply != "split it" || got.DecidedBy != "auto:group/x" ||
		got.AutoReason != "allowed refactor" {
		t.Fatalf("decided = %+v", got)
	}
	if gotModel != "group/x" || len(*prompts) != 1 {
		t.Fatalf("model %q, %d prompts", gotModel, len(*prompts))
	}
	p := (*prompts)[0]
	for _, want := range []string{"on behalf of the human operator", "ESCALATE", "You may refactor code in the repo.",
		"Summary: Storage layout", "- C: split", "recommends C", "storage layer is messy", "refactor storage", `"decision":"approve|reject|answer|escalate"`} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt misses %q:\n%s", want, p)
		}
	}
	msgs, _ := st.Receive(ctx, "demo", "leader", "", 10)
	last := msgs[len(msgs)-1]
	if last.Type != "decision_result" || last.Reason != "allowed refactor" || last.From != "auto:group/x" {
		t.Fatalf("leader got %+v", last)
	}
	// Reviewed once: nothing is left to review.
	*prompts = nil
	a.Drain(ctx)
	if len(*prompts) != 0 {
		t.Fatal("reviewed twice")
	}
}

func TestAutoReviewerLeavesRequestsPending(t *testing.T) {
	cases := []struct {
		name, out string
		err       error
		note      string
	}{
		{"escalate", `{"decision":"escalate","reason":"deletes data"}`, nil, "AI escalated: deletes data"},
		{"invalid output", "I think C is best.", nil, "AI review gave no usable decision"},
		{"unknown decision", `{"decision":"maybe","reason":"x"}`, nil, "unknown decision"},
		{"no reason", `{"decision":"approve","choice":"C"}`, nil, "no reason"},
		{"bad choice", `{"decision":"approve","choice":"Z","reason":"x"}`, nil, "AI decision was not valid: Choose one of the options"},
		{"command failed", "", errors.New("exit status 1"), "AI review failed: exit status 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, st, _ := newReviewFixture(t, true)
			ctx := context.Background()
			r := createABC(t, st, false)
			calls := 0
			a.Review = func(context.Context, string, string) (string, error) { calls++; return c.out, c.err }
			if err := a.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			got, _ := st.GetDecisionRequest(ctx, "demo", r.ID)
			if got.Status != model.DecisionPending || !strings.Contains(got.AutoNote, c.note) {
				t.Fatalf("got %+v, want note %q", got, c.note)
			}
			// Not retried automatically.
			a.Drain(ctx)
			if calls != 1 {
				t.Fatalf("reviewed %d times", calls)
			}
			// The operator still decides it.
			if _, err := st.DecideRequest(ctx, "demo", r.ID, store.DecideInput{Action: "approve", Choice: "A", DecidedBy: "operator"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAutoReviewerSkipsHumanOnlyAndDisabledTeams(t *testing.T) {
	for _, c := range []struct {
		name           string
		enabled, human bool
	}{{"human only", true, true}, {"disabled team", false, false}} {
		t.Run(c.name, func(t *testing.T) {
			a, st, _ := newReviewFixture(t, c.enabled)
			ctx := context.Background()
			r := createABC(t, st, c.human)
			a.Review = func(context.Context, string, string) (string, error) {
				t.Error("the reviewer must not run")
				return "", nil
			}
			if err := a.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			if got, _ := st.GetDecisionRequest(ctx, "demo", r.ID); got.Status != model.DecisionPending || got.AutoNote != "" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestAutoReviewerDropsDecisionsTheOperatorMadeMeanwhile(t *testing.T) {
	a, st, _ := newReviewFixture(t, true)
	ctx := context.Background()
	r := createABC(t, st, false)
	a.Review = func(context.Context, string, string) (string, error) {
		if _, err := st.DecideRequest(ctx, "demo", r.ID, store.DecideInput{Action: "reject", Reply: "no", DecidedBy: "operator"}); err != nil {
			t.Fatal(err)
		}
		return `{"decision":"approve","choice":"C","reason":"x"}`, nil
	}
	if err := a.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetDecisionRequest(ctx, "demo", r.ID); got.Status != model.DecisionRejected || got.DecidedBy != "operator" {
		t.Fatalf("got %+v", got)
	}
}

func TestRunReviewCommandPassesPromptAndModel(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "review.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" > \""+dir+"/args\"\ncat > \""+dir+"/stdin\"\necho '{\"decision\":\"escalate\",\"reason\":\"r\"}'\n"), 0o700)
	t.Setenv("TEAM_WORKER_AUTO_REVIEW_COMMAND", script+" --flag")
	out, err := RunReviewCommand(context.Background(), "group/x", "the prompt")
	if err != nil || !strings.Contains(out, `"escalate"`) {
		t.Fatalf("out %q, %v", out, err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if string(args) != "--flag --model group/x\n" || string(stdin) != "the prompt" {
		t.Fatalf("args %q stdin %q", args, stdin)
	}
	os.WriteFile(script, []byte("#!/bin/sh\necho boom >&2\nexit 3\n"), 0o700)
	if _, err := RunReviewCommand(context.Background(), "", "p"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failure: %v", err)
	}
	if got := reviewCommand(""); strings.Join(got, " ") != script+" --flag" {
		t.Fatalf("argv %v", got)
	}
	t.Setenv("TEAM_WORKER_AUTO_REVIEW_COMMAND", "")
	if got := reviewCommand("m"); !slices.Equal(got, []string{"claude", "-p", "--tools", "", "--no-session-persistence", "--model", "m"}) {
		t.Fatalf("default argv %v", got)
	}
}
