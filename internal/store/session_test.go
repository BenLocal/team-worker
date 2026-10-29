package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestSessionsSurviveExitAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	newTeam(t, s, "alice", "bob")
	for _, name := range []string{"alice", "bob"} {
		_, err = s.RegisterAgent(ctx, model.Agent{Team: "demo", Name: name, RunID: name + "-run"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.SaveSession(ctx, model.Session{Team: "demo", Agent: name, RunID: name + "-run", Client: "claude", SessionID: name + "-session", Cwd: "/shared", Command: []string{"claude"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.AgentExitedRun(ctx, "demo", "alice", "alice-run", 1); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{"alice", "bob"} {
		got, err := s.GetSession(ctx, "demo", name, "")
		if err != nil || got.SessionID != name+"-session" {
			t.Fatalf("%s: %+v, %v", name, got, err)
		}
	}
}

func TestOldRunCannotExitOrOverwriteNewSession(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	for _, run := range []string{"old", "new"} {
		if run == "new" {
			if _, err := s.AgentExitedRun(ctx, "demo", "alice", "old", 0); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", RunID: run}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SaveSession(ctx, model.Session{Team: "demo", Agent: "alice", RunID: run, Client: "claude", SessionID: run}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.AgentExitedRun(ctx, "demo", "alice", "old", 1)
	if err != nil || res.Changed {
		t.Fatalf("late exit: %+v %v", res, err)
	}
	if _, err := s.SaveSession(ctx, model.Session{Team: "demo", Agent: "alice", RunID: "old", Client: "claude", SessionID: "wrong"}); KindOf(err) != KindConflict {
		t.Fatalf("late capture: %v", err)
	}
	got, err := s.GetAgent(ctx, "demo", "alice")
	if err != nil || got.Status != model.AgentRunning || got.RunID != "new" {
		t.Fatalf("new run: %+v %v", got, err)
	}
	if _, err := s.RequestAgentStop(ctx, "demo", "alice", "new"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAgent(ctx, "demo", "alice")
	if !got.StopRequested {
		t.Fatal("stop intent was not persisted")
	}
	if _, err := s.AgentExitedRun(ctx, "demo", "alice", "new", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", RunID: "retry", PreviousRunID: "new"}); KindOf(err) != KindConflict {
		t.Fatalf("queued retry ignored stop: %v", err)
	}
}
