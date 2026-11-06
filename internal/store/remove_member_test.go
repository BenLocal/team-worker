package store

import (
	"context"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestRemoveMemberDropsItsAgentAndSession(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice", "bob")
	ctx := context.Background()
	if _, err := s.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", RunID: "run-1", Backend: "tmux"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSession(ctx, model.Session{Team: "demo", Agent: "alice", RunID: "run-1", Client: "claude", SessionID: "s-1"}); err != nil {
		t.Fatal(err)
	}
	s.Probe = func(model.Agent) (bool, bool) { return false, true }
	if _, err := s.AgentExited(ctx, "demo", "alice"); err != nil {
		t.Fatal(err)
	}
	done, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "old work", Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	completed := model.StatusCompleted
	if _, err := s.UpdateTask(ctx, "demo", done.ID, UpdateTaskInput{Status: &completed}); err != nil {
		t.Fatal(err)
	}

	removed, err := s.RemoveMember(ctx, "demo", "alice")
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	team, err := s.GetTeam(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range team.Members {
		if m.Name == "alice" {
			t.Fatal("alice is still a member")
		}
	}
	if _, err := s.GetAgent(ctx, "demo", "alice"); KindOf(err) != KindNotFound {
		t.Fatalf("the agent record must go with the member: %v", err)
	}
	if _, err := s.GetSession(ctx, "demo", "alice", ""); KindOf(err) != KindNotFound {
		t.Fatalf("the session must go with the member: %v", err)
	}
	if again, err := s.RemoveMember(ctx, "demo", "alice"); again || err != nil {
		t.Fatalf("a second removal finds nothing: %v %v", again, err)
	}
}

func TestRemoveMemberRefusesTheLeader(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s)
	_, err := s.RemoveMember(context.Background(), "demo", "leader")
	if KindOf(err) != KindConflict || !strings.Contains(err.Error(), "leader") {
		t.Fatalf("got %v", err)
	}
}

func TestRemoveMemberRefusesARunningAgent(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	if _, err := s.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", RunID: "run-1", Backend: "tmux"}); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []struct{ alive, known bool }{{true, true}, {false, false}} {
		s.Probe = func(model.Agent) (bool, bool) { return probe.alive, probe.known }
		_, err := s.RemoveMember(ctx, "demo", "alice")
		if KindOf(err) != KindConflict || !strings.Contains(err.Error(), "agent stop") {
			t.Fatalf("probe %+v: got %v", probe, err)
		}
	}
	// A process that died without reporting its exit does not block the removal.
	s.Probe = func(model.Agent) (bool, bool) { return false, true }
	if removed, err := s.RemoveMember(ctx, "demo", "alice"); !removed || err != nil {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
}

func TestRemoveMemberRefusesUnfinishedTasks(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	task, err := s.CreateTask(ctx, "demo", CreateTaskInput{Subject: "work", Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.RemoveMember(ctx, "demo", "alice")
	if KindOf(err) != KindConflict || !strings.Contains(err.Error(), task.ID) {
		t.Fatalf("got %v", err)
	}
}
