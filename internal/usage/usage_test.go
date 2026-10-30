package usage

import (
	"os"
	"path/filepath"
	"testing"
)

func open(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestClaudeCountsEachMessageOnceWithCacheAsInput(t *testing.T) {
	got, err := Claude(open(t, "claude.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// msg_1 counted once with its final output; cache tokens are input.
	want := Tokens{Provider: "anthropic", Model: "claude-sonnet-4-5", Input: 1110 + 203, Output: 40 + 7}
	if got != want {
		t.Fatalf("Claude = %+v, want %+v", got, want)
	}
}

func TestCodexTakesTheLastRunningTotal(t *testing.T) {
	got, err := Codex(open(t, "codex.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := Tokens{Provider: "openai", Model: "gpt-5.4", Input: 2500, Output: 210}
	if got != want {
		t.Fatalf("Codex = %+v, want %+v", got, want)
	}
}

func TestSessionFindsFilesByID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	write := func(path, src string) {
		data, err := os.ReadFile(filepath.Join("testdata", src))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, ".claude/projects/-work-repo/1111-2222.jsonl"), "claude.jsonl")
	write(filepath.Join(home, ".codex/sessions/2026/10/30/rollout-2026-10-30T09-00-00-0199-abc.jsonl"), "codex.jsonl")

	if got, ok, err := Session("claude", "1111-2222"); err != nil || !ok || got.Output != 47 {
		t.Fatalf("claude session = %+v %v %v", got, ok, err)
	}
	if got, ok, err := Session("codex", "0199-abc"); err != nil || !ok || got.Input != 2500 {
		t.Fatalf("codex session = %+v %v %v", got, ok, err)
	}
	for _, tc := range []struct{ client, id string }{{"claude", "missing"}, {"dsh", "x"}, {"claude", "../x"}, {"codex", ""}} {
		if _, ok, err := Session(tc.client, tc.id); ok || err != nil {
			t.Fatalf("Session(%q, %q) ok=%v err=%v", tc.client, tc.id, ok, err)
		}
	}
}
