package server

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

// pastePane records what the Deliverer pastes; goroutines write to it.
type pastePane struct {
	mu    sync.Mutex
	texts []string
	dead  bool
}

func (p *pastePane) pasted() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.texts...)
}

func newDeliverFixture(t *testing.T) (*Deliverer, *store.Store, *pastePane, *time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
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
	for _, name := range []string{"alice", "bob"} {
		if _, err := st.AddMember(ctx, "demo", store.AddMemberInput{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", Backend: "tmux", Target: "%1", Nudge: true,
		Command: []string{"dsh", "tui"}}); err != nil {
		t.Fatal(err)
	}
	pane := &pastePane{}
	now := time.Now().Add(time.Minute)
	d := NewDeliverer(st, hub, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.Paste = func(_, text string) error {
		pane.mu.Lock()
		defer pane.mu.Unlock()
		pane.texts = append(pane.texts, text)
		return nil
	}
	d.Alive = func(string) bool {
		pane.mu.Lock()
		defer pane.mu.Unlock()
		return !pane.dead
	}
	d.Now = func() time.Time { return now }
	return d, st, pane, &now
}

func alice(t *testing.T, st *store.Store) model.Agent {
	t.Helper()
	a, err := st.GetAgent(context.Background(), "demo", "alice")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func send(t *testing.T, st *store.Store, from, content string) {
	t.Helper()
	if _, err := st.Send(context.Background(), "demo", store.SendInput{From: from, To: "alice", Content: content}); err != nil {
		t.Fatal(err)
	}
}

func unread(t *testing.T, st *store.Store) int {
	t.Helper()
	msgs, err := st.Peek(context.Background(), "demo", "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

func TestDeliverPastesTheMessagesAndTakesThem(t *testing.T) {
	d, st, pane, _ := newDeliverFixture(t)
	send(t, st, "leader", "first task")
	send(t, st, "bob", "a question")
	last := map[string]time.Time{}
	if retry := d.deliver(context.Background(), alice(t, st), last); retry != 0 {
		t.Fatalf("nothing is held back, but retry in %s", retry)
	}
	got := pane.pasted()
	if len(got) != 1 || !strings.Contains(got[0], "first task") || !strings.Contains(got[0], "a question") {
		t.Fatalf("want one paste with both messages, got %q", got)
	}
	if n := unread(t, st); n != 0 {
		t.Fatalf("pasted messages must leave the inbox, %d left", n)
	}
}

func TestDeliverThrottlesEachSender(t *testing.T) {
	d, st, pane, now := newDeliverFixture(t)
	ctx := context.Background()
	last := map[string]time.Time{}
	send(t, st, "leader", "one")
	d.deliver(ctx, alice(t, st), last)

	*now = now.Add(10 * time.Second)
	send(t, st, "leader", "two")
	if retry := d.deliver(ctx, alice(t, st), last); retry != 20*time.Second {
		t.Fatalf("leader's next paste is due in 20s, got retry %s", retry)
	}
	if len(pane.pasted()) != 1 || unread(t, st) != 1 {
		t.Fatalf("a throttled message waits in the inbox: pasted %q", pane.pasted())
	}

	// Another sender's message is due at once and takes the waiting one along.
	send(t, st, "bob", "three")
	d.deliver(ctx, alice(t, st), last)
	got := pane.pasted()
	if len(got) != 2 || !strings.Contains(got[1], "two") || !strings.Contains(got[1], "three") || unread(t, st) != 0 {
		t.Fatalf("want the held message pasted with bob's, got %q", got)
	}
}

func TestDeliverWaitsForStartupAndLivePane(t *testing.T) {
	d, st, pane, now := newDeliverFixture(t)
	ctx := context.Background()
	send(t, st, "leader", "hi")
	started, err := time.Parse(time.RFC3339Nano, alice(t, st).StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	*now = started.Add(5 * time.Second)
	if retry := d.deliver(ctx, alice(t, st), map[string]time.Time{}); retry != 10*time.Second || len(pane.pasted()) != 0 {
		t.Fatalf("an agent 5s old waits 10s more: retry %s, pasted %q", retry, pane.pasted())
	}

	*now = started.Add(time.Minute)
	pane.dead = true
	if retry := d.deliver(ctx, alice(t, st), map[string]time.Time{}); retry != time.Second || unread(t, st) != 1 {
		t.Fatalf("messages for a dead pane stay in the inbox: retry %s, unread %d", retry, unread(t, st))
	}
}

func TestDeliverWatchesEachLocalAgentAndPastesOnArrival(t *testing.T) {
	d, st, pane, _ := newDeliverFixture(t)
	ctx := context.Background()
	// Neither gets a goroutine: bob forbids typing, carol runs elsewhere.
	if _, err := st.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "bob", Backend: "tmux", Target: "%2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMember(ctx, "demo", store.AddMemberInput{Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "carol", Backend: "tmux", Target: "%3", Nudge: true,
		Host: "other-machine.example"}); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		d.Run(runCtx)
		close(done)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for len(d.workersSnapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := d.workersSnapshot(); len(got) != 1 || !strings.HasPrefix(got[0], "demo/alice/") {
		t.Fatalf("want one goroutine, for alice: %v", got)
	}

	send(t, st, "leader", "new task")
	for len(pane.pasted()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := pane.pasted(); len(got) != 1 || !strings.Contains(got[0], "new task") {
		t.Fatalf("the message should be pasted as it arrives: %q", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not wait for its goroutines and return")
	}
}

// workersSnapshot lists the agent runs that have a goroutine.
func (d *Deliverer) workersSnapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var keys []string
	for key := range d.workers {
		keys = append(keys, key)
	}
	return keys
}

func TestDeliverLeavesWorkingAgentsToTheirHooks(t *testing.T) {
	d, st, pane, _ := newDeliverFixture(t)
	ctx := context.Background()
	send(t, st, "leader", "hi")
	for _, act := range []model.AgentActivity{model.ActivityWorking, model.ActivityBlocked, model.ActivityWaiting} {
		if _, err := st.SetAgentActivity(ctx, "demo", "alice", "", act); err != nil {
			t.Fatal(err)
		}
		d.deliver(ctx, alice(t, st), map[string]time.Time{})
		want := 1
		if act == model.ActivityWorking {
			want = 0
		}
		if got := pane.pasted(); len(got) != want {
			t.Fatalf("%s: pasted %q", act, got)
		}
		if act == model.ActivityBlocked {
			send(t, st, "leader", "again")
			pane.texts = nil
		}
	}
}
