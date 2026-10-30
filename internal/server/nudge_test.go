package server

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/agent/builtin"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

type fakePane struct {
	screen string
	typed  []string
}

// claudeIdle is Claude Code waiting at its input box.
const claudeIdle = "● done\n\n✻ Cooked for 7s\n" +
	"────────────────────────────────────────\n❯\n────────────────────────────────────────\n" +
	"  ⏵⏵ auto mode on (shift+tab to cycle)\n"

func newNudgeFixture(t *testing.T, nudge bool) (*Nudger, *store.Store, *fakePane, *time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	st.CreateTeam(ctx, store.CreateTeamInput{Name: "demo", LeaderName: "leader"})
	st.AddMember(ctx, "demo", store.AddMemberInput{Name: "alice"})
	st.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", Backend: "tmux", Target: "%1", Nudge: nudge,
		Command: []string{"claude"}})

	pane := &fakePane{screen: claudeIdle}
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	n := NewNudger(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	n.Screen = func(string) (string, error) { return pane.screen, nil }
	n.Title = func(string) (string, error) { return "", nil }
	n.Adapter = builtin.Registry().For
	n.Type = func(_, text string) error { pane.typed = append(pane.typed, text); return nil }
	n.Now = func() time.Time { return now }
	return n, st, pane, &now
}

// advance runs ticks over d, two seconds apart, like the daemon loop.
func advance(n *Nudger, now *time.Time, d time.Duration) {
	for end := now.Add(d); now.Before(end); {
		*now = now.Add(2 * time.Second)
		n.Tick(context.Background())
	}
}

func TestNudgeIdleAgentOncePerBatch(t *testing.T) {
	n, st, pane, now := newNudgeFixture(t, true)
	ctx := context.Background()
	advance(n, now, 20*time.Second)
	if len(pane.typed) != 0 {
		t.Fatalf("nothing to deliver, but typed %q", pane.typed)
	}

	st.Send(ctx, "demo", store.SendInput{From: "leader", To: "alice", Content: "hi"})
	advance(n, now, 12*time.Second)
	if len(pane.typed) != 1 {
		t.Fatalf("want one nudge, got %q", pane.typed)
	}
	advance(n, now, 60*time.Second)
	if len(pane.typed) != 1 {
		t.Fatalf("same unread message must not be nudged again so soon: %q", pane.typed)
	}

	st.Send(ctx, "demo", store.SendInput{From: "leader", To: "alice", Content: "more"})
	advance(n, now, 12*time.Second)
	if len(pane.typed) != 2 {
		t.Fatalf("a new message should nudge again: %q", pane.typed)
	}
	advance(n, now, 6*time.Minute)
	if len(pane.typed) != 3 {
		t.Fatalf("still-unread messages get a reminder after Repeat: %q", pane.typed)
	}
}

func TestNoNudgeWhileBusyOrWithoutPrompt(t *testing.T) {
	n, st, pane, now := newNudgeFixture(t, true)
	ctx := context.Background()
	st.Send(ctx, "demo", store.SendInput{From: "leader", To: "alice", Content: "hi"})

	for i := 0; i < 10; i++ {
		pane.screen = "✢ Working… (" + string(rune('a'+i)) + "s)\n────\n❯\n────\n  ⏵⏵ auto mode on · esc to interrupt\n"
		*now = now.Add(2 * time.Second)
		n.Tick(ctx)
	}
	if len(pane.typed) != 0 {
		t.Fatalf("a changing screen means the agent is busy: %q", pane.typed)
	}

	pane.screen = " Do you want to proceed?\n ❯ 1. Yes\n   2. No\n Esc to cancel · Tab to amend\n"
	advance(n, now, 30*time.Second)
	if len(pane.typed) != 0 {
		t.Fatalf("never type into a menu: %q", pane.typed)
	}
}

func TestNoScreenNudgeWithoutRules(t *testing.T) {
	n, st, pane, now := newNudgeFixture(t, true)
	ctx := context.Background()
	// The generic adapter has no screen rules, so its screen proves nothing.
	st.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", Backend: "tmux", Target: "%1", Nudge: true,
		Command: []string{"my-agent"}})
	st.Send(ctx, "demo", store.SendInput{From: "leader", To: "alice", Content: "hi"})
	advance(n, now, 30*time.Second)
	if len(pane.typed) != 0 {
		t.Fatalf("typed into an agent without screen rules: %q", pane.typed)
	}
}

func TestNudgeRespectsAgentSetting(t *testing.T) {
	n, st, pane, now := newNudgeFixture(t, false)
	st.Send(context.Background(), "demo", store.SendInput{From: "leader", To: "alice", Content: "hi"})
	advance(n, now, 30*time.Second)
	if len(pane.typed) != 0 {
		t.Fatalf("nudge disabled for this agent: %q", pane.typed)
	}
}

func TestNoNudgeForAgentsOnAnotherMachine(t *testing.T) {
	n, st, pane, now := newNudgeFixture(t, true)
	ctx := context.Background()
	// A remote CLI registered this agent; %1 would name an unrelated pane here.
	st.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", Backend: "tmux", Target: "%1", Nudge: true, Host: "other-machine.example"})
	st.Send(ctx, "demo", store.SendInput{From: "leader", To: "alice", Content: "hi"})
	advance(n, now, 30*time.Second)
	if len(pane.typed) != 0 {
		t.Fatalf("typed into a pane of another machine: %q", pane.typed)
	}
}

func TestNudgeFollowsHookActivity(t *testing.T) {
	n, st, pane, now := newNudgeFixture(t, true)
	ctx := context.Background()
	set := func(act model.AgentActivity) {
		t.Helper()
		if _, err := st.SetAgentActivity(ctx, "demo", "alice", "", act); err != nil {
			t.Fatal(err)
		}
	}
	// The screen looks idle throughout; only the reported activity decides.
	set(model.ActivityWorking)
	st.Send(ctx, "demo", store.SendInput{From: "leader", To: "alice", Content: "hi"})
	advance(n, now, 30*time.Second)
	set(model.ActivityBlocked)
	advance(n, now, 30*time.Second)
	if len(pane.typed) != 0 {
		t.Fatalf("a working or blocked agent must not be typed into: %q", pane.typed)
	}

	// A reply ending in a numbered list fooled the screen check; hooks do not.
	pane.screen = "Next steps\n1. deploy\n2. verify\n────\n❯ \n────\n"
	set(model.ActivityWaiting)
	advance(n, now, 4*time.Second)
	if len(pane.typed) != 0 {
		t.Fatalf("nudged before IdleFor: %q", pane.typed)
	}
	advance(n, now, 8*time.Second)
	if len(pane.typed) != 1 {
		t.Fatalf("a waiting agent with unread messages should be nudged once: %q", pane.typed)
	}
}

func TestNoNudgeWhenMessagesAreTakenQuickly(t *testing.T) {
	n, st, pane, now := newNudgeFixture(t, true)
	ctx := context.Background()
	st.SetAgentActivity(ctx, "demo", "alice", "", model.ActivityWaiting)
	st.Send(ctx, "demo", store.SendInput{From: "leader", To: "alice", Content: "hi"})
	advance(n, now, 4*time.Second)
	// An extension such as pi's wakes the agent itself and takes the message.
	st.Receive(ctx, "demo", "alice", "", 10)
	advance(n, now, 30*time.Second)
	if len(pane.typed) != 0 {
		t.Fatalf("messages already taken must not cause a nudge: %q", pane.typed)
	}
}
