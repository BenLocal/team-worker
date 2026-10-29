package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/server"
	"github.com/benlocal/team-worker/internal/store"
)

type harness struct {
	t   *testing.T
	api *client.Client
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cli.db"))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.New(st))
	t.Cleanup(func() {
		ts.Close()
		st.Close()
	})
	t.Setenv("TEAM_WORKER_AGENT_NAME", "leader")
	t.Setenv("TEAM_WORKER_USER", "")
	t.Setenv("TEAM_WORKER_DATA_DIR", t.TempDir())
	return &harness{t: t, api: client.NewHTTP(ts.URL, ts.Client())}
}

// run executes the CLI and returns stdout and the exit status.
func (h *harness) run(args ...string) (string, bool) {
	h.t.Helper()
	root := NewRoot(func() *client.Client { return h.api })
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	err := root.Execute()
	if err != nil && !errors.As(err, new(exitError)) {
		h.t.Fatalf("%v: unexpected error %v", args, err)
	}
	return out.String(), err == nil
}

func (h *harness) json(v any, args ...string) {
	h.t.Helper()
	out, ok := h.run(append([]string{"--json"}, args...)...)
	if !ok {
		h.t.Fatalf("%v failed: %s", args, out)
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		h.t.Fatalf("%v: decode %q: %v", args, out, err)
	}
}

func TestTeamTaskInboxFlow(t *testing.T) {
	h := newHarness(t)

	var created map[string]string
	h.json(&created, "team", "spawn-team", "demo", "-d", "build auth")
	if created["status"] != "created" || created["leaderName"] != "leader" || created["leadAgentId"] == "" {
		t.Fatalf("spawn-team: %v", created)
	}
	var added struct {
		Status string       `json:"status"`
		Member model.Member `json:"member"`
	}
	h.json(&added, "team", "add-member", "demo", "alice")
	if added.Status != "added" || added.Member.Name != "alice" || added.Member.AgentType != "general-purpose" {
		t.Fatalf("add-member: %+v", added)
	}

	var a, b model.Task
	h.json(&a, "task", "create", "demo", "implement oauth", "-o", "alice", "-p", "high")
	h.json(&b, "task", "create", "demo", "write tests", "--agent", "alice", "--blocked-by", a.ID)
	if a.Owner != "alice" || a.Priority != model.PriorityHigh || b.Status != model.StatusBlocked {
		t.Fatalf("create: a=%+v b=%+v", a, b)
	}

	var mine []model.Task
	h.json(&mine, "task", "list", "demo", "--owner", "alice")
	if len(mine) != 2 || mine[1].BlockedBy[0] != a.ID {
		t.Fatalf("list: %+v", mine)
	}

	t.Setenv("TEAM_WORKER_AGENT_NAME", "alice")
	var started model.Task
	h.json(&started, "task", "update", "demo", a.ID, "--status", "in_progress")
	if started.LockedBy != "alice" {
		t.Fatalf("lock: %+v", started)
	}

	t.Setenv("TEAM_WORKER_AGENT_NAME", "bob")
	out, ok := h.run("--json", "task", "update", "demo", a.ID, "--status", "in_progress")
	if ok || !strings.Contains(out, "locked by 'alice'") {
		t.Fatalf("expected lock conflict, got ok=%v %s", ok, out)
	}

	t.Setenv("TEAM_WORKER_AGENT_NAME", "alice")
	h.json(&started, "task", "update", "demo", a.ID, "--status", "completed")
	var got model.Task
	h.json(&got, "task", "get", "demo", b.ID)
	if got.Status != model.StatusPending || len(got.BlockedBy) != 0 {
		t.Fatalf("dependent not unblocked: %+v", got)
	}

	var sent model.Message
	h.json(&sent, "inbox", "send", "demo", "leader", "oauth done")
	if sent.From != "alice" || sent.To != "leader" || sent.Type != "message" {
		t.Fatalf("send: %+v", sent)
	}
	t.Setenv("TEAM_WORKER_AGENT_NAME", "leader")
	var peek struct {
		Count int `json:"count"`
	}
	h.json(&peek, "inbox", "peek", "demo")
	var received []model.Message
	h.json(&received, "inbox", "receive", "demo")
	if peek.Count != 1 || len(received) != 1 || received[0].Content != "oauth done" {
		t.Fatalf("peek=%d receive=%+v", peek.Count, received)
	}
	h.json(&received, "inbox", "receive", "demo")
	if len(received) != 0 {
		t.Fatalf("message delivered twice: %+v", received)
	}

	var bc struct {
		Count      int      `json:"count"`
		Recipients []string `json:"recipients"`
	}
	h.json(&bc, "inbox", "broadcast", "demo", "standup")
	if bc.Count != 1 || bc.Recipients[0] != "alice" {
		t.Fatalf("broadcast: %+v", bc)
	}

	var cleaned map[string]string
	h.json(&cleaned, "team", "cleanup", "demo")
	if cleaned["status"] != "cleaned" {
		t.Fatalf("cleanup: %v", cleaned)
	}
	out, ok = h.run("--json", "team", "status", "demo")
	if ok || !strings.Contains(out, `"error": "Team 'demo' not found"`) {
		t.Fatalf("status after cleanup: ok=%v %s", ok, out)
	}
}

func TestReceiveWaitWakesOnSend(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	ctx := context.Background()

	go func() {
		time.Sleep(200 * time.Millisecond)
		h.api.Send(ctx, "demo", store.SendInput{From: "alice", To: "leader", Content: "ping"})
	}()
	start := time.Now()
	msgs, err := h.api.Receive(ctx, "demo", "leader", "", 10, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "ping" {
		t.Fatalf("receive: %+v", msgs)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("long-poll did not wake on send: %v", elapsed)
	}

	start = time.Now()
	msgs, _ = h.api.Receive(ctx, "demo", "leader", "", 10, 300*time.Millisecond)
	if len(msgs) != 0 || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("empty wait should time out: %+v after %v", msgs, time.Since(start))
	}
}

func TestTaskWaitTimeoutBeatsPollInterval(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	h.run("task", "create", "demo", "never done")
	start := time.Now()
	out, ok := h.run("--json", "task", "wait", "demo", "--timeout", "0.5", "--poll-interval", "5")
	if ok || !strings.Contains(out, `"status":"timeout"`) {
		t.Fatalf("want timeout, got ok=%v %s", ok, out)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout waited a full poll interval: %v", elapsed)
	}
}

func TestTaskWaitCompletes(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	var task model.Task
	h.json(&task, "task", "create", "demo", "only task")

	go func() {
		time.Sleep(200 * time.Millisecond)
		ctx := context.Background()
		h.api.Send(ctx, "demo", store.SendInput{From: "alice", To: "leader", Content: "finished"})
		done := model.StatusCompleted
		h.api.UpdateTask(ctx, "demo", task.ID, store.UpdateTaskInput{Status: &done})
	}()
	out, ok := h.run("--json", "task", "wait", "demo", "--poll-interval", "0.2", "--timeout", "10")
	if !ok {
		t.Fatalf("wait failed: %s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var result map[string]any
	json.Unmarshal([]byte(lines[len(lines)-1]), &result)
	if result["status"] != "completed" || result["messages_received"] != 1.0 {
		t.Fatalf("wait result: %s", out)
	}
}
