package server

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
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

// setActivity records what the agent's hooks would report.
func setActivity(t *testing.T, st *store.Store, name string, act model.AgentActivity) {
	t.Helper()
	if _, err := st.SetAgentActivity(context.Background(), "demo", name, "", act); err != nil {
		t.Fatal(err)
	}
}

// startTask gives alice an in-progress task.
func startTask(t *testing.T, st *store.Store, owner string) model.Task {
	t.Helper()
	ctx := context.Background()
	task, err := st.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "run the checks", Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	status := model.StatusInProgress
	if task, err = st.UpdateTask(ctx, "demo", task.ID, store.UpdateTaskInput{Status: &status, Caller: owner}); err != nil {
		t.Fatal(err)
	}
	return task
}

// leaderMessages returns what the daemon told the leader.
func leaderMessages(t *testing.T, st *store.Store) []model.Message {
	t.Helper()
	log, err := st.MessageLog(context.Background(), "demo", 50, "")
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Message
	for _, m := range log {
		if m.From == "team-worker" && m.To == "leader" {
			out = append(out, m)
		}
	}
	return out
}

func TestStalledWorkerIsRemindedThenReported(t *testing.T) {
	n, st, pane, now := newNudgeFixture(t, true)
	task := startTask(t, st, "alice")
	// A model error ended the turn without the Stop hook; idle_prompt then
	// reported waiting.
	setActivity(t, st, "alice", model.ActivityWaiting)

	advance(n, now, 9*time.Minute)
	if len(pane.typed) != 0 {
		t.Fatalf("reminded before StallAfter: %q", pane.typed)
	}
	advance(n, now, 2*time.Minute)
	if len(pane.typed) != 1 || !strings.Contains(pane.typed[0], task.ID) {
		t.Fatalf("want one reminder naming %s, got %q", task.ID, pane.typed)
	}
	advance(n, now, 25*time.Minute)
	if len(pane.typed) != 1 {
		t.Fatalf("reminded again before StallRepeat: %q", pane.typed)
	}
	advance(n, now, 6*time.Minute)
	if len(pane.typed) != 2 {
		t.Fatalf("want a second reminder after StallRepeat: %q", pane.typed)
	}
	if got := leaderMessages(t, st); len(got) != 0 {
		t.Fatalf("leader told before the reminders ran out: %+v", got)
	}
	advance(n, now, 31*time.Minute)
	got := leaderMessages(t, st)
	if len(got) != 1 || !strings.Contains(got[0].Content, "alice") || !strings.Contains(got[0].Content, task.ID) {
		t.Fatalf("want one report to the leader about alice and %s, got %+v", task.ID, got)
	}
	advance(n, now, 3*time.Hour)
	if len(pane.typed) != 2 || len(leaderMessages(t, st)) != 1 {
		t.Fatalf("a stall is reported once: typed %q, leader %+v", pane.typed, leaderMessages(t, st))
	}
}

func TestNoStallReminderWhenWorkingOrWithoutTask(t *testing.T) {
	n, st, pane, now := newNudgeFixture(t, true)
	setActivity(t, st, "alice", model.ActivityWaiting)
	advance(n, now, 15*time.Minute)
	if len(pane.typed) != 0 {
		t.Fatalf("an idle worker without in-progress tasks is not stalled: %q", pane.typed)
	}

	startTask(t, st, "alice")
	setActivity(t, st, "alice", model.ActivityWorking)
	advance(n, now, 15*time.Minute)
	if len(pane.typed) != 0 {
		t.Fatalf("a working agent is not stalled: %q", pane.typed)
	}
	// Waiting starts the clock over.
	setActivity(t, st, "alice", model.ActivityWaiting)
	advance(n, now, 9*time.Minute)
	if len(pane.typed) != 0 {
		t.Fatalf("time spent working counted as idle: %q", pane.typed)
	}
}

func TestNoStallReminderForLeader(t *testing.T) {
	n, st, pane, now := newNudgeFixture(t, true)
	ctx := context.Background()
	if _, err := st.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "leader", Backend: "tmux", Target: "%2", Nudge: true,
		Command: []string{"claude"}}); err != nil {
		t.Fatal(err)
	}
	startTask(t, st, "leader")
	setActivity(t, st, "leader", model.ActivityWaiting)
	advance(n, now, 2*time.Hour)
	if len(pane.typed) != 0 {
		t.Fatalf("the leader waits for its workers; it is not stalled: %q", pane.typed)
	}
}

func TestStallReminderByScreen(t *testing.T) {
	n, st, pane, now := newNudgeFixture(t, true)
	// Claude without hook reports is judged by its unchanged idle screen.
	startTask(t, st, "alice")
	advance(n, now, 11*time.Minute)
	if len(pane.typed) != 1 {
		t.Fatalf("want one reminder for an idle screen, got %q", pane.typed)
	}
}
