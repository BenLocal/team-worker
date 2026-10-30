// Package profile manages runtime profiles and endpoint presets: named agent
// commands, models, endpoints and credential references kept in the user's
// config file. Field names follow ClawTeam so existing configs carry over.
// Credentials are only ever environment variable names; no secret value is
// stored or copied into an agent's environment layer.
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Profile is one way to start an agent CLI.
type Profile struct {
	Description string   `json:"description"`
	Agent       string   `json:"agent"`
	Command     []string `json:"command"`
	Model       string   `json:"model"`
	BaseURL     string   `json:"base_url"`
	BaseURLEnv  string   `json:"base_url_env"`
	// APIKeyEnv names the caller's variable holding the key; APIKeyTargetEnv
	// names the agent's variable that receives it.
	APIKeyEnv       string            `json:"api_key_env"`
	APIKeyTargetEnv string            `json:"api_key_target_env"`
	Env             map[string]string `json:"env"`
	// EnvMap copies caller variables into the agent: DEST -> SOURCE.
	EnvMap map[string]string `json:"env_map"`
	Args   []string          `json:"args"`
}

// Preset describes an endpoint once and derives profiles for several CLIs.
type Preset struct {
	Description     string             `json:"description"`
	AuthEnv         string             `json:"auth_env"`
	BaseURL         string             `json:"base_url"`
	Env             map[string]string  `json:"env"`
	ClientOverrides map[string]Profile `json:"client_overrides"`
}

// Config is the profile part of ~/.team-worker/config.json.
type Config struct {
	Profiles       map[string]Profile
	Presets        map[string]Preset
	DefaultProfile string
}

const defaultProfileEnv = "TEAM_WORKER_DEFAULT_PROFILE"

// normalized fills nil collections so JSON shows [] and {} like ClawTeam.
func (p Profile) normalized() Profile {
	if p.Command == nil {
		p.Command = []string{}
	}
	if p.Args == nil {
		p.Args = []string{}
	}
	if p.Env == nil {
		p.Env = map[string]string{}
	}
	if p.EnvMap == nil {
		p.EnvMap = map[string]string{}
	}
	return p
}

func (p Preset) normalized() Preset {
	if p.Env == nil {
		p.Env = map[string]string{}
	}
	overrides := make(map[string]Profile, len(p.ClientOverrides))
	for k, v := range p.ClientOverrides {
		overrides[k] = v.normalized()
	}
	p.ClientOverrides = overrides
	return p
}

// Clone deep-copies a profile so edits never reach a shared built-in.
func (p Profile) Clone() Profile {
	p.Command = slices.Clone(p.Command)
	p.Args = slices.Clone(p.Args)
	p.Env = maps.Clone(p.Env)
	p.EnvMap = maps.Clone(p.EnvMap)
	return p.normalized()
}

func (p Preset) Clone() Preset {
	c := p
	c.Env = maps.Clone(p.Env)
	c.ClientOverrides = map[string]Profile{}
	for k, v := range p.ClientOverrides {
		c.ClientOverrides[k] = v.Clone()
	}
	return c.normalized()
}

// Load reads the profile keys of the config file; a missing file is empty.
func Load(path string) (Config, error) {
	raw, err := readRaw(path)
	if err != nil {
		return Config{}, err
	}
	c := Config{Profiles: map[string]Profile{}, Presets: map[string]Preset{}}
	if v, ok := raw["profiles"]; ok {
		if err := json.Unmarshal(v, &c.Profiles); err != nil {
			return Config{}, fmt.Errorf("%s: profiles: %w", path, err)
		}
	}
	if v, ok := raw["presets"]; ok {
		if err := json.Unmarshal(v, &c.Presets); err != nil {
			return Config{}, fmt.Errorf("%s: presets: %w", path, err)
		}
	}
	if v, ok := raw["default_profile"]; ok {
		if err := json.Unmarshal(v, &c.DefaultProfile); err != nil {
			return Config{}, fmt.Errorf("%s: default_profile: %w", path, err)
		}
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	if c.Presets == nil {
		c.Presets = map[string]Preset{}
	}
	for k, v := range c.Profiles {
		c.Profiles[k] = v.normalized()
	}
	for k, v := range c.Presets {
		c.Presets[k] = v.normalized()
	}
	return c, nil
}

// Save writes the profile keys back atomically, keeping every other key of
// the config file (data_dir, user, server_url, …) as it was.
func Save(path string, c Config) error {
	raw, err := readRaw(path)
	if err != nil {
		return err
	}
	set := func(key string, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		raw[key] = data
		return nil
	}
	profiles := map[string]Profile{}
	for k, v := range c.Profiles {
		profiles[k] = v.normalized()
	}
	presets := map[string]Preset{}
	for k, v := range c.Presets {
		presets[k] = v.normalized()
	}
	if err := set("profiles", profiles); err != nil {
		return err
	}
	if err := set("presets", presets); err != nil {
		return err
	}
	if err := set("default_profile", c.DefaultProfile); err != nil {
		return err
	}
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readRaw(path string) (map[string]json.RawMessage, error) {
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return raw, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return raw, nil
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object: %w", path, err)
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	return raw, nil
}

// Resolve picks the profile a spawn uses: an explicit name; none when an
// explicit command is given; the default profile; the only profile; or none
// when no profile is configured. Several profiles without a default is an error.
func Resolve(c Config, explicit string, hasCommand bool) (string, error) {
	if explicit != "" {
		if _, ok := c.Profiles[explicit]; !ok {
			return "", fmt.Errorf("Profile '%s' not found", explicit)
		}
		return explicit, nil
	}
	if hasCommand {
		return "", nil
	}
	def := c.DefaultProfile
	if v := os.Getenv(defaultProfileEnv); v != "" {
		def = v
	}
	if def != "" {
		if _, ok := c.Profiles[def]; !ok {
			return "", fmt.Errorf("Configured default_profile '%s' was not found", def)
		}
		return def, nil
	}
	switch len(c.Profiles) {
	case 0:
		return "", nil
	case 1:
		for name := range c.Profiles {
			return name, nil
		}
	}
	return "", errors.New("Multiple profiles are configured. Set `default_profile`, pass `--profile`, " +
		"or provide an explicit command.")
}

// Applied is a profile resolved against a command and the caller's environment.
type Applied struct {
	Command []string
	// Agent is the lower-case basename of Command[0].
	Agent string
	// Env is the profile's environment layer. Credentials appear as
	// ${SOURCE} references that spawn expands from the caller's environment.
	Env map[string]string
}

// Apply resolves a profile like ClawTeam's apply_profile: the command comes
// from the explicit command, then the profile's command, then its agent;
// env and env_map overwrite, while the endpoint and API key only fill
// variables the profile did not set; --model and args are appended.
func Apply(p Profile, command []string, caller map[string]string) (Applied, error) {
	cmd := slices.Clone(command)
	if len(cmd) == 0 {
		if len(p.Command) > 0 {
			cmd = slices.Clone(p.Command)
		} else if p.Agent != "" {
			cmd = []string{p.Agent}
		}
	}
	if len(cmd) == 0 {
		return Applied{}, errors.New("Profile does not define an agent command")
	}
	agent := Basename(cmd)
	env := maps.Clone(p.Env)
	if env == nil {
		env = map[string]string{}
	}
	for _, dest := range sortedKeys(p.EnvMap) {
		if source := p.EnvMap[dest]; caller[source] != "" {
			env[dest] = "${" + source + "}"
		}
	}
	if p.BaseURL != "" {
		key := p.BaseURLEnv
		if key == "" {
			key = baseURLEnv(agent)
		}
		if _, set := env[key]; key != "" && !set {
			env[key] = p.BaseURL
		}
	}
	if p.APIKeyEnv != "" && caller[p.APIKeyEnv] != "" {
		key := p.APIKeyTargetEnv
		if key == "" {
			key = apiKeyEnv(agent)
		}
		if _, set := env[key]; key != "" && !set {
			env[key] = "${" + p.APIKeyEnv + "}"
		}
	}
	if p.Model != "" && !slices.Contains(cmd, "--model") && !slices.Contains(cmd, "-m") && modelFlagAgents[agent] {
		cmd = append(cmd, "--model", p.Model)
	}
	cmd = append(cmd, p.Args...)
	return Applied{Command: cmd, Agent: agent, Env: env}, nil
}

// Basename is the lower-case executable name of a command.
func Basename(command []string) string {
	if len(command) == 0 {
		return ""
	}
	return strings.ToLower(filepath.Base(command[0]))
}

var modelFlagAgents = map[string]bool{
	"claude": true, "claude-code": true, "codex": true, "codex-cli": true, "gemini": true, "kimi": true, "pi": true,
}

func baseURLEnv(agent string) string {
	switch agent {
	case "claude", "claude-code":
		return "ANTHROPIC_BASE_URL"
	case "codex", "codex-cli":
		return "OPENAI_BASE_URL"
	case "gemini":
		return "GOOGLE_GEMINI_BASE_URL"
	case "kimi":
		return "KIMI_BASE_URL"
	}
	return ""
}

func apiKeyEnv(agent string) string {
	switch agent {
	case "claude", "claude-code":
		return "ANTHROPIC_AUTH_TOKEN"
	case "codex", "codex-cli":
		return "OPENAI_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "kimi":
		return "KIMI_API_KEY"
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// CallerEnv turns os.Environ-style pairs into a map.
func CallerEnv(pairs []string) map[string]string {
	out := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}
