package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
	"github.com/benlocal/team-worker/internal/sysproc"
)

// subprocessAgent registers alice as a subprocess agent backed by a real
// process, with its log at target.
func subprocessAgent(t *testing.T) (*Server, *store.Store, *exec.Cmd, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, err := st.CreateTeam(ctx, store.CreateTeamInput{Name: "demo", LeaderName: "leader"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddMember(ctx, "demo", store.AddMemberInput{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = sysproc.Detached()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	logPath := filepath.Join(t.TempDir(), "alice.log")
	if err := os.WriteFile(logPath, []byte("line one\nworking on the task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", RunID: "run-1", Backend: "subprocess",
		PID: cmd.Process.Pid, Target: logPath}); err != nil {
		t.Fatal(err)
	}
	return New(st, nil), st, cmd, logPath
}

func TestTerminateStopsTheProcessAndReleasesTasks(t *testing.T) {
	srv, st, cmd, _ := subprocessAgent(t)
	ctx := context.Background()
	task, err := st.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "work", Owner: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	status := model.StatusInProgress
	if _, err := st.UpdateTask(ctx, "demo", task.ID, store.UpdateTaskInput{Status: &status, Caller: "alice"}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/teams/demo/agents/alice/terminate", strings.NewReader("{}")))
	var out model.AgentExit
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || len(out.Released) != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon did not end the agent's process")
	}
	a, err := st.GetAgent(ctx, "demo", "alice")
	if err != nil || a.Status != model.AgentExited || !a.StopRequested {
		t.Fatalf("the stop must be recorded as intended: %+v %v", a, err)
	}
}

func TestScreenIsReadByTheDaemon(t *testing.T) {
	srv, _, _, _ := subprocessAgent(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/teams/demo/agents/alice/screen?lines=5", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "working on the task") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestDeleteTeamStopsItsAgents(t *testing.T) {
	srv, _, cmd, _ := subprocessAgent(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/teams/demo", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deleted":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("deleting the team left its agent running")
	}
}
