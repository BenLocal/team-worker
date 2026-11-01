package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/store"
)

// operatorFixture writes a config.json with one remote. --remote changes the
// process environment, so every variable it touches is registered with
// t.Setenv first and restored after the test.
func operatorFixture(t *testing.T, h *harness, mode os.FileMode) {
	t.Helper()
	for _, key := range append([]string{"TEAM_WORKER_SERVER_URL", "TEAM_WORKER_API_KEY", "TEAM_WORKER_AGENT_NAME",
		"TEAM_WORKER_NO_AUTOSTART", "NO_PROXY", "no_proxy"}, localIdentity...) {
		t.Setenv(key, os.Getenv(key))
	}
	// A local agent's identity must not reach the remote.
	t.Setenv("TEAM_WORKER_AGENT_NAME", "alice")
	t.Setenv("TEAM_WORKER_TEAM_NAME", "local-team")
	data, err := json.Marshal(map[string]any{"remotes": map[string]any{
		"prod": map[string]any{"server_url": "http://team-worker.example.internal:28088/team-worker-api", "api_key": "secret-key"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(os.Getenv("HOME"), ".team-worker")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, mode); err != nil {
		t.Fatal(err)
	}
	h.run("team", "spawn-team", "demo")
}

func TestRemoteActsAsOperator(t *testing.T) {
	h := newHarness(t)
	operatorFixture(t, h, 0o600)
	if out, ok := h.run("--remote", "prod", "inbox", "send", "demo", "leader", "status?"); !ok {
		t.Fatalf("%s", out)
	}
	msgs, err := h.api.Peek(context.Background(), "demo", "leader", "")
	if err != nil || len(msgs) != 1 || msgs[0].From != "operator" {
		t.Fatalf("the message should come from operator, not the local agent: %+v %v", msgs, err)
	}
	if os.Getenv("TEAM_WORKER_TEAM_NAME") != "" || os.Getenv("TEAM_WORKER_SERVER_URL") != "http://team-worker.example.internal:28088/team-worker-api" ||
		!strings.Contains(os.Getenv("NO_PROXY"), "team-worker.example.internal") {
		t.Fatalf("operator environment not applied: team=%q url=%q no_proxy=%q",
			os.Getenv("TEAM_WORKER_TEAM_NAME"), os.Getenv("TEAM_WORKER_SERVER_URL"), os.Getenv("NO_PROXY"))
	}

	out, ok := h.run("--remote", "prod", "--json", "config", "health")
	var got health
	if err := json.Unmarshal([]byte(out), &got); err != nil || !ok || got.Remote != "prod" || !got.APIKeySet ||
		got.ServerURLSource != "remotes.prod" || strings.Contains(out, "secret-key") {
		t.Fatalf("%v %v %s", ok, err, out)
	}
}

func TestRemoteRejectsUnknownRemotes(t *testing.T) {
	h := newHarness(t)
	operatorFixture(t, h, 0o600)
	if out, ok := h.run("--json", "--remote", "nope", "team", "discover"); ok || !strings.Contains(out, "configured: prod") {
		t.Fatalf("unknown remote: %v %s", ok, out)
	}
}

func TestRemoteTaskWaitLeavesTheLeadersInbox(t *testing.T) {
	h := newHarness(t)
	operatorFixture(t, h, 0o600)
	ctx := context.Background()
	done, err := h.api.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "still open"}); err != nil {
		t.Fatal(err)
	}
	if out, ok := h.run("task", "update", "demo", done.ID, "--status", "completed"); !ok {
		t.Fatalf("%s", out)
	}
	if _, err := h.api.Send(ctx, "demo", store.SendInput{From: "bob", To: "leader", Content: "report"}); err != nil {
		t.Fatal(err)
	}
	// Only the chosen task counts, so the open one does not keep it waiting.
	if out, ok := h.run("--remote", "prod", "--json", "task", "wait", "demo", "--task", done.ID, "-t", "5"); !ok ||
		!strings.Contains(out, `"completed"`) {
		t.Fatalf("%v %s", ok, out)
	}
	if msgs, err := h.api.Peek(ctx, "demo", "leader", ""); err != nil || len(msgs) != 1 {
		t.Fatalf("the leader's report must stay in its inbox: %+v %v", msgs, err)
	}
	if out, ok := h.run("--json", "--remote", "prod", "task", "wait", "demo", "--task", "missing", "-t", "5"); ok ||
		!strings.Contains(out, "not found") {
		t.Fatalf("an unknown task should fail: %s", out)
	}
}

func TestHealthFlagsReadableConfigWithKeys(t *testing.T) {
	h := newHarness(t)
	operatorFixture(t, h, 0o644)
	out, ok := h.run("--remote", "prod", "--json", "config", "health")
	if ok || !strings.Contains(out, "readable by other users") || strings.Contains(out, "secret-key") {
		t.Fatalf("%v %s", ok, out)
	}
}

func TestRemoteResumeRunsOnTheServer(t *testing.T) {
	h := newHarness(t)
	operatorFixture(t, h, 0o600)
	// The server looks the session up: the request reached it, nothing ran here.
	out, ok := h.run("--json", "--remote", "prod", "agent", "resume", "demo", "leader", "--keepalive")
	if ok || !strings.Contains(out, "No saved session for 'leader'") {
		t.Fatalf("%v %s", ok, out)
	}
}
