package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
)

func supervisedFixture(t *testing.T, h *harness, max int) (string, string) {
	t.Helper()
	h.run("team", "spawn-team", "demo")
	h.run("team", "add-member", "demo", "alice")
	dir := t.TempDir()
	log := filepath.Join(dir, "attempts")
	command := filepath.Join(dir, "claude")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf 'ATTEMPT:%s:%s\\n' \"$1\" \"$2\" >> \"$ATTEMPT_LOG\"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATTEMPT_LOG", log)
	ctx := context.Background()
	if _, err := h.api.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", AgentID: "alice-id", RunID: "initial", Backend: "subprocess", PID: os.Getpid(), Command: []string{command}, Cwd: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.SaveSession(ctx, model.Session{Team: "demo", Agent: "alice", RunID: "initial", Client: "claude", SessionID: "exact-id", Command: []string{command}, Cwd: dir, Backend: "subprocess", KeepAlive: true, MaxRestarts: max, RestartDelayMillis: 1}); err != nil {
		t.Fatal(err)
	}
	return command, log
}

func TestKeepaliveHasBoundedRetriesAndExactResume(t *testing.T) {
	h := newHarness(t)
	command, log := supervisedFixture(t, h, 2)
	out, ok := h.run("--json", "lifecycle", "supervise", "--team", "demo", "--agent", "alice", "--run-id", "initial", "--", command)
	if ok || !strings.Contains(out, "restart limit") {
		t.Fatalf("%v %s", ok, out)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 3 || lines[1] != "ATTEMPT:--resume:exact-id" || lines[2] != "ATTEMPT:--resume:exact-id" {
		t.Fatalf("attempts: %q", data)
	}
	session, err := h.api.GetSession(context.Background(), "demo", "alice", "")
	if err != nil || session.SessionID != "exact-id" {
		t.Fatalf("session lost: %+v %v", session, err)
	}
}

func TestStopIntentPreventsSupervisorLaunch(t *testing.T) {
	h := newHarness(t)
	command, log := supervisedFixture(t, h, 2)
	if _, err := h.api.RequestAgentStop(context.Background(), "demo", "alice", "initial"); err != nil {
		t.Fatal(err)
	}
	if out, ok := h.run("lifecycle", "supervise", "--team", "demo", "--agent", "alice", "--run-id", "initial", "--", command); !ok {
		t.Fatalf("%s", out)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("a stopped agent was launched: %v", err)
	}
}

func TestManualResumeDryRunUsesSavedSession(t *testing.T) {
	h := newHarness(t)
	_, _ = supervisedFixture(t, h, 0)
	out, ok := h.run("--json", "agent", "resume", "demo", "alice", "--dry-run")
	if !ok || !strings.Contains(out, "--resume") || !strings.Contains(out, "exact-id") {
		t.Fatalf("%v %s", ok, out)
	}
}

func TestStopDuringBackoffPreventsAnotherAttempt(t *testing.T) {
	h := newHarness(t)
	command, log := supervisedFixture(t, h, 3)
	ctx := context.Background()
	session, err := h.api.GetSession(ctx, "demo", "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	session.RestartDelayMillis = 300
	if _, err := h.api.SaveSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		_, ok := h.run("lifecycle", "supervise", "--team", "demo", "--agent", "alice", "--run-id", "initial", "--", command)
		done <- ok
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ag, err := h.api.GetAgent(ctx, "demo", "alice")
		if err != nil {
			t.Fatal(err)
		}
		if ag.Status == model.AgentExited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not exit")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := h.api.RequestAgentStop(ctx, "demo", "alice", "initial"); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("stopped supervisor failed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor did not stop")
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "ATTEMPT::\n" {
		t.Fatalf("restarted after stop: %q %v", data, err)
	}
}

func TestStartupHookCapturesOnlyItsOwnRun(t *testing.T) {
	h := newHarness(t)
	supervisedFixture(t, h, 0)
	t.Setenv("TEAM_WORKER_RUN_ID", "initial")
	if _, ok := h.runIn(`{"source":"startup","session_id":"native-hook-id"}`, "hook", "session-start", "--team", "demo", "--agent", "alice"); !ok {
		t.Fatal("startup hook failed")
	}
	got, err := h.api.GetSession(context.Background(), "demo", "alice", "")
	if err != nil || got.SessionID != "native-hook-id" {
		t.Fatalf("%+v %v", got, err)
	}
	t.Setenv("TEAM_WORKER_RUN_ID", "stale")
	h.runIn(`{"source":"startup","session_id":"wrong"}`, "hook", "session-start", "--team", "demo", "--agent", "alice")
	got, err = h.api.GetSession(context.Background(), "demo", "alice", "")
	if err != nil || got.SessionID != "native-hook-id" {
		t.Fatalf("late hook overwrote session: %+v %v", got, err)
	}
}
