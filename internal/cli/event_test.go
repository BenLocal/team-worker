package cli

import (
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestEventHookCommands(t *testing.T) {
	h := newHarness(t)
	var types []model.EventType
	h.json(&types, "event", "types")
	if len(types) != len(model.EventTypes) || types[0].Name != "AfterWorkerSpawn" {
		t.Fatalf("%+v", types)
	}

	if out, ok := h.run("--json", "event", "hook", "add", "-e", "Nope", "-c", "true"); ok || !strings.Contains(out, "Unknown event type") {
		t.Fatalf("unknown event accepted: %s", out)
	}
	if out, ok := h.run("--json", "event", "hook", "add", "-e", "WorkerExit", "-a", "python", "-c", "pkg.fn"); ok || !strings.Contains(out, "only shell") {
		t.Fatalf("python hook accepted: %s", out)
	}
	var added model.EventHook
	h.json(&added, "event", "hook", "add", "-e", "WorkerExit", "-c", `echo "bye $TEAM_WORKER_EVENT_AGENT_NAME"`, "-p", "2")
	h.json(&added, "event", "hook", "add", "-e", "WorkerExit", "-t", "demo", "-c", "exit 1")
	var hooks []model.EventHook
	h.json(&hooks, "event", "hook", "list")
	if len(hooks) != 2 || hooks[0].Command != "exit 1" || hooks[1].Priority != 2 || hooks[0].Team != "demo" {
		t.Fatalf("%+v", hooks)
	}

	// Another team only runs the hook for every team.
	var res model.HookTestResult
	h.json(&res, "event", "hook", "test", "-e", "WorkerExit", "-t", "other", "-n", "alice")
	if len(res.Results) != 1 || strings.TrimSpace(res.Results[0].Output) != "bye alice" || res.Payload["team_name"] != "other" {
		t.Fatalf("%+v", res)
	}
	out, ok := h.run("event", "hook", "test", "-e", "WorkerExit", "-t", "demo")
	if ok || !strings.Contains(out, "2 hook(s) executed") || !strings.Contains(out, "FAILED (exit 1)") || !strings.Contains(out, "bye test-agent") {
		t.Fatalf("a failing hook should fail the test: %v %s", ok, out)
	}

	out, ok = h.run("event", "hook", "remove", "-e", "WorkerExit", "-c", "exit 1")
	if !ok || !strings.Contains(out, "Removed 1 hook(s) for WorkerExit") {
		t.Fatalf("%v %s", ok, out)
	}
	h.json(&hooks, "event", "hook", "list")
	if len(hooks) != 1 {
		t.Fatalf("%+v", hooks)
	}
}
