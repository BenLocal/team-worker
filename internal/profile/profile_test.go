package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestResolveOrder(t *testing.T) {
	t.Setenv(defaultProfileEnv, "")
	one := Config{Profiles: map[string]Profile{"only": {Agent: "claude"}}}
	two := Config{Profiles: map[string]Profile{"a": {Agent: "claude"}, "b": {Agent: "codex"}}}
	cases := []struct {
		name     string
		cfg      Config
		explicit string
		command  bool
		want     string
		wantErr  string
	}{
		{"explicit wins over a command", two, "b", true, "b", ""},
		{"explicit must exist", two, "x", false, "", "Profile 'x' not found"},
		{"explicit command means no profile", one, "", true, "", ""},
		{"only profile", one, "", false, "only", ""},
		{"several without default", two, "", false, "", "Multiple profiles"},
		{"none configured", Config{}, "", false, "", ""},
		{"default profile", Config{Profiles: two.Profiles, DefaultProfile: "a"}, "", false, "a", ""},
		{"missing default", Config{Profiles: two.Profiles, DefaultProfile: "zz"}, "", false, "", "default_profile 'zz'"},
	}
	for _, tc := range cases {
		got, err := Resolve(tc.cfg, tc.explicit, tc.command)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v, want %q", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	t.Setenv(defaultProfileEnv, "b")
	if got, err := Resolve(Config{Profiles: two.Profiles, DefaultProfile: "a"}, "", false); err != nil || got != "b" {
		t.Errorf("env default: got %q, %v", got, err)
	}
}

func TestApplyEnvAndCommand(t *testing.T) {
	p := Profile{Agent: "claude", Model: "sonnet", BaseURL: "https://gw.example/anthropic", APIKeyEnv: "SRC_KEY",
		Env: map[string]string{"STATIC": "1"}, EnvMap: map[string]string{"COPY": "SRC_COPY", "EMPTY": "SRC_EMPTY"},
		Args: []string{"--verbose"}}
	caller := map[string]string{"SRC_KEY": "secret", "SRC_COPY": "x", "SRC_EMPTY": ""}
	got, err := Apply(p, nil, caller)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"claude", "--model", "sonnet", "--verbose"}; !slices.Equal(got.Command, want) {
		t.Errorf("command = %v, want %v", got.Command, want)
	}
	want := map[string]string{"STATIC": "1", "COPY": "${SRC_COPY}", "ANTHROPIC_BASE_URL": "https://gw.example/anthropic",
		"ANTHROPIC_AUTH_TOKEN": "${SRC_KEY}"}
	if len(got.Env) != len(want) {
		t.Fatalf("env = %v, want %v", got.Env, want)
	}
	for k, v := range want {
		if got.Env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, got.Env[k], v)
		}
	}
	for _, v := range got.Env {
		if strings.Contains(v, "secret") {
			t.Fatalf("a secret value reached the layer: %v", got.Env)
		}
	}
}

func TestApplyExplicitValuesWinOverInjectedOnes(t *testing.T) {
	p := Profile{Agent: "codex", BaseURL: "https://injected", APIKeyEnv: "KEY",
		Env: map[string]string{"OPENAI_BASE_URL": "https://explicit"}, EnvMap: map[string]string{"OPENAI_API_KEY": "OTHER"}}
	got, err := Apply(p, nil, map[string]string{"KEY": "k", "OTHER": "o"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Env["OPENAI_BASE_URL"] != "https://explicit" || got.Env["OPENAI_API_KEY"] != "${OTHER}" {
		t.Errorf("env = %v", got.Env)
	}
}

func TestApplyModelFlagRules(t *testing.T) {
	cases := []struct {
		command []string
		want    []string
	}{
		{[]string{"codex"}, []string{"codex", "--model", "m"}},
		{[]string{"/usr/local/bin/Kimi"}, []string{"/usr/local/bin/Kimi", "--model", "m"}},
		{[]string{"claude", "-m", "x"}, []string{"claude", "-m", "x"}},
		{[]string{"claude", "--model", "x"}, []string{"claude", "--model", "x"}},
		{[]string{"aider"}, []string{"aider"}},
	}
	for _, tc := range cases {
		got, err := Apply(Profile{Agent: "claude", Model: "m"}, tc.command, nil)
		if err != nil || !slices.Equal(got.Command, tc.want) {
			t.Errorf("%v: got %v, %v; want %v", tc.command, got.Command, err, tc.want)
		}
	}
	if _, err := Apply(Profile{}, nil, nil); err == nil {
		t.Error("a profile without agent or command must fail")
	}
	got, _ := Apply(Profile{Command: []string{"kimi", "--config-file", "c.toml"}, Agent: "claude"}, nil, nil)
	if got.Agent != "kimi" || got.Command[0] != "kimi" {
		t.Errorf("profile command should win over agent: %+v", got)
	}
}

func TestGenerateFromPreset(t *testing.T) {
	cfg := Config{Profiles: map[string]Profile{}, Presets: map[string]Preset{}}
	name, p, err := Generate(cfg, "gemini-vertex", "gemini", "")
	if err != nil {
		t.Fatal(err)
	}
	if name != "gemini-gemini-vertex" || p.Agent != "gemini" || p.Model != "gemini-2.5-flash" ||
		p.Env["GOOGLE_GENAI_USE_VERTEXAI"] != "true" || p.Description == "" {
		t.Errorf("generated %s = %+v", name, p)
	}
	name, p, err = Generate(cfg, "deepseek", "claude-code", "ds")
	if err != nil || name != "ds" || p.APIKeyEnv != "DEEPSEEK_API_KEY" || p.BaseURL != "https://api.deepseek.com/anthropic" ||
		p.Env["ANTHROPIC_MODEL"] != "DeepSeek-V3.2" {
		t.Errorf("deepseek: %s %+v %v", name, p, err)
	}
	if _, _, err := Generate(cfg, "deepseek", "codex", ""); err == nil {
		t.Error("a missing client override must fail")
	}
	// Editing a generated profile must not change the built-in catalog.
	p.Env["ANTHROPIC_MODEL"] = "changed"
	if Builtin()["deepseek"].ClientOverrides["claude"].Env["ANTHROPIC_MODEL"] != "DeepSeek-V3.2" {
		t.Error("built-in preset was modified")
	}
	if len(Builtin()) != 13 {
		t.Errorf("built-in presets = %d, want 13", len(Builtin()))
	}
}

func TestLocalPresetOverridesBuiltin(t *testing.T) {
	cfg := Config{Presets: map[string]Preset{"deepseek": {Description: "mine"}}}
	l, err := Find(cfg, "deepseek")
	if err != nil || l.Source != SourceLocal || l.Preset.Description != "mine" {
		t.Errorf("find = %+v, %v", l, err)
	}
	if e := Editable(Config{}, "openrouter"); len(e.ClientOverrides) != 3 {
		t.Errorf("editable copy of a built-in = %+v", e)
	}
}

func TestSaveKeepsOtherKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"data_dir":"/d","user":"ann","server_url":"http://x","future":{"a":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Profiles["p"] = Profile{Agent: "claude"}
	cfg.DefaultProfile = "p"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["data_dir"] != "/d" || raw["user"] != "ann" || raw["server_url"] != "http://x" || raw["future"] == nil {
		t.Errorf("unrelated keys lost: %s", data)
	}
	profile := raw["profiles"].(map[string]any)["p"].(map[string]any)
	for _, key := range []string{"description", "agent", "command", "model", "base_url", "base_url_env", "api_key_env",
		"api_key_target_env", "env", "env_map", "args"} {
		if _, ok := profile[key]; !ok {
			t.Errorf("profile JSON misses %q: %v", key, profile)
		}
	}
	again, err := Load(path)
	if err != nil || again.DefaultProfile != "p" || again.Profiles["p"].Agent != "claude" {
		t.Errorf("round trip = %+v, %v", again, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode().Perm())
	}
}
