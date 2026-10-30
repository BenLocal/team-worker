package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestPresetListAndGenerateProfile(t *testing.T) {
	h := newHarness(t)
	var list map[string]struct {
		Source string `json:"source"`
	}
	h.json(&list, "preset", "list")
	if len(list) != 13 || list["deepseek"].Source != "builtin" {
		t.Fatalf("preset list = %v", list)
	}
	var saved map[string]string
	h.json(&saved, "preset", "generate-profile", "deepseek", "claude-code")
	if saved["status"] != "saved" || saved["profile"] != "claude-deepseek" || saved["client"] != "claude" {
		t.Fatalf("generate-profile = %v", saved)
	}
	var shown map[string]any
	h.json(&shown, "profile", "show", "claude-deepseek")
	if shown["api_key_env"] != "DEEPSEEK_API_KEY" || shown["base_url"] != "https://api.deepseek.com/anthropic" {
		t.Fatalf("profile show = %v", shown)
	}
	out, ok := h.run("--json", "preset", "generate-profile", "deepseek", "claude")
	if ok || !strings.Contains(out, `"error"`) || !strings.Contains(out, "--force") {
		t.Fatalf("regenerating without --force: ok=%v %s", ok, out)
	}
	var boot struct {
		Generated []string `json:"generated"`
		Skipped   []string `json:"skipped"`
	}
	h.json(&boot, "preset", "bootstrap", "openrouter")
	if !slices.Equal(boot.Generated, []string{"claude-openrouter", "codex-openrouter", "gemini-openrouter"}) || len(boot.Skipped) != 0 {
		t.Fatalf("bootstrap = %+v", boot)
	}
	h.json(&boot, "preset", "bootstrap", "openrouter", "--client", "codex")
	if len(boot.Generated) != 0 || !slices.Equal(boot.Skipped, []string{"codex-openrouter"}) {
		t.Fatalf("second bootstrap = %+v", boot)
	}
}

func TestPresetEditing(t *testing.T) {
	h := newHarness(t)
	var res map[string]string
	h.json(&res, "preset", "set-client", "gw", "codex-cli", "--model", "m1", "--base-url", "https://gw/v1")
	if res["client"] != "codex" {
		t.Fatalf("set-client = %v", res)
	}
	var shown struct {
		Preset struct {
			ClientOverrides map[string]map[string]any `json:"client_overrides"`
		} `json:"preset"`
		Source string `json:"source"`
	}
	h.json(&shown, "preset", "show", "gw")
	if shown.Source != "local" || shown.Preset.ClientOverrides["codex"]["agent"] != "codex" ||
		shown.Preset.ClientOverrides["codex"]["model"] != "m1" {
		t.Fatalf("preset show = %+v", shown)
	}
	h.json(&res, "preset", "copy", "gw", "gw2")
	if out, ok := h.run("--json", "preset", "copy", "gw", "deepseek"); ok || !strings.Contains(out, "already exists") {
		t.Fatalf("copy onto a built-in name: %s", out)
	}
	h.json(&res, "preset", "remove-client", "gw2", "codex")
	if out, ok := h.run("--json", "preset", "remove", "deepseek"); ok || !strings.Contains(out, `"error"`) {
		t.Fatalf("removing a built-in must fail: %s", out)
	}
	h.json(&res, "preset", "remove", "gw")
}

func TestSpawnProfileLayerOrder(t *testing.T) {
	h := newHarness(t)
	t.Setenv("SRC_KEY", "very-secret-key")
	var res map[string]string
	h.json(&res, "profile", "set", "gw", "--command", "sh -c 'exec cat'", "--base-url", "https://gw.example",
		"--base-url-env", "MY_BASE", "--api-key-env", "SRC_KEY", "--api-key-target-env", "MY_KEY",
		"--env", "STATIC=profile", "--env", "SHARED=profile")
	if res["status"] != "saved" {
		t.Fatalf("profile set = %v", res)
	}
	out, ok := h.run("--json", "spawn", "subprocess", "--team", "demo", "-n", "alice", "--profile", "gw",
		"--env", "SHARED=flag", "--dry-run")
	if !ok {
		t.Fatalf("dry run failed: %s", out)
	}
	if strings.Contains(out, "very-secret-key") {
		t.Fatalf("dry run printed a secret: %s", out)
	}
	var dry spawnResult
	if err := json.Unmarshal([]byte(out), &dry); err != nil {
		t.Fatal(err)
	}
	origin := map[string]string{}
	for _, e := range dry.Env {
		origin[e.Name] = e.Origin
	}
	for name, want := range map[string]string{"MY_BASE": "profile gw", "MY_KEY": "profile gw",
		"STATIC": "profile gw", "SHARED": "--env"} {
		if origin[name] != want {
			t.Errorf("%s from %q, want %q", name, origin[name], want)
		}
	}
	if !slices.Equal(dry.Command, []string{"sh", "-c", "exec cat"}) {
		t.Errorf("command = %v", dry.Command)
	}
}

func TestSpawnImplicitProfileAndExplicitCommand(t *testing.T) {
	h := newHarness(t)
	var res map[string]string
	h.json(&res, "profile", "set", "only", "--command", "sh", "--env", "FROM_PROFILE=1")
	var dry spawnResult
	h.json(&dry, "spawn", "subprocess", "--team", "demo", "-n", "alice", "--dry-run")
	if !slices.Equal(dry.Command, []string{"sh"}) || !hasOrigin(dry, "FROM_PROFILE", "profile only") {
		t.Fatalf("implicit profile not used: %+v", dry)
	}
	h.json(&dry, "spawn", "subprocess", "--team", "demo", "-n", "alice", "--dry-run", "--", "true")
	if !slices.Equal(dry.Command, []string{"true"}) || hasOrigin(dry, "FROM_PROFILE", "profile only") {
		t.Fatalf("an explicit command must skip the implicit profile: %+v", dry)
	}
	h.json(&res, "profile", "set", "second", "--agent", "claude")
	out, ok := h.run("--json", "spawn", "subprocess", "--team", "demo", "-n", "alice", "--dry-run")
	if ok || !strings.Contains(out, "Multiple profiles") {
		t.Fatalf("several profiles without a default: ok=%v %s", ok, out)
	}
}

func TestProfileErrorsAreJSON(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"profile", "show", "missing"},
		{"profile", "remove", "missing"},
		{"profile", "set", "empty", "--model", "x"},
		{"preset", "show", "missing"},
		{"spawn", "subprocess", "--team", "demo", "-n", "a", "--profile", "missing", "--dry-run"},
	} {
		out, ok := h.run(append([]string{"--json"}, args...)...)
		var e map[string]string
		if ok || json.Unmarshal([]byte(out), &e) != nil || e["error"] == "" {
			t.Errorf("%v: ok=%v out=%s", args, ok, out)
		}
	}
}

func TestProfileTestRunsTheCommand(t *testing.T) {
	h := newHarness(t)
	var res map[string]string
	h.json(&res, "profile", "set", "echo", "--command", "sh -c 'echo profile-ok'")
	var out profileTestResult
	h.json(&out, "profile", "test", "echo")
	if out.ReturnCode != 0 || !strings.Contains(out.Stdout, "profile-ok") || out.Agent != "sh" {
		t.Fatalf("profile test = %+v", out)
	}
	h.json(&res, "profile", "set", "fail", "--command", "sh -c 'exit 3'")
	text, ok := h.run("--json", "profile", "test", "fail")
	if ok || !strings.Contains(text, `"returncode": 3`) {
		t.Fatalf("failing profile test: ok=%v %s", ok, text)
	}
}

func TestSplitWords(t *testing.T) {
	cases := map[string][]string{
		`kimi --config-file ~/.kimi/config.toml`: {"kimi", "--config-file", "~/.kimi/config.toml"},
		`sh -c 'echo "a b"'`:                     {"sh", "-c", `echo "a b"`},
		`a "b \"c\"" d\ e`:                       {"a", `b "c"`, "d e"},
		`  `:                                     nil,
	}
	for in, want := range cases {
		got, err := splitWords(in)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("splitWords(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := splitWords(`a 'b`); err == nil {
		t.Error("an unterminated quote must fail")
	}
}

func hasOrigin(res spawnResult, name, origin string) bool {
	for _, e := range res.Env {
		if e.Name == name && e.Origin == origin {
			return true
		}
	}
	return false
}
