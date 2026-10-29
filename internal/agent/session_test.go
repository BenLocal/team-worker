package agent

import (
	"reflect"
	"testing"
)

func TestResumeUsesExactSessionAndKeepsModel(t *testing.T) {
	for _, tt := range []struct {
		command, want []string
		interactive   bool
	}{
		{[]string{"claude", "--model", "chosen", "--session-id", "old"}, []string{"claude", "--model", "chosen", "--resume", "exact"}, true},
		{[]string{"codex", "--model", "chosen"}, []string{"codex", "resume", "exact", "--model", "chosen"}, true},
		{[]string{"codex", "--model", "chosen"}, []string{"codex", "exec", "resume", "exact", "--model", "chosen"}, false},
		{[]string{"codex", "-c", "model=exec"}, []string{"codex", "resume", "exact", "-c", "model=exec"}, true},
		{[]string{"gemini"}, []string{"gemini", "--resume", "exact"}, true},
		{[]string{"dsh", "--profile", "headless", "--json"}, []string{"dsh", "--profile", "headless", "--json", "--session-id", "exact"}, false},
		{[]string{"dsh", "headless", "--session-id", "old"}, []string{"dsh", "headless", "--session-id", "exact"}, false},
		{[]string{"dsh", "--profile=headless", "--session-id=old", "--patch", "local.yml"}, []string{"dsh", "--profile=headless", "--patch", "local.yml", "--session-id", "exact"}, false},
		{[]string{"dsh", "--profile", "headless"}, []string{"dsh", "--profile", "headless", "--session-id", "exact"}, true},
		{[]string{"dsh", "--profile", "tui"}, []string{"dsh", "--profile", "tui", "--resume", "exact"}, true},
		{[]string{"dsh"}, []string{"dsh", "--resume", "exact"}, true},
	} {
		got, err := ResumeCommand(tt.command, "exact", tt.interactive)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%v: %v %v", tt.command, got, err)
		}
	}
	if _, err := ResumeCommand([]string{"custom"}, "exact", true); err == nil {
		t.Fatal("unsupported CLI silently started a new session")
	}
	if _, err := ResumeCommand([]string{"claude"}, "", true); err == nil {
		t.Fatal("missing session silently resumed latest")
	}
}

func TestClaudeSessionsAreUniqueBeforeLaunch(t *testing.T) {
	a, client, first, err := StartSessionCommand([]string{"claude"})
	if err != nil || client != "claude" || len(first) != 36 || len(a) != 3 {
		t.Fatalf("%v %s %s %v", a, client, first, err)
	}
	_, _, second, err := StartSessionCommand([]string{"claude"})
	if err != nil || first == second {
		t.Fatalf("sessions collide: %s %s %v", first, second, err)
	}
}
