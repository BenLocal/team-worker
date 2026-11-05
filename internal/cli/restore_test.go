package cli

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/proc"
	"github.com/benlocal/team-worker/internal/server"
	"github.com/benlocal/team-worker/internal/store"
)

// deadPID returns the pid of a process that has already exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestRestoreResumesOnlyAgentsARestartLeftBehind(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cli.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Probe = proc.Probe
	srv := server.New(st, nil)
	srv.SetRunner("", workerBinary())
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		st.Close()
	})
	t.Setenv("TEAM_WORKER_AGENT_NAME", "leader")
	t.Setenv("TEAM_WORKER_USER", "")
	t.Setenv("TEAM_WORKER_DATA_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TEAM_WORKER_WORKSPACE", "never")
	h := &harness{t: t, api: client.NewHTTP(ts.URL, ts.Client())}
	h.run("team", "spawn-team", "demo")
	dir := t.TempDir()
	log := filepath.Join(dir, "attempts")
	command := filepath.Join(dir, "claude")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf '%s %s\\n' \"$TEAM_WORKER_AGENT_NAME\" \"$*\" >> \"$ATTEMPT_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATTEMPT_LOG", log)
	ctx := context.Background()
	// alice was running when the daemon went down; erin's supervisor was
	// killed by the shutdown; bob was asked to stop; frank's supervisor gave
	// up with the child's exit code; carol has no saved session; dave is
	// still alive.
	for _, a := range []struct {
		name    string
		pid     int
		session bool
	}{{"alice", deadPID(t), true}, {"erin", deadPID(t), true}, {"bob", deadPID(t), true}, {"frank", deadPID(t), true},
		{"carol", deadPID(t), false}, {"dave", os.Getpid(), true}} {
		h.run("team", "add-member", "demo", a.name)
		if _, err := h.api.RegisterAgent(ctx, model.Agent{Team: "demo", Name: a.name, AgentID: a.name + "-id", RunID: "before",
			Backend: "subprocess", PID: a.pid, Command: []string{command}, Cwd: dir}); err != nil {
			t.Fatal(err)
		}
		if a.session {
			if _, err := h.api.SaveSession(ctx, model.Session{Team: "demo", Agent: a.name, RunID: "before", Client: "claude",
				SessionID: a.name + "-session", Command: []string{command}, Cwd: dir, Backend: "subprocess"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := h.api.RequestAgentStop(ctx, "demo", "bob", "before"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.AgentExitedRun(ctx, "demo", "erin", "before", store.InterruptedExit); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.AgentExitedRun(ctx, "demo", "frank", "before", 1); err != nil {
		t.Fatal(err)
	}
	var restored []model.RestoredAgent
	h.json(&restored, "agent", "restore")
	names := []string{}
	for _, r := range restored {
		if r.Status != "resumed" {
			t.Fatalf("restore failed: %+v", r)
		}
		names = append(names, r.Agent)
	}
	if strings.Join(names, ",") != "alice,erin" {
		t.Fatalf("restored %v, want alice and erin", names)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(log)
		if strings.Contains(string(data), "alice") && strings.Contains(string(data), "erin") {
			if !strings.Contains(string(data), "alice-session") || !strings.Contains(string(data), "erin-session") ||
				strings.Contains(string(data), "bob") || strings.Contains(string(data), "frank") {
				t.Fatalf("attempts: %q", data)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("alice and erin were not resumed: %q", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Both now run again or exited on their own, so a second restore has
	// nothing to do for them.
	h.json(&restored, "agent", "restore")
	for _, r := range restored {
		if r.Agent == "alice" || r.Agent == "erin" {
			t.Fatalf("restored twice: %+v", restored)
		}
	}
}
