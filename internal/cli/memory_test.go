package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/model"
)

func TestMemoryCLIRecreationAndVersionChecks(t *testing.T) {
	h := newHarness(t)
	h.run("team", "spawn-team", "demo")
	h.run("team", "add-member", "demo", "alice")
	var init model.MemoryResult
	h.json(&init, "memory", "init", "demo", "alice")
	var read model.MemoryResult
	h.json(&read, "memory", "read", "demo", "alice", "MEMORY.md")
	original := read.Documents[0]
	var write model.MemoryResult
	h.json(&write, "memory", "write", "demo", "alice", "MEMORY.md", "# Memory\nReusable fact.\n", "--base-hash", original.Hash)
	if _, ok := h.run("memory", "write", "demo", "alice", "MEMORY.md", "stale", "--base-hash", original.Hash); ok {
		t.Fatal("stale write succeeded")
	}
	h.run("team", "remove-member", "demo", "alice")
	h.run("team", "add-member", "demo", "alice")
	h.json(&read, "memory", "read", "demo", "alice", "MEMORY.md")
	if !strings.Contains(read.Documents[0].Content, "Reusable fact") {
		t.Fatal(read)
	}
	var history model.MemoryResult
	h.json(&history, "memory", "history", "demo", "alice", "MEMORY.md")
	if len(history.History) < 2 {
		t.Fatal(history)
	}
	h.json(&init, "memory", "init", "demo", "alice", "--user", "other")
	h.json(&read, "memory", "read", "demo", "alice", "MEMORY.md", "--user", "other")
	if strings.Contains(read.Documents[0].Content, "Reusable fact") {
		t.Fatal("cross-user leak")
	}
	if _, ok := h.run("memory", "read", "demo", "alice", "../AGENTS.md"); ok {
		t.Fatal("traversal succeeded")
	}
}

func TestPersonalRulesReloadAtHookBoundaries(t *testing.T) {
	h, ctx := newHookTeam(t)
	t.Setenv("TEAM_WORKER_RUN_ID", "example-run")
	if _, err := h.api.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", RunID: "example-run", Backend: "subprocess", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	id := model.MemoryIdentity{Team: "demo", Name: "alice"}
	_, err := h.api.Memory(ctx, id, "init", model.MemoryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	read, err := h.api.Memory(ctx, id, "read", model.MemoryRequest{Path: "AGENTS.md"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.api.Memory(ctx, id, "write", model.MemoryRequest{Path: "AGENTS.md", BaseHash: read.Documents[0].Hash, Content: "# Rules\nUse marker RULE_A.\n"})
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := h.hook(`{"source":"resume"}`, "session-start", "--team", "demo", "--agent", "alice")
	if !strings.Contains(initial.Specific.Context, "RULE_A") {
		t.Fatal(initial)
	}
	status, err := h.api.Memory(ctx, id, "status", model.MemoryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(status.Status.Directory, "AGENTS.md"), []byte("# Rules\nUse marker RULE_B.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	updated, _ := h.hook(`{}`, "prompt-submit", "--team", "demo", "--agent", "alice")
	if !strings.Contains(updated.Specific.Context, "RULE_B") || strings.Contains(updated.Specific.Context, "RULE_A") {
		t.Fatal(updated)
	}
	unchanged, _ := h.hook(`{}`, "prompt-submit", "--team", "demo", "--agent", "alice")
	if unchanged.Specific.Context != "" {
		t.Fatal("unchanged rules injected repeatedly")
	}
	compacted, _ := h.hook(`{"source":"compact"}`, "session-start", "--team", "demo", "--agent", "alice")
	if !strings.Contains(compacted.Specific.Context, "RULE_B") {
		t.Fatal(compacted)
	}
}

func TestMemoryDryRunDoesNotCreateProfile(t *testing.T) {
	h := newHarness(t)
	id := model.MemoryIdentity{Team: "demo", Name: "preview"}
	_, err := h.api.Memory(context.Background(), id, "context", model.MemoryRequest{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.api.Memory(context.Background(), id, "status", model.MemoryRequest{}); err == nil {
		t.Fatal("dry run persisted profile")
	}
}

func TestRejectedSpawnKeepsCurrentMemoryRun(t *testing.T) {
	h, ctx := newHookTeam(t)
	id := model.MemoryIdentity{Team: "demo", Name: "alice"}
	if _, err := h.api.RegisterAgent(ctx, model.Agent{Team: "demo", Name: "alice", RunID: "current-run", Backend: "subprocess", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.api.Memory(ctx, id, "prepare", model.MemoryRequest{RunID: "current-run"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.run("spawn", "subprocess", "--team", "demo", "--agent-name", "alice", "--cwd", t.TempDir(), "--", "sh"); ok {
		t.Fatal("running member was replaced without authorization")
	}
	status, err := h.api.Memory(ctx, id, "status", model.MemoryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if status.Status.RunID != "current-run" {
		t.Fatalf("rejected spawn published a different run: %+v", status.Status)
	}
}
