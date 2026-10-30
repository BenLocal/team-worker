package spawn

import (
	"github.com/benlocal/team-worker/internal/agent"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
		Identity:  map[string]string{"TEAM_WORKER_AGENT_NAME": "alice"},
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

func TestComposeEnvLayers(t *testing.T) {
	caller := []string{"PATH=/usr/bin", "HOME=/home/u", "TERM=dumb", "TMUX=/tmp/tmux", "CLAUDECODE=1",
		"SECRET=s3", "GATEWAY_KEY=k1", "KEEP=yes"}
	env, err := ComposeEnv(EnvSpec{
		Caller:      caller,
		Adapter:     agent.EnvPlan{Unset: []string{"CLAUDECODE"}, Set: map[string]string{"A": "adapter"}},
		AdapterName: "claude (builtin)",
		Layers: []EnvLayer{
			{Name: "template", Set: map[string]string{"A": "template", "API_KEY": "${GATEWAY_KEY}"}},
			{Name: "--env", Set: map[string]string{"A": "cli"}, Unset: []string{"SECRET"}},
		},
		Identity:  map[string]string{"TEAM_WORKER_AGENT_NAME": "alice"},
		WorkerBin: "/opt/tw/bin/team-worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "cli", "API_KEY": "k1", "TERM": "xterm-256color", "KEEP": "yes",
		"TEAM_WORKER_AGENT_NAME": "alice", "PATH": "/opt/tw/bin:/usr/bin"}
	for k, v := range want {
		if env.Vars[k] != v {
			t.Errorf("%s = %q, want %q", k, env.Vars[k], v)
		}
	}
	for _, k := range []string{"TMUX", "CLAUDECODE", "SECRET"} {
		if _, ok := env.Vars[k]; ok {
			t.Errorf("%s should be removed", k)
		}
	}
	if env.Origin["A"] != "--env" || env.Origin["API_KEY"] != "template" || env.Origin["KEEP"] != "caller" {
		t.Errorf("origins: %v", env.Origin)
	}
}

func TestComposeEnvGuards(t *testing.T) {
	identity := map[string]string{"TEAM_WORKER_AGENT_NAME": "alice"}
	_, err := ComposeEnv(EnvSpec{Identity: identity,
		Layers: []EnvLayer{{Name: "--env", Set: map[string]string{"TEAM_WORKER_AGENT_NAME": "mallory"}}}})
	if err == nil || !strings.Contains(err.Error(), "team-worker manages") {
		t.Fatalf("identity override must fail: %v", err)
	}
	_, err = ComposeEnv(EnvSpec{Layers: []EnvLayer{{Name: "template", Set: map[string]string{"K": "${NOPE}"}}}})
	if err == nil || !strings.Contains(err.Error(), "${NOPE} is not set") {
		t.Fatalf("missing reference must fail: %v", err)
	}
	env, err := ComposeEnv(EnvSpec{Caller: []string{"PATH=/bin", "HOME=/h", "TOKEN=t"},
		Adapter: agent.EnvPlan{NoInherit: true, Pass: []string{"PATH"}}})
	if err != nil {
		t.Fatal(err)
	}
	if env.Vars["PATH"] != "/bin" || env.Vars["HOME"] != "" || env.Vars["TOKEN"] != "" {
		t.Fatalf("inherit=false should keep only pass: %v", env.Vars)
	}
}

func TestBuildUsesAdapterAndChecksCommand(t *testing.T) {
	b, err := Build(Options{Backend: "subprocess", Command: []string{"sh", "-c", "true"}, Prompt: "P",
		Cwd: t.TempDir(), Identity: map[string]string{"TEAM_WORKER_AGENT_NAME": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if b.Plan.Stdin != "P" || b.Env.Vars["TEAM_WORKER_AGENT_NAME"] != "a" {
		t.Fatalf("generic adapter plan: %+v", b.Plan)
	}
	if _, err := Build(Options{Backend: "docker", Command: []string{"sh"}}); err == nil {
		t.Fatal("unknown backend must fail")
	}
}

// fileAdapter is a test adapter that asks spawn to write one support file.
type fileAdapter struct{ path string }

func (fileAdapter) Name() string                { return "files" }
func (fileAdapter) Source() string              { return "test" }
func (fileAdapter) Match([]string) bool         { return true }
func (fileAdapter) StartupKeys(string) []string { return nil }
func (fileAdapter) Nudge() bool                 { return false }
func (f fileAdapter) Prepare(s agent.Session) agent.Plan {
	return agent.Plan{Args: s.Command, Files: []agent.File{{Path: f.path, Content: []byte("ext")}}}
}

func TestStartWritesAdapterFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run", "pi", "ext.ts")
	b, err := Build(Options{Backend: "subprocess", Command: []string{"sh", "-c", "true"}, Adapter: fileAdapter{path},
		Team: "demo", Agent: "a", Cwd: dir, LogPath: filepath.Join(dir, "a.log")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("Build must not write files; dry runs use it")
	}
	if _, err := b.Start(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "ext" {
		t.Fatalf("support file: %q %v", data, err)
	}
}

func TestBuildPromptOptionalSectionsAndLiteralText(t *testing.T) {
	bare := BuildPrompt(PromptParams{AgentName: "alice", AgentID: "id1", AgentType: "dev", Team: "demo", Leader: "lead"})
	for _, absent := range []string{"- User:", "## Workspace", "## Team Instructions", "## Task\n"} {
		if strings.Contains(bare, absent) {
			t.Errorf("empty field still renders %q", absent)
		}
	}
	if !strings.Contains(bare, "- Leader: lead\n\n## Coordination Protocol\n") {
		t.Errorf("sections are not separated by one blank line:\n%s", bare)
	}
	// Instructions are data: template syntax and markup in them stay literal.
	p := BuildPrompt(PromptParams{AgentName: "alice", Team: "demo", Leader: "lead",
		TeamPrompt: "Use {{.Team}} & <tags>", Task: "Fix {{x}}"})
	if !strings.Contains(p, "Use {{.Team}} & <tags>\n\n## Task\n\nFix {{x}}\n\n## Coordination") {
		t.Errorf("instructions not kept literally:\n%s", p)
	}
}

func TestBuildPromptPicksProtocolByRole(t *testing.T) {
	worker := BuildPrompt(PromptParams{AgentName: "alice", Team: "demo", Leader: "lead"})
	leader := BuildPrompt(PromptParams{AgentName: "lead", Team: "demo", Leader: "lead"})
	if !strings.Contains(worker, "## Worker Loop Protocol") || strings.Contains(worker, "## Leader Protocol") {
		t.Errorf("worker prompt has the wrong protocol:\n%s", worker)
	}
	if !strings.Contains(leader, "## Leader Protocol") || !strings.Contains(leader, "team-worker spawn tmux --team demo") ||
		strings.Contains(leader, "## Worker Loop Protocol") || strings.Contains(leader, "inbox send demo lead") {
		t.Errorf("leader prompt has the wrong protocol:\n%s", leader)
	}
	if !strings.HasSuffix(worker, "keep working until you can stop.\n") || !strings.HasSuffix(leader, "while the workspaces stay.\n") {
		t.Errorf("prompts should end with one newline")
	}
}

func TestBuildPromptAddsPlanApprovalOnlyWhenRequired(t *testing.T) {
	base := PromptParams{AgentName: "alice", Team: "demo", Leader: "lead"}
	if p := BuildPrompt(base); strings.Contains(p, "## Plan approval") {
		t.Errorf("plan approval without --plan-required:\n%s", p)
	}
	base.PlanRequired = true
	p := BuildPrompt(base)
	if !strings.Contains(p, "\n\n## Plan approval\n\n") || !strings.Contains(p, "`team-worker plan submit demo alice <plan text or file>") ||
		!strings.HasSuffix(p, "revise the plan and submit it again.\n") {
		t.Errorf("plan approval section missing or misplaced:\n%s", p)
	}
}
