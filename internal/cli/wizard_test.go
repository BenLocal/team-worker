package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benlocal/team-worker/internal/profile"
)

// presetsFor lists the built-in presets the wizard offers for a client, in
// menu order.
func presetsFor(client string) []string {
	listed := profile.List(profile.Config{})
	var names []string
	for _, name := range sortedNames(listed) {
		if _, ok := listed[name].Preset.ClientOverrides[client]; ok {
			names = append(names, name)
		}
	}
	return names
}

func TestProfileWizardQuickFromPresetRunsDoctor(t *testing.T) {
	h := newHarness(t)
	providers := presetsFor("claude")
	if len(providers) == 0 {
		t.Skip("no built-in preset for claude")
	}
	want, _, err := profile.Generate(profile.Config{}, providers[0], "claude", "")
	if err != nil {
		t.Fatal(err)
	}
	// quick, claude, first preset, default name, no edits, no advanced,
	// run doctor, no smoke test.
	out, ok := h.runIn("1\nclaude\n1\n\nn\nn\ny\nn\n", "--json", "profile", "wizard")
	if !ok {
		t.Fatalf("wizard failed: %s", out)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil || res["profile"] != want {
		t.Fatalf("%v %s", err, out)
	}
	var got profile.Profile
	h.json(&got, "profile", "show", want)
	if got.Agent != "claude" && profile.Basename(got.Command) != "claude" {
		t.Fatalf("saved profile: %+v", got)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".claude.json"))
	if err != nil || !strings.Contains(string(data), `"hasCompletedOnboarding": true`) {
		t.Fatalf("doctor did not run: %v %s", err, data)
	}
}

func TestProfileWizardAdvancedCustom(t *testing.T) {
	h := newHarness(t)
	// The custom entry follows the codex presets in the menu.
	custom := len(presetsFor("codex")) + 1
	in := strings.Join([]string{
		"2", "codex", fmt.Sprint(custom), "my-codex",
		"Custom box", "gpt-x", "http://llm.local/v1", "MY_KEY",
		"", "codex --profile work", "--search", "A=1", "", "OPENAI_API_KEY=MY_KEY", "",
		"n",
	}, "\n") + "\n"
	if out, ok := h.runIn(in, "profile", "wizard"); !ok {
		t.Fatalf("wizard failed: %s", out)
	}
	var got profile.Profile
	h.json(&got, "profile", "show", "my-codex")
	if got.Agent != "codex" || got.Model != "gpt-x" || got.BaseURL != "http://llm.local/v1" || got.APIKeyEnv != "MY_KEY" ||
		strings.Join(got.Command, " ") != "codex --profile work" || strings.Join(got.Args, " ") != "--search" ||
		got.Env["A"] != "1" || got.EnvMap["OPENAI_API_KEY"] != "MY_KEY" || got.Description != "Custom box" {
		t.Fatalf("%+v", got)
	}
}

func TestProfileWizardCancelsOnEndOfInput(t *testing.T) {
	h := newHarness(t)
	if out, ok := h.runIn("1\nclaude\n", "profile", "wizard"); ok {
		t.Fatalf("wizard succeeded without answers: %s", out)
	}
	var list map[string]profile.Profile
	h.json(&list, "profile", "list")
	if len(list) != 0 {
		t.Fatalf("a cancelled wizard saved %v", list)
	}
}

func TestProfileDoctorCommand(t *testing.T) {
	h := newHarness(t)
	var res profile.DoctorResult
	h.json(&res, "profile", "doctor", "claude")
	if !res.Created || res.Path != filepath.Join(os.Getenv("HOME"), ".claude.json") {
		t.Fatalf("%+v", res)
	}
	if out, ok := h.run("--json", "profile", "doctor", "codex"); ok || !strings.Contains(out, "Unsupported") {
		t.Fatalf("codex accepted: %s", out)
	}
}
