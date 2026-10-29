package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeMagpie puts a magpie script on PATH that records each call's arguments,
// one call per line with arguments separated by tabs, and returns the log path.
// With dsh true, a fake dsh is installed next to it.
func fakeMagpie(t *testing.T, dsh bool) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\nIFS='\t'\nprintf '%s\\n' \"$*\" >> " + log + "\necho 'secret output'\n"
	if err := os.WriteFile(filepath.Join(bin, "magpie"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if dsh {
		if err := os.WriteFile(filepath.Join(bin, "dsh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	return log
}

func magpieCalls(t *testing.T, log string) [][]string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		calls = append(calls, strings.Split(line, "\t"))
	}
	return calls
}

func providersConfig() map[string]any {
	return map[string]any{
		"providers": []any{map[string]any{
			"id": "relay", "url_env": "TEST_BASE", "key_env": "TEST_KEY", "models": []any{"test-model"},
		}},
		"groups": []any{map[string]any{"id": "coding", "models": []any{"relay/test-model"}, "stays": "off"}},
		"claude": map[string]any{"model": "group/coding"},
	}
}

// setupMagpieHome isolates HOME, XDG_CONFIG_HOME and the pending marker, and
// writes config as providers.yaml (JSON is valid YAML).
func setupMagpieHome(t *testing.T, config map[string]any) (home, path string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	old := pendingPath
	pendingPath = filepath.Join(home, "pending")
	t.Cleanup(func() { pendingPath = old })
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(home, "providers.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return home, path
}

func TestMagpieUpdatesExistingProviderAndPreservesClaudeState(t *testing.T) {
	home, path := setupMagpieHome(t, providersConfig())
	log := fakeMagpie(t, false)
	if err := os.MkdirAll(filepath.Join(home, ".config/magpie"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, ".config/magpie/providers.json"), []byte(`{"providers":[{"id":"relay"}]}`), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"unrelated":"keep"}`), 0o600)
	env := map[string]string{"TEST_BASE": "http://gateway:8000/v1", "TEST_KEY": "secret", "TEAM_WORKER_MODEL": "relay/test-model"}

	model, err := configureMagpie(path, env, runMagpie)
	if err != nil {
		t.Fatal(err)
	}
	if model != "relay/test-model" {
		t.Fatalf("model = %q", model)
	}
	calls := magpieCalls(t, log)
	if !slices.Equal(calls[0][:3], []string{"provider", "set", "relay"}) ||
		!slices.Contains(calls[0], "key=secret") || !slices.Contains(calls[0], "url=http://gateway:8000/v1") {
		t.Fatalf("first call = %v", calls[0])
	}
	if last := calls[len(calls)-1]; !slices.Equal(last, []string{"claude", "relay/test-model"}) {
		t.Fatalf("last call = %v", last)
	}
	var state map[string]any
	data, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	project := state["projects"].(map[string]any)[cwd].(map[string]any)
	if state["unrelated"] != "keep" || state["hasCompletedOnboarding"] != true || project["hasTrustDialogAccepted"] != true {
		t.Fatalf("state = %v", state)
	}
}

func TestMagpieInstalledDshFollowsClaudeModel(t *testing.T) {
	for _, installed := range []bool{true, false} {
		_, path := setupMagpieHome(t, providersConfig())
		log := fakeMagpie(t, installed)
		env := map[string]string{"TEST_BASE": "http://gateway:8000/v1", "TEST_KEY": "secret", "TEAM_WORKER_MODEL": "relay/test-model"}
		if _, err := configureMagpie(path, env, runMagpie); err != nil {
			t.Fatal(err)
		}
		calls := magpieCalls(t, log)
		has := func(want ...string) bool {
			return slices.ContainsFunc(calls, func(c []string) bool { return slices.Equal(c, want) })
		}
		if !has("claude", "relay/test-model") || has("dsh", "relay/test-model") != installed {
			t.Fatalf("dsh installed=%v, calls = %v", installed, calls)
		}
	}
}

func TestMagpieRequiredEmptyKeyWaitsAndFilledKeyResumes(t *testing.T) {
	config := providersConfig()
	config["providers"].([]any)[0].(map[string]any)["key_required"] = true
	_, path := setupMagpieHome(t, config)
	log := fakeMagpie(t, false)
	env := map[string]string{"TEST_BASE": "http://gateway/v1", "TEST_KEY": "", "TEAM_WORKER_MODEL": ""}

	if _, err := configureMagpie(path, env, runMagpie); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(pendingPath); string(data) != "relay" {
		t.Fatalf("pending = %q", data)
	}
	if calls := magpieCalls(t, log); !slices.Contains(calls[0], "key=team-worker-unconfigured") {
		t.Fatalf("first call = %v", calls[0])
	}

	os.Remove(log)
	env["TEST_KEY"] = "filled-key"
	if _, err := configureMagpie(path, env, runMagpie); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatal("pending marker still exists")
	}
	if calls := magpieCalls(t, log); !slices.Contains(calls[0], "key=filled-key") {
		t.Fatalf("first call = %v", calls[0])
	}
}

func TestMagpieValidatesAllEntriesBeforeAnyMutation(t *testing.T) {
	config := providersConfig()
	config["groups"].([]any)[0].(map[string]any)["stays"] = false
	_, path := setupMagpieHome(t, config)
	log := fakeMagpie(t, false)
	env := map[string]string{"TEST_BASE": "http://gateway/v1", "TEST_KEY": "secret"}
	if _, err := configureMagpie(path, env, runMagpie); err == nil {
		t.Fatal("expected a validation error")
	}
	if calls := magpieCalls(t, log); calls != nil {
		t.Fatalf("magpie called: %v", calls)
	}
}

func TestMagpieMissingKeyEnvironmentIsRejected(t *testing.T) {
	_, err := validateProviders(toYAMLValue(t, providersConfig()), map[string]string{"TEST_BASE": "http://gateway/v1"})
	if err == nil || !strings.Contains(err.Error(), "key_env") {
		t.Fatalf("err = %v", err)
	}
}

func TestMagpieDuplicateProviderIDsAreRejected(t *testing.T) {
	config := providersConfig()
	providers := config["providers"].([]any)
	config["providers"] = append(providers, providers[0])
	_, err := validateProviders(toYAMLValue(t, config), map[string]string{"TEST_BASE": "http://gateway/v1", "TEST_KEY": "k"})
	if err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("err = %v", err)
	}
}

func TestMagpieCLIFailureHidesOutput(t *testing.T) {
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "magpie"), []byte("#!/bin/sh\necho 'key=leaked' >&2\nexit 3\n"), 0o755)
	t.Setenv("PATH", bin)
	err := runMagpie("provider", "add", "relay", "key=leaked")
	if err == nil || strings.Contains(err.Error(), "leaked") || !strings.Contains(err.Error(), "exit code 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestMagpieYAMLErrorDoesNotQuoteInput(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "providers.yaml")
	os.WriteFile(path, []byte("providers: [key: \"sk-secret"), 0o600)
	_, err := configureMagpie(path, map[string]string{}, runMagpie)
	if err != errYAML || strings.Contains(err.Error(), "sk-secret") {
		t.Fatalf("err = %v", err)
	}
}

// toYAMLValue round-trips v through a YAML file so tests validate the same
// value types loadYAML produces.
func toYAMLValue(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(path, data, 0o600)
	out, err := loadYAML(path)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
