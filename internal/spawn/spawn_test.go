package spawn

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPrepare(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("claude permission flag is skipped for root")
	}
	cases := []struct {
		name        string
		command     []string
		interactive bool
		want        []string
		paste       bool
		stdin       bool
	}{
		{"claude tmux", []string{"claude"}, true, []string{"claude", "--dangerously-skip-permissions", "P"}, false, false},
		{"claude subprocess", []string{"claude"}, false, []string{"claude", "--dangerously-skip-permissions", "-p", "P"}, false, false},
		{"codex tmux", []string{"codex"}, true, []string{"codex", "--dangerously-bypass-approvals-and-sandbox", "P"}, false, false},
		{"codex subprocess", []string{"codex"}, false, []string{"codex", "exec", "--dangerously-bypass-approvals-and-sandbox", "P"}, false, false},
		{"gemini tmux", []string{"gemini"}, true, []string{"gemini", "--yolo", "-i", "P"}, false, false},
		{"unknown tmux", []string{"my-agent", "--x"}, true, []string{"my-agent", "--x"}, true, false},
		{"unknown subprocess", []string{"my-agent"}, false, []string{"my-agent"}, false, true},
	}
	for _, c := range cases {
		p := Prepare(c.command, "P", true, c.interactive)
		if !slices.Equal(p.Args, c.want) || (p.Paste != "") != c.paste || (p.Stdin != "") != c.stdin {
			t.Errorf("%s: args=%q paste=%q stdin=%q", c.name, p.Args, p.Paste, p.Stdin)
		}
	}
	if p := Prepare([]string{"dsh"}, "P", true, true); p.Env["DSH_PERMISSION_MODE"] != "danger-full-access" {
		t.Errorf("dsh permission env missing: %v", p.Env)
	}
	if p := Prepare([]string{"claude"}, "P", false, true); slices.Contains(p.Args, "--dangerously-skip-permissions") {
		t.Errorf("skip-permissions off still added the flag: %q", p.Args)
	}
}

func TestBuildPromptUsesTeamWorkerCommands(t *testing.T) {
	p := BuildPrompt(PromptParams{AgentName: "alice", AgentID: "id1", AgentType: "dev", Team: "demo",
		Leader: "leader", Workspace: "/w/alice", TeamPrompt: "Repo is at /src", Task: "Build auth"})
	for _, want := range []string{"- Name: alice", "/w/alice", "## Team Instructions", "Repo is at /src",
		"Build auth", "team-worker task list demo --owner alice", "team-worker inbox send demo leader",
		"team-worker lifecycle idle demo"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Contains(p, "clawteam") {
		t.Error("prompt still mentions clawteam")
	}
}

func TestStartupAction(t *testing.T) {
	if a := startupAction("claude", "do you trust this folder?\npress enter to continue"); a != "enter" {
		t.Errorf("claude trust: %q", a)
	}
	if a := startupAction("claude", "bypass permissions mode\n1. no\n2. yes, i accept"); a != "down-enter" {
		t.Errorf("claude permissions: %q", a)
	}
	if a := startupAction("codex", "do you trust the contents of this directory?\npress enter to continue"); a != "enter" {
		t.Errorf("codex trust: %q", a)
	}
	if a := startupAction("claude", "welcome\n> "); a != "" {
		t.Errorf("plain screen: %q", a)
	}
}

func TestSubprocessRunsExitHook(t *testing.T) {
	dir := t.TempDir()
	hookLog := filepath.Join(dir, "hook.log")
	// A fake team-worker binary records the exit hook call.
	bin := filepath.Join(dir, "team-worker")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" > "+hookLog+"\n"), 0o755)
	cwd := filepath.Join(dir, "ws")
	os.MkdirAll(cwd, 0o755)
	res, err := Launch(Options{
		Backend:   "subprocess",
		Command:   []string{"sh", "-c", "pwd; cat; echo agent=$TEAM_WORKER_AGENT_NAME"},
		Team:      "demo",
		Agent:     "alice",
		Prompt:    "hello prompt",
		Cwd:       cwd,
		Env:       map[string]string{"TEAM_WORKER_AGENT_NAME": "alice"},
		WorkerBin: bin,
		LogPath:   filepath.Join(dir, "logs", "alice.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.PID <= 0 {
		t.Fatalf("no pid: %+v", res)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(hookLog); err == nil && len(data) > 0 {
			if got := strings.TrimSpace(string(data)); got != "lifecycle on-exit --team demo --agent alice" {
				t.Fatalf("hook args: %q", got)
			}
			out, _ := os.ReadFile(filepath.Join(dir, "logs", "alice.log"))
			for _, want := range []string{cwd, "hello prompt", "agent=alice"} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("log missing %q:\n%s", want, out)
				}
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("exit hook did not run")
}

func TestLaunchRejectsMissingCommand(t *testing.T) {
	_, err := Launch(Options{Backend: "subprocess", Command: []string{"no-such-agent-cli-xyz"}, Cwd: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "not found in PATH") {
		t.Fatalf("want not-found error, got %v", err)
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh missing")
	}
}
