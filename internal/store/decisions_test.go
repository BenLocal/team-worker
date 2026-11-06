package store

import (
	"context"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func abcRequest() CreateDecisionInput {
	return CreateDecisionInput{From: "leader", Summary: "Which storage?", Body: "Details\nhere",
		Options:     []model.DecisionOption{{Key: "A", Label: "keep as is"}, {Key: "B", Label: "move"}, {Key: "C", Label: "split"}},
		Recommended: "C"}
}

func TestDecisionRequestTellsOperatorAndDecisionTellsRequester(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()

	r, err := s.CreateDecisionRequest(ctx, "demo", abcRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ID) != 12 || r.Status != model.DecisionPending || len(r.Options) != 3 || r.Recommended != "C" {
		t.Fatalf("request = %+v", r)
	}
	msgs, _ := s.Receive(ctx, "demo", OperatorInbox, "", 10)
	if len(msgs) != 1 || msgs[0].Type != "decision_request" || msgs[0].RequestID != r.ID || msgs[0].Summary != "Which storage?" ||
		!strings.Contains(msgs[0].Content, "- C: split (recommended)") || !strings.Contains(msgs[0].Content, "request decide demo "+r.ID) {
		t.Fatalf("operator got %+v", msgs)
	}

	decided, err := s.DecideRequest(ctx, "demo", r.ID, DecideInput{Action: "approve", Choice: "C", Reply: "go", DecidedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if decided.Status != model.DecisionApproved || decided.Choice != "C" || decided.Reply != "go" || decided.DecidedBy != "operator" || decided.DecidedAt == "" {
		t.Fatalf("decided = %+v", decided)
	}
	msgs, _ = s.Receive(ctx, "demo", "leader", "", 10)
	if len(msgs) != 1 || msgs[0].Type != "decision_result" || msgs[0].RequestID != r.ID || msgs[0].Status != "approved" ||
		msgs[0].Choice != "C" || msgs[0].Feedback != "go" || !strings.Contains(msgs[0].Content, "approved C (split) by operator") ||
		!strings.Contains(msgs[0].Content, "Note: go") {
		t.Fatalf("leader got %+v", msgs)
	}
	got, err := s.GetDecisionRequest(ctx, "demo", r.ID)
	if err != nil || got.Status != model.DecisionApproved || got.Body != "Details\nhere" || len(got.Options) != 3 {
		t.Fatalf("stored = %+v, %v", got, err)
	}
	if _, err := s.DecideRequest(ctx, "demo", r.ID, DecideInput{Action: "reject", Reply: "no", DecidedBy: "operator"}); KindOf(err) != KindConflict ||
		!strings.Contains(err.Error(), "already approved") {
		t.Errorf("deciding a human decision twice: %v", err)
	}
}

func TestDecisionRequestValidation(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	bad := []func(*CreateDecisionInput){
		func(in *CreateDecisionInput) { in.Summary = "  " },
		func(in *CreateDecisionInput) { in.Summary = "two\nlines" },
		func(in *CreateDecisionInput) { in.Recommended = "D" },
		func(in *CreateDecisionInput) { in.Options[1].Key = "A" },
		func(in *CreateDecisionInput) { in.Options[0].Label = "" },
		func(in *CreateDecisionInput) { in.Options[0].Key = "A B" },
		func(in *CreateDecisionInput) { in.From = "" },
	}
	for i, change := range bad {
		in := abcRequest()
		change(&in)
		if _, err := s.CreateDecisionRequest(ctx, "demo", in); KindOf(err) != KindInvalid {
			t.Errorf("case %d: %v", i, err)
		}
	}
	if _, err := s.CreateDecisionRequest(ctx, "nope", abcRequest()); KindOf(err) != KindNotFound {
		t.Errorf("unknown team: %v", err)
	}

	r, _ := s.CreateDecisionRequest(ctx, "demo", abcRequest())
	plain, err := s.CreateDecisionRequest(ctx, "demo", CreateDecisionInput{From: "leader", Summary: "May I deploy?"})
	if err != nil || len(plain.Options) != 0 {
		t.Fatalf("plain = %+v, %v", plain, err)
	}
	cases := []struct {
		name string
		id   string
		in   DecideInput
		kind Kind
	}{
		{"choice not an option", r.ID, DecideInput{Action: "approve", Choice: "D", DecidedBy: "operator"}, KindInvalid},
		{"no choice with options", r.ID, DecideInput{Action: "approve", DecidedBy: "operator"}, KindInvalid},
		{"choice without options", plain.ID, DecideInput{Action: "approve", Choice: "A", DecidedBy: "operator"}, KindInvalid},
		{"reject without reason", r.ID, DecideInput{Action: "reject", DecidedBy: "operator"}, KindInvalid},
		{"empty answer", r.ID, DecideInput{Action: "answer", Reply: " ", DecidedBy: "operator"}, KindInvalid},
		{"unknown action", r.ID, DecideInput{Action: "maybe", DecidedBy: "operator"}, KindInvalid},
		{"member decides", r.ID, DecideInput{Action: "approve", Choice: "A", DecidedBy: "alice"}, KindConflict},
		{"leader decides", r.ID, DecideInput{Action: "approve", Choice: "A", DecidedBy: "leader"}, KindConflict},
		{"human as auto", r.ID, DecideInput{Action: "approve", Choice: "A", DecidedBy: "auto:x"}, KindInvalid},
		{"missing request", "missing", DecideInput{Action: "answer", Reply: "x", DecidedBy: "operator"}, KindNotFound},
	}
	for _, c := range cases {
		if _, err := s.DecideRequest(ctx, "demo", c.id, c.in); KindOf(err) != c.kind {
			t.Errorf("%s: err = %v, want kind %d", c.name, err, c.kind)
		}
	}
	if got, err := s.DecideRequest(ctx, "demo", plain.ID, DecideInput{Action: "approve", DecidedBy: "operator"}); err != nil || got.Status != model.DecisionApproved {
		t.Errorf("approve without options: %+v %v", got, err)
	}
	ans, err := s.DecideRequest(ctx, "demo", r.ID, DecideInput{Action: "answer", Reply: "use B next week", DecidedBy: "operator"})
	if err != nil || ans.Status != model.DecisionAnswered {
		t.Errorf("answer: %+v %v", ans, err)
	}
	rej, _ := s.CreateDecisionRequest(ctx, "demo", abcRequest())
	if got, err := s.DecideRequest(ctx, "demo", rej.ID, DecideInput{Action: "reject", Reply: "too risky", DecidedBy: "operator"}); err != nil ||
		got.Status != model.DecisionRejected || got.Reply != "too risky" {
		t.Errorf("reject: %+v %v", got, err)
	}
	pending, _ := s.ListDecisionRequests(ctx, "demo", model.DecisionPending)
	all, _ := s.ListDecisionRequests(ctx, "demo", "")
	if len(pending) != 0 || len(all) != 3 {
		t.Errorf("pending=%d all=%d", len(pending), len(all))
	}
	if _, err := s.ListDecisionRequests(ctx, "demo", "done"); KindOf(err) != KindInvalid {
		t.Errorf("bad status: %v", err)
	}
}

func TestOperatorOverridesOnlyAutoDecisions(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	if _, err := s.SetAutoReview(ctx, "demo", model.AutoReview{Enabled: true, Model: "group/x"}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.CreateDecisionRequest(ctx, "demo", abcRequest())
	human, _ := s.CreateDecisionRequest(ctx, "demo", CreateDecisionInput{From: "leader", Summary: "Wipe the disk?", HumanOnly: true})

	queue, err := s.AutoReviewQueue(ctx)
	if err != nil || len(queue) != 1 || queue[0].Request.ID != r.ID || queue[0].Model != "group/x" {
		t.Fatalf("queue = %+v, %v", queue, err)
	}
	if ok, err := s.ClaimAutoReview(ctx, "demo", human.ID); err != nil || ok {
		t.Fatalf("claimed a human-only request: %v %v", ok, err)
	}
	if ok, _ := s.ClaimAutoReview(ctx, "demo", r.ID); !ok {
		t.Fatal("claim failed")
	}
	if ok, _ := s.ClaimAutoReview(ctx, "demo", r.ID); ok {
		t.Fatal("claimed twice")
	}
	if queue, _ := s.AutoReviewQueue(ctx); len(queue) != 0 {
		t.Fatalf("claimed request still queued: %+v", queue)
	}
	if _, err := s.AutoDecideRequest(ctx, "demo", human.ID, DecideInput{Action: "approve", DecidedBy: "auto"}, "fine"); KindOf(err) != KindConflict {
		t.Errorf("auto decided a human-only request: %v", err)
	}
	if _, err := s.AutoDecideRequest(ctx, "demo", r.ID, DecideInput{Action: "approve", Choice: "C", DecidedBy: "operator"}, "fine"); KindOf(err) != KindInvalid {
		t.Errorf("auto decision under a human name: %v", err)
	}
	auto, err := s.AutoDecideRequest(ctx, "demo", r.ID, DecideInput{Action: "approve", Choice: "C", DecidedBy: "auto:group/x"}, "fits the prompt")
	if err != nil || auto.AutoReason != "fits the prompt" || auto.DecidedBy != "auto:group/x" {
		t.Fatalf("auto = %+v, %v", auto, err)
	}
	msgs, _ := s.Receive(ctx, "demo", "leader", "", 10)
	if len(msgs) != 1 || msgs[0].Reason != "fits the prompt" || !strings.Contains(msgs[0].Content, "AI auto review") ||
		!strings.Contains(msgs[0].Content, "AI reason: fits the prompt") {
		t.Fatalf("leader got %+v", msgs)
	}
	if _, err := s.AutoDecideRequest(ctx, "demo", r.ID, DecideInput{Action: "reject", Reply: "x", DecidedBy: "auto"}, "again"); KindOf(err) != KindConflict {
		t.Errorf("auto decided twice: %v", err)
	}

	over, err := s.DecideRequest(ctx, "demo", r.ID, DecideInput{Action: "reject", Reply: "not now", DecidedBy: "operator"})
	if err != nil || over.Status != model.DecisionRejected || over.DecidedBy != "operator" || over.AutoReason != "fits the prompt" {
		t.Fatalf("override = %+v, %v", over, err)
	}
	msgs, _ = s.Receive(ctx, "demo", "leader", "", 10)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "overrides the earlier AI decision (approved C (split) by auto:group/x)") ||
		msgs[0].Status != "rejected" || msgs[0].Reason != "" {
		t.Fatalf("override message %+v", msgs)
	}
	if _, err := s.DecideRequest(ctx, "demo", r.ID, DecideInput{Action: "answer", Reply: "y", DecidedBy: "operator"}); KindOf(err) != KindConflict {
		t.Errorf("overrode a human decision: %v", err)
	}

	if err := s.NoteAutoReview(ctx, "demo", human.ID, "escalated"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetDecisionRequest(ctx, "demo", human.ID); got.AutoNote != "escalated" || got.Status != model.DecisionPending {
		t.Errorf("noted = %+v", got)
	}
	s.SetAutoReview(ctx, "demo", model.AutoReview{})
	later, _ := s.CreateDecisionRequest(ctx, "demo", abcRequest())
	if ok, _ := s.ClaimAutoReview(ctx, "demo", later.ID); ok {
		t.Error("claimed with auto review off")
	}
	if a, err := s.AutoReviewSetting(ctx, "demo"); err != nil || a.Enabled || a.Model != "" {
		t.Errorf("setting = %+v %v", a, err)
	}
	if _, err := s.SetAutoReview(ctx, "nope", model.AutoReview{Enabled: true}); KindOf(err) != KindNotFound {
		t.Errorf("unknown team: %v", err)
	}
}
