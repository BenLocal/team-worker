package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/store"
)

type hookOutput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
	Specific struct {
		Event   string `json:"hookEventName"`
		Context string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

func (h *harness) hook(stdin string, args ...string) (hookOutput, string) {
	h.t.Helper()
	out, ok := h.runIn(stdin, append([]string{"hook"}, args...)...)
	if !ok {
		h.t.Fatalf("hook %v failed: %s", args, out)
	}
	var res hookOutput
	if strings.TrimSpace(out) != "" {
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			h.t.Fatalf("hook output %q: %v", out, err)
		}
	}
	return res, out
}

func newHookTeam(t *testing.T) (*harness, context.Context) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	h.run("team", "add-member", "demo", "alice")
	return h, context.Background()
}

func done() *model.TaskStatus { s := model.StatusCompleted; return &s }

func TestStopHookDeliversMessages(t *testing.T) {
	h, ctx := newHookTeam(t)
	h.api.Send(ctx, "demo", store.SendInput{From: "leader", To: "alice", Content: "please also add tests"})
	h.api.Send(ctx, "demo", store.SendInput{From: "leader", To: "alice", Type: "shutdown_request", Content: "wrap up"})
	res, _ := h.hook(`{"stop_hook_active":false}`, "stop", "--team", "demo", "--agent", "alice", "--wait", "1")
	if res.Decision != "block" || !strings.Contains(res.Reason, "please also add tests") ||
		!strings.Contains(res.Reason, "approve-shutdown demo") {
		t.Fatalf("stop hook: %+v", res)
	}
	if msgs, _ := h.api.Peek(ctx, "demo", "alice", ""); len(msgs) != 0 {
		t.Fatalf("delivered messages should be consumed: %+v", msgs)
	}
}

func TestStopHookWorkerTasksAndIdle(t *testing.T) {
	h, ctx := newHookTeam(t)
	task, _ := h.api.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "build api", Owner: "alice"})
	res, _ := h.hook(`{"stop_hook_active":false}`, "stop", "--team", "demo", "--agent", "alice", "--wait", "1")
	if res.Decision != "block" || !strings.Contains(res.Reason, "build api") {
		t.Fatalf("open task should keep the worker going: %+v", res)
	}
	h.api.UpdateTask(ctx, "demo", task.ID, store.UpdateTaskInput{Status: done()})

	start := time.Now()
	_, out := h.hook(`{"stop_hook_active":true}`, "stop", "--team", "demo", "--agent", "alice", "--wait", "1")
	if strings.TrimSpace(out) != "" || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("idle worker should wait, then be allowed to stop: %q after %v", out, time.Since(start))
	}
	h.hook(`{}`, "stop", "--team", "demo", "--agent", "alice", "--wait", "0")
	idle, _ := h.api.Peek(ctx, "demo", "leader", "")
	if len(idle) != 1 || idle[0].Type != "idle" {
		t.Fatalf("leader should get exactly one idle notice: %+v", idle)
	}
}

func TestStopHookWakesOnNewWork(t *testing.T) {
	h, ctx := newHookTeam(t)
	first, _ := h.api.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "design", Owner: "leader"})
	h.api.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "implement", Owner: "alice", BlockedBy: []string{first.ID}})
	go func() {
		time.Sleep(300 * time.Millisecond)
		h.api.UpdateTask(ctx, "demo", first.ID, store.UpdateTaskInput{Status: done()})
	}()
	start := time.Now()
	res, _ := h.hook(`{}`, "stop", "--team", "demo", "--agent", "alice", "--wait", "10")
	if res.Decision != "block" || !strings.Contains(res.Reason, "New work") || !strings.Contains(res.Reason, "implement") {
		t.Fatalf("unblocked task should wake the worker: %+v", res)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("hook did not wake on the change: %v", time.Since(start))
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		h.api.Send(ctx, "demo", store.SendInput{From: "leader", To: "alice", Content: "status?"})
	}()
	h.api.UpdateTask(ctx, "demo", first.ID, store.UpdateTaskInput{}) // unrelated write
	tasks, _ := h.api.ListTasks(ctx, "demo", store.TaskFilter{Owner: "alice"})
	h.api.UpdateTask(ctx, "demo", tasks[0].ID, store.UpdateTaskInput{Status: done()})
	res, _ = h.hook(`{"stop_hook_active":true}`, "stop", "--team", "demo", "--agent", "alice", "--wait", "10")
	if res.Decision != "block" || !strings.Contains(res.Reason, "status?") {
		t.Fatalf("message during the wait should wake the worker: %+v", res)
	}
}

func TestStopHookLeaderProgress(t *testing.T) {
	h, ctx := newHookTeam(t)
	task, _ := h.api.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "build", Owner: "alice"})
	go func() {
		time.Sleep(300 * time.Millisecond)
		h.api.UpdateTask(ctx, "demo", task.ID, store.UpdateTaskInput{Status: done()})
	}()
	res, _ := h.hook(`{}`, "stop", "--team", "demo", "--agent", "leader", "--role", "leader", "--wait", "10")
	if res.Decision != "block" || !strings.Contains(res.Reason, "1/1 tasks completed") ||
		!strings.Contains(res.Reason, "pending -> completed") {
		t.Fatalf("leader progress: %+v", res)
	}
	_, out := h.hook(`{}`, "stop", "--team", "demo", "--agent", "leader", "--role", "leader", "--wait", "10")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("leader should stop once everything is completed: %q", out)
	}
}

func TestPostToolUseHookThrottles(t *testing.T) {
	h, ctx := newHookTeam(t)
	h.api.Send(ctx, "demo", store.SendInput{From: "leader", To: "alice", Content: "use port 8080"})
	res, _ := h.hook(`{}`, "post-tool-use", "--team", "demo", "--agent", "alice")
	if res.Specific.Event != "PostToolUse" || !strings.Contains(res.Specific.Context, "use port 8080") {
		t.Fatalf("post-tool-use: %+v", res)
	}
	h.api.Send(ctx, "demo", store.SendInput{From: "leader", To: "alice", Content: "second"})
	if _, out := h.hook(`{}`, "post-tool-use", "--team", "demo", "--agent", "alice"); strings.TrimSpace(out) != "" {
		t.Fatalf("second call within the interval should be skipped: %q", out)
	}
	if msgs, _ := h.api.Peek(ctx, "demo", "alice", ""); len(msgs) != 1 {
		t.Fatalf("throttled call must not consume messages: %+v", msgs)
	}
}

func TestSessionStartHookRestoresContext(t *testing.T) {
	h, ctx := newHookTeam(t)
	h.api.SetTeamPrompt(ctx, "demo", "Repo is /src/app")
	if _, err := h.api.SetAgentPrompt(ctx, "demo", "alice", "", "Keep the security reviewer role"); err != nil {
		t.Fatal(err)
	}
	h.api.CreateTask(ctx, "demo", store.CreateTaskInput{Subject: "write docs", Owner: "alice"})
	res, _ := h.hook(`{"source":"compact"}`, "session-start", "--team", "demo", "--agent", "alice")
	for _, want := range []string{"compact", "- Name: alice", "Repo is /src/app", "write docs",
		"team-worker task list demo --owner alice", "Keep the security reviewer role"} {
		if !strings.Contains(res.Specific.Context, want) {
			t.Errorf("session-start context missing %q", want)
		}
	}
	if _, out := h.hook(`{"source":"startup"}`, "session-start", "--team", "demo", "--agent", "alice"); strings.TrimSpace(out) != "" {
		t.Fatalf("startup already has the first prompt: %q", out)
	}
}

func TestHookSettings(t *testing.T) {
	h := newHarness(t)
	out, ok := h.run("hook", "settings", "--team", "demo", "--agent", "leader", "--role", "leader")
	if !ok {
		t.Fatal(out)
	}
	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(out), &settings); err != nil {
		t.Fatal(err)
	}
	stop := settings.Hooks["Stop"][0].Hooks[0]
	if !strings.Contains(stop.Command, "hook stop --team 'demo' --agent 'leader' --role leader") || stop.Timeout <= 600 {
		t.Fatalf("stop hook: %+v", stop)
	}
	if settings.Hooks["SessionStart"][0].Matcher != "startup|resume|clear|compact" || len(settings.Hooks["PostToolUse"]) != 1 {
		t.Fatalf("settings: %+v", settings.Hooks)
	}
}

func TestHooksStaySilentWhenDaemonIsDown(t *testing.T) {
	dead := httptest.NewServer(nil)
	url := dead.URL
	dead.Close()
	h := newHarness(t)
	h.api = client.NewHTTP(url, dead.Client())
	for _, event := range []string{"stop", "post-tool-use", "session-start"} {
		args := []string{"hook", event, "--team", "demo", "--agent", "alice"}
		if event == "stop" {
			args = append(args, "--wait", "1")
		}
		out, ok := h.runIn(`{"source":"compact"}`, args...)
		if !ok || strings.TrimSpace(out) != "" {
			t.Errorf("%s: want silent success, got ok=%v %q", event, ok, out)
		}
	}
}
