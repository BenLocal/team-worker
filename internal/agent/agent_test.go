package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinPrepare(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("claude permission flag is dropped for root")
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
		{"codex subcommand kept", []string{"codex", "review"}, false, []string{"codex", "review", "--dangerously-bypass-approvals-and-sandbox", "P"}, false, false},
		{"gemini tmux", []string{"gemini"}, true, []string{"gemini", "--yolo", "-i", "P"}, false, false},
		{"kimi", []string{"kimi"}, true, []string{"kimi", "--yolo", "--print", "-p", "P"}, false, false},
		{"path to binary", []string{"/opt/bin/claude", "--model", "haiku"}, true,
			[]string{"/opt/bin/claude", "--model", "haiku", "--dangerously-skip-permissions", "P"}, false, false},
		{"unknown tmux", []string{"my-agent", "--x"}, true, []string{"my-agent", "--x"}, true, false},
		{"unknown subprocess", []string{"my-agent"}, false, []string{"my-agent"}, false, true},
	}
	r := Builtin()
	for _, c := range cases {
		p := r.For(c.command).Prepare(Session{Command: c.command, Prompt: "P", Interactive: c.interactive, SkipPermissions: true})
		if !slices.Equal(p.Args, c.want) || (p.Paste != "") != c.paste || (p.Stdin != "") != c.stdin {
			t.Errorf("%s: args=%q paste=%q stdin=%q", c.name, p.Args, p.Paste, p.Stdin)
		}
	}

	dsh := r.For([]string{"dsh"}).Prepare(Session{Command: []string{"dsh"}, Prompt: "P", SkipPermissions: true})
	if dsh.Env.Set["DSH_PERMISSION_MODE"] != "danger-full-access" {
		t.Errorf("dsh permission env missing: %v", dsh.Env)
	}
	claude := r.For([]string{"claude"}).Prepare(Session{Command: []string{"claude"}, Prompt: "P", Interactive: true})
	if slices.Contains(claude.Args, "--dangerously-skip-permissions") {
		t.Errorf("skip-permissions off still added the flag: %q", claude.Args)
	}
	if !slices.Contains(claude.Env.Unset, "CLAUDECODE") {
		t.Errorf("claude must drop the nesting markers: %v", claude.Env.Unset)
	}
	if got := r.For([]string{"bash"}).Name(); got != "generic" {
		t.Errorf("fallback adapter: %s", got)
	}
}

func TestBuiltinStartupScreens(t *testing.T) {
	r := Builtin()
	keys := func(cmd, screen string) string { return strings.Join(r.For([]string{cmd}).StartupKeys(screen), "+") }
	if k := keys("claude", "do you trust this folder?\npress enter to continue"); k != "Enter" {
		t.Errorf("claude trust: %q", k)
	}
	if k := keys("claude", "bypass permissions mode\n1. no\n2. yes, i accept"); k != "Down+Enter" {
		t.Errorf("claude permissions: %q", k)
	}
	if k := keys("codex", "do you trust the contents of this directory?\npress enter to continue"); k != "Enter" {
		t.Errorf("codex trust: %q", k)
	}
	newTrust := " quick safety check: is this a project you created or one you trust?\n ❯ no, exit\n   yes, i trust this folder\n enter to confirm · esc to cancel"
	if k := keys("claude", newTrust); k != "Down+Enter" {
		t.Errorf("claude trust prompt with No preselected: %q", k)
	}
	if k := keys("claude", strings.Replace(strings.Replace(newTrust, "❯ no", "  no", 1), "   yes", " ❯ yes", 1)); k != "Enter" {
		t.Errorf("claude trust prompt with Yes selected: %q", k)
	}
	if k := keys("claude", "welcome\n> "); k != "" {
		t.Errorf("plain screen: %q", k)
	}
}

func writeAdapter(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeclarativeAdapter(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "aider.toml", `
match = ["aider"]
skip_permissions = ["--yes-always"]
prompt = "flag:--message"
inherit = false
pass = ["PATH", "HOME"]
[env]
AIDER_MODEL = "${MODEL}"
[[startup]]
all = ["open documentation url"]
keys = ["n", "Enter"]
`)
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := r.For([]string{"aider", "--no-git"})
	if a.Name() != "aider" || !strings.HasSuffix(a.Source(), "aider.toml") {
		t.Fatalf("adapter: %s from %s", a.Name(), a.Source())
	}
	p := a.Prepare(Session{Command: []string{"aider", "--no-git"}, Prompt: "P", Interactive: true, SkipPermissions: true})
	if !slices.Equal(p.Args, []string{"aider", "--no-git", "--yes-always", "--message", "P"}) {
		t.Errorf("args: %q", p.Args)
	}
	if !p.Env.NoInherit || !slices.Equal(p.Env.Pass, []string{"PATH", "HOME"}) || p.Env.Set["AIDER_MODEL"] != "${MODEL}" {
		t.Errorf("env: %+v", p.Env)
	}
	if k := a.StartupKeys("open documentation url for more info? (y/n)"); !slices.Equal(k, []string{"n", "Enter"}) {
		t.Errorf("startup: %q", k)
	}
}

func TestExtendsKeepsBuiltinBehaviour(t *testing.T) {
	dir := t.TempDir()
	writeAdapter(t, dir, "codex-gateway.toml", `
extends = "codex"
unset = ["OPENAI_ORG_ID"]
[env]
OPENAI_BASE_URL = "https://gateway.internal/v1"
`)
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := r.For([]string{"codex"})
	if a.Name() != "codex-gateway" {
		t.Fatalf("user adapter should win over the built-in: %s", a.Name())
	}
	p := a.Prepare(Session{Command: []string{"codex"}, Prompt: "P", SkipPermissions: true})
	if !slices.Equal(p.Args, []string{"codex", "exec", "--dangerously-bypass-approvals-and-sandbox", "P"}) {
		t.Errorf("built-in argument handling lost: %q", p.Args)
	}
	if p.Env.Set["OPENAI_BASE_URL"] == "" || !slices.Contains(p.Env.Unset, "OPENAI_ORG_ID") {
		t.Errorf("env: %+v", p.Env)
	}
	if len(a.StartupKeys("trust the contents of this directory press enter to continue")) == 0 {
		t.Error("inherited startup rules lost")
	}
	// The built-in itself is unchanged.
	if Builtin().For([]string{"codex"}).Prepare(Session{Command: []string{"codex"}}).Env.Set["OPENAI_BASE_URL"] != "" {
		t.Error("extending modified the built-in adapter")
	}
}

func TestInvalidAdapters(t *testing.T) {
	cases := map[string]string{
		"no match":      `prompt = "arg"`,
		"bad prompt":    "match = [\"x\"]\nprompt = \"flagged\"",
		"bad env name":  "match = [\"x\"]\n[env]\n\"BAD-NAME\" = \"1\"",
		"unknown key":   "match = [\"x\"]\npromt = \"arg\"",
		"bad extends":   `extends = "nope"`,
		"empty startup": "match = [\"x\"]\n[[startup]]\nkeys = [\"Enter\"]",
	}
	for name, body := range cases {
		dir := t.TempDir()
		writeAdapter(t, dir, "a.toml", body)
		if _, err := Load(dir); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestClaudeInstallsHooks(t *testing.T) {
	s := Session{Command: []string{"claude"}, Prompt: "P", Interactive: true, Team: "demo", AgentName: "alice",
		WorkerBin: "/opt/tw/team-worker"}
	p := Builtin().For(s.Command).Prepare(s)
	i := slices.Index(p.Args, "--settings")
	if i < 0 || !strings.Contains(p.Args[i+1], "'/opt/tw/team-worker' hook stop --team 'demo' --agent 'alice' --role worker") {
		t.Fatalf("claude hooks missing: %q", p.Args)
	}
	if p.Args[len(p.Args)-1] != "P" {
		t.Fatalf("prompt must stay last: %q", p.Args)
	}

	dir := t.TempDir()
	writeAdapter(t, dir, "plain-claude.toml", "extends = \"claude\"\nhooks = false\n")
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.For(s.Command).Prepare(s).Args, "--settings") {
		t.Fatal("hooks = false should keep the settings out")
	}
	if slices.Contains(Builtin().For([]string{"codex"}).Prepare(Session{Command: []string{"codex"}, Team: "demo",
		AgentName: "a", WorkerBin: "/x"}).Args, "--settings") {
		t.Fatal("codex has no hooks yet")
	}
}

func TestInputPromptVisible(t *testing.T) {
	for _, screen := range []string{"> ", "output\n╭──╮\n│ > type here │\n╰──╯", "codex\n› ", "❯ "} {
		if !InputPromptVisible(screen) {
			t.Errorf("prompt not found in %q", screen)
		}
	}
	for _, screen := range []string{"", "Allow this command?\n  1. Yes\n  2. No", "✻ Working…",
		"Do you want to proceed?\n❯ 1. Yes\n  2. No, and tell Claude what to do differently",
		"Allow command?\n› 1) Yes\n  2) No\n> "} {
		if InputPromptVisible(screen) {
			t.Errorf("false prompt in %q", screen)
		}
	}
}

func TestCodexHooksAreOptIn(t *testing.T) {
	s := Session{Command: []string{"codex"}, Prompt: "P", Interactive: true, Team: "demo", AgentName: "bob",
		WorkerBin: "/opt/tw/team-worker"}
	if args := Builtin().For(s.Command).Prepare(s).Args; slices.Contains(args, "--dangerously-bypass-hook-trust") {
		t.Fatalf("codex hooks must be off by default: %q", args)
	}
	dir := t.TempDir()
	writeAdapter(t, dir, "codex.toml", "extends = \"codex\"\nhooks = true\n")
	r, _ := Load(dir)
	args := r.For(s.Command).Prepare(s).Args
	joined := strings.Join(args, " ")
	if !slices.Contains(args, "--dangerously-bypass-hook-trust") ||
		!strings.Contains(joined, `hooks.Stop=[{hooks=[{type="command",command="'/opt/tw/team-worker' hook stop --team 'demo' --agent 'bob' --role worker",timeout=360}]}]`) ||
		args[len(args)-1] != "P" {
		t.Fatalf("codex hook overrides: %q", args)
	}

	writeAdapter(t, dir, "x.toml", "match = [\"x\"]\nhooks = true\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("hooks = true on an adapter without hook support should fail")
	}
}

func TestPiLoadsTeamWorkerExtension(t *testing.T) {
	state := t.TempDir()
	s := Session{Command: []string{"pi", "--model", "x"}, Prompt: "P", Interactive: true, SkipPermissions: true,
		Team: "demo", AgentName: "carol", WorkerBin: "/opt/tw/team-worker", StateDir: state}
	p := Builtin().For(s.Command).Prepare(s)
	ext := filepath.Join(state, "pi", "team-worker.ts")
	want := []string{"pi", "--model", "x", "--approve", "-e", ext, "P"}
	if !slices.Equal(p.Args, want) {
		t.Fatalf("pi args: %q, want %q", p.Args, want)
	}
	if len(p.Files) != 1 || p.Files[0].Path != ext || !strings.Contains(string(p.Files[0].Content), `runHook("stop"`) {
		t.Fatalf("pi extension file: %+v", p.Files)
	}
	batch := Builtin().For(s.Command).Prepare(Session{Command: []string{"pi"}, Prompt: "P", Team: "demo",
		AgentName: "carol", WorkerBin: "/x", StateDir: state})
	if !slices.Equal(batch.Args, []string{"pi", "-e", ext, "-p", "P"}) {
		t.Fatalf("pi subprocess args: %q", batch.Args)
	}

	dir := t.TempDir()
	writeAdapter(t, dir, "pi.toml", "extends = \"pi\"\nhooks = false\n")
	r, _ := Load(dir)
	if off := r.For(s.Command).Prepare(s); slices.Contains(off.Args, "-e") || len(off.Files) != 0 {
		t.Fatalf("hooks = false should skip the extension: %q %+v", off.Args, off.Files)
	}
}
