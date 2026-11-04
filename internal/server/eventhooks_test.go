package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

func newHookFixture(t *testing.T) (*HookRunner, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hub := NewHub()
	st.OnChange = hub.Notify
	if _, err := st.CreateTeam(context.Background(), store.CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}
	return NewHookRunner(st, hub, slog.New(slog.DiscardHandler)), st
}

func pendingCount(t *testing.T, st *store.Store) int {
	t.Helper()
	events, err := st.PendingEvents(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return len(events)
}

func TestDrainRunsShellHooksInOrderWithTheEvent(t *testing.T) {
	h, st := newHookFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	// The later hook writes stdin and the env; the earlier one only marks its turn.
	st.AddEventHook(ctx, store.EventHookInput{Event: "TaskCreated", Priority: 1,
		Command: `cat > "` + dir + `/stdin"; echo "second $TEAM_WORKER_EVENT_TYPE $TEAM_WORKER_EVENT_TEAM_NAME $TEAM_WORKER_EVENT_SUBJECT" >> "` + log + `"`})
	st.AddEventHook(ctx, store.EventHookInput{Event: "TaskCreated", Command: `echo first >> "` + log + `"; exit 3`})
	task, _ := st.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "build it"})

	if err := h.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(log)
	if string(got) != "first\nsecond TaskCreated demo build it\n" {
		t.Fatalf("hooks ran as %q", got)
	}
	var payload map[string]any
	data, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if err := json.Unmarshal(data, &payload); err != nil || payload["task_id"] != task.ID || payload["type"] != "TaskCreated" {
		t.Fatalf("stdin %v %s", err, data)
	}
	// A failing hook does not keep the event: it ran.
	if n := pendingCount(t, st); n != 0 {
		t.Fatalf("%d events left", n)
	}
}

func TestDrainKeepsTheEventWhenStopped(t *testing.T) {
	h, st := newHookFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	st.AddEventHook(ctx, store.EventHookInput{Event: "TaskCreated", Command: "true"})
	st.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "x"})
	h.Exec = func(context.Context, model.EventHook, map[string]any) model.HookResult {
		cancel()
		return model.HookResult{ExitCode: -1, Error: "killed"}
	}
	if err := h.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if n := pendingCount(t, st); n != 1 {
		t.Fatalf("an interrupted event must be run again on the next start, %d left", n)
	}
}

func TestRunWakesOnWrites(t *testing.T) {
	h, st := newHookFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan string, 1)
	h.Exec = func(_ context.Context, _ model.EventHook, p map[string]any) model.HookResult {
		ran <- p["subject"].(string)
		return model.HookResult{}
	}
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	st.AddEventHook(ctx, store.EventHookInput{Event: "TaskCreated", Command: "true"})
	st.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "wake"})
	select {
	case got := <-ran:
		if got != "wake" {
			t.Fatalf("ran for %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the runner did not wake on the write")
	}
	cancel()
	<-done
}

func TestRunShellHookReportsExitTimeoutAndEnv(t *testing.T) {
	ctx := context.Background()
	payload := map[string]any{"type": "WorkerExit", "abandoned_tasks": []any{"a", "b"}, "exit_code": float64(2)}
	res := RunShellHook(ctx, model.EventHook{ID: "h1", Command: `echo "$TEAM_WORKER_EVENT_ABANDONED_TASKS $TEAM_WORKER_EVENT_EXIT_CODE"; exit 4`}, payload)
	if res.ExitCode != 4 || res.Error != "" || strings.TrimSpace(res.Output) != "a,b 2" || res.HookID != "h1" {
		t.Fatalf("%+v", res)
	}

	old := HookTimeout
	HookTimeout = 200 * time.Millisecond
	defer func() { HookTimeout = old }()
	start := time.Now()
	res = RunShellHook(ctx, model.EventHook{Command: "sleep 10"}, payload)
	if res.ExitCode != -1 || !strings.Contains(res.Error, "timed out") || time.Since(start) > 5*time.Second {
		t.Fatalf("%+v after %s", res, time.Since(start))
	}
}
