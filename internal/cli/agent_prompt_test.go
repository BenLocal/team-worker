package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentPromptCLIAndSpawnReuse(t *testing.T) {
	h := newHarness(t)
	if out, ok := h.run("team", "spawn-team", "demo"); !ok {
		t.Fatal(out)
	}
	if out, ok := h.run("team", "add-member", "demo", "alice"); !ok {
		t.Fatal(out)
	}
	file := filepath.Join(t.TempDir(), "role.txt")
	if err := os.WriteFile(file, []byte("Review security\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, ok := h.run("agent", "prompt", "set", "demo", "alice", "--file", file); !ok {
		t.Fatal(out)
	}
	var shown struct {
		Prompt string `json:"prompt"`
	}
	h.json(&shown, "agent", "prompt", "show", "demo", "alice")
	if shown.Prompt != "Review security" {
		t.Fatalf("show=%+v", shown)
	}
	var res spawnResult
	h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", "alice", "--dry-run", "--", "sh")
	if !strings.Contains(res.Prompt, "Review security") {
		t.Fatalf("reuse: %s", res.Prompt)
	}
	h.json(&res, "spawn", "subprocess", "--team", "demo", "-n", "alice", "--task", "", "--dry-run", "--", "sh")
	if strings.Contains(res.Prompt, "Review security") {
		t.Fatal("explicit empty task reused stored prompt")
	}
	prompt, err := h.api.AgentPrompt(context.Background(), "demo", "alice", "")
	if err != nil || prompt != "Review security" {
		t.Fatalf("dry-run changed prompt=%q err=%v", prompt, err)
	}
	if out, ok := h.run("agent", "prompt", "set", "demo", "alice", ""); !ok {
		t.Fatal(out)
	}
	h.json(&shown, "agent", "prompt", "show", "demo", "alice")
	if shown.Prompt != "" {
		t.Fatal("clear failed")
	}
	if out, ok := h.runIn("new role", "agent", "prompt", "set", "demo", "alice"); !ok {
		t.Fatal(out)
	}
	h.json(&shown, "agent", "prompt", "show", "demo", "alice")
	if shown.Prompt != "new role" {
		t.Fatal("stdin failed")
	}
	if out, ok := h.run("--json", "agent", "prompt", "show", "demo", "missing"); ok || !strings.Contains(out, "not found") {
		t.Fatalf("missing=%s", out)
	}
}
