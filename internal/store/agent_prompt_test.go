package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentPromptPersistsAndNotifiesOnlyItsMember(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	newTeam(t, s, "alice", "bob")
	ctx := context.Background()
	if _, err := s.AddMember(ctx, "demo", AddMemberInput{Name: "alice", User: "other"}); err != nil {
		t.Fatal(err)
	}
	changed, err := s.SetAgentPrompt(ctx, "demo", "alice", "", "Review security")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	changed, err = s.SetAgentPrompt(ctx, "demo", "alice", "", "Review security")
	if err != nil || changed {
		t.Fatalf("unchanged=%v err=%v", changed, err)
	}
	msgs, err := s.Peek(ctx, "demo", "alice", "")
	if err != nil || len(msgs) != 1 || !strings.Contains(msgs[0].Content, "agent prompt show demo alice") {
		t.Fatalf("notifications=%v err=%v", msgs, err)
	}
	for _, member := range []struct{ name, user string }{{"bob", ""}, {"alice", "other"}} {
		prompt, err := s.AgentPrompt(ctx, "demo", member.name, member.user)
		if err != nil || prompt != "" {
			t.Fatalf("other prompt=%q err=%v", prompt, err)
		}
		msgs, err := s.Peek(ctx, "demo", member.name, member.user)
		if err != nil || len(msgs) != 0 {
			t.Fatalf("other inbox=%v err=%v", msgs, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := s.AgentPrompt(ctx, "demo", "alice", "")
	if err != nil || prompt != "Review security" {
		t.Fatalf("reopened prompt=%q err=%v", prompt, err)
	}
	changed, err = s.SetAgentPrompt(ctx, "demo", "alice", "", "")
	if err != nil || !changed {
		t.Fatalf("clear=%v err=%v", changed, err)
	}
	prompt, err = s.AgentPrompt(ctx, "demo", "alice", "")
	if err != nil || prompt != "" {
		t.Fatalf("cleared prompt=%q err=%v", prompt, err)
	}
}

func TestAgentPromptRejectsUnknownAndInvalidMembers(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	for _, tc := range []struct {
		team, name, user string
		kind             Kind
	}{
		{"missing", "alice", "", KindNotFound},
		{"demo", "missing", "", KindNotFound},
		{"demo", "alice", "other", KindNotFound},
		{"../demo", "alice", "", KindInvalid},
	} {
		if _, err := s.AgentPrompt(ctx, tc.team, tc.name, tc.user); KindOf(err) != tc.kind {
			t.Fatalf("get %+v: %v", tc, err)
		}
		if _, err := s.SetAgentPrompt(ctx, tc.team, tc.name, tc.user, "rules"); KindOf(err) != tc.kind {
			t.Fatalf("set %+v: %v", tc, err)
		}
	}
	if _, err := s.SetAgentPrompt(ctx, "demo", "alice", "", strings.Repeat("x", 65537)); KindOf(err) != KindInvalid {
		t.Fatalf("oversize: %v", err)
	}
}

func TestAgentPromptNotificationFailureRollsBackUpdate(t *testing.T) {
	s := newTestStore(t)
	newTeam(t, s, "alice")
	ctx := context.Background()
	if _, err := s.db.Exec("CREATE TRIGGER reject_notice BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'notice failed'); END"); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.SetAgentPrompt(ctx, "demo", "alice", "", "new rules"); err == nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	prompt, err := s.AgentPrompt(ctx, "demo", "alice", "")
	if err != nil || prompt != "" {
		t.Fatalf("failed notification saved prompt=%q err=%v", prompt, err)
	}
}
