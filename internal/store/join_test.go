package store

import (
	"context"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestJoinRequestApprovalAddsMemberAndNotifiesBothSides(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	ctx := context.Background()
	r, err := s.RequestJoin(ctx, "demo", JoinRequestInput{ProposedName: "alice", Capabilities: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(r.RequestID, "join-") || len(r.RequestID) != len("join-")+12 || r.Status != model.JoinPending {
		t.Fatalf("request = %+v", r)
	}
	leaderMsgs, _ := s.Peek(ctx, "demo", "leader", "")
	if len(leaderMsgs) != 1 || leaderMsgs[0].Type != "join_request" || leaderMsgs[0].RequestID != r.RequestID ||
		leaderMsgs[0].ProposedName != "alice" || leaderMsgs[0].Capabilities != "go" {
		t.Fatalf("leader inbox = %+v", leaderMsgs)
	}
	if _, err := s.RequestJoin(ctx, "demo", JoinRequestInput{ProposedName: "alice"}); KindOf(err) != KindConflict {
		t.Fatalf("duplicate pending request err = %v", err)
	}

	approved, err := s.ApproveJoin(ctx, "demo", r.RequestID, JoinDecisionInput{Caller: "leader", AssignedName: "alice-2"})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != model.JoinApproved || approved.AssignedName != "alice-2" || len(approved.AgentID) != 12 || approved.DecidedBy != "leader" {
		t.Fatalf("approved = %+v", approved)
	}
	team, _ := s.GetTeam(ctx, "demo")
	if len(team.Members) != 2 || team.Members[1].Name != "alice-2" || team.Members[1].AgentID != approved.AgentID ||
		team.Members[1].AgentType != "general-purpose" {
		t.Fatalf("members = %+v", team.Members)
	}
	memberMsgs, _ := s.Peek(ctx, "demo", "alice-2", "")
	if len(memberMsgs) != 1 || memberMsgs[0].Type != "join_approved" || memberMsgs[0].AgentID != approved.AgentID ||
		memberMsgs[0].TeamName != "demo" || memberMsgs[0].AssignedName != "alice-2" {
		t.Fatalf("member inbox = %+v", memberMsgs)
	}
	if _, err := s.ApproveJoin(ctx, "demo", r.RequestID, JoinDecisionInput{Caller: "leader"}); KindOf(err) != KindConflict {
		t.Fatalf("second approval err = %v", err)
	}
	if _, err := s.RequestJoin(ctx, "demo", JoinRequestInput{ProposedName: "alice-2"}); KindOf(err) != KindConflict {
		t.Fatalf("request by an existing member err = %v", err)
	}
}

func TestJoinDecisionsAreLeaderOnlyAndNeedAPendingRequest(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "bob")
	ctx := context.Background()
	r, _ := s.RequestJoin(ctx, "demo", JoinRequestInput{ProposedName: "carol"})

	for _, caller := range []string{"bob", "carol", ""} {
		if _, err := s.ApproveJoin(ctx, "demo", r.RequestID, JoinDecisionInput{Caller: caller}); KindOf(err) != KindInvalid {
			t.Fatalf("approve by %q err = %v", caller, err)
		}
		if _, err := s.RejectJoin(ctx, "demo", r.RequestID, JoinDecisionInput{Caller: caller}); KindOf(err) != KindInvalid {
			t.Fatalf("reject by %q err = %v", caller, err)
		}
	}
	if _, err := s.RejectJoin(ctx, "demo", "join-000000000000", JoinDecisionInput{Caller: "leader"}); KindOf(err) != KindNotFound {
		t.Fatalf("unknown id err = %v", err)
	}
	rejected, err := s.RejectJoin(ctx, "demo", r.RequestID, JoinDecisionInput{Caller: "leader", Reason: "team is full"})
	if err != nil || rejected.Status != model.JoinRejected || rejected.Reason != "team is full" {
		t.Fatalf("rejected = %+v, %v", rejected, err)
	}
	if _, err := s.ApproveJoin(ctx, "demo", r.RequestID, JoinDecisionInput{Caller: "leader"}); KindOf(err) != KindConflict {
		t.Fatalf("approve after reject err = %v", err)
	}
	team, _ := s.GetTeam(ctx, "demo")
	if len(team.Members) != 2 {
		t.Fatalf("a rejected request added a member: %+v", team.Members)
	}
	pending, _ := s.ListJoinRequests(ctx, "demo", model.JoinPending)
	all, _ := s.ListJoinRequests(ctx, "demo", "")
	if len(pending) != 0 || len(all) != 1 {
		t.Fatalf("pending = %d, all = %d", len(pending), len(all))
	}
	if _, err := s.ListJoinRequests(ctx, "demo", "weird"); KindOf(err) != KindInvalid {
		t.Fatalf("bad status err = %v", err)
	}
}

func TestJoinRequestNeedsAnExistingTeam(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.RequestJoin(context.Background(), "nope", JoinRequestInput{ProposedName: "x"}); KindOf(err) != KindNotFound ||
		err.Error() != "Team 'nope' not found" {
		t.Fatalf("err = %v", err)
	}
}
