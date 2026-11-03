package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Key is a scalar setting of config.json that `config show/get/set` handle.
type Key struct {
	Name string
	// Env overrides the file value when set and not empty.
	Env     string
	Default func() string
	// Valid is the set of allowed values; empty allows any value.
	Valid []string
}

// Keys lists the scalar settings in the order `config show` prints them.
// Remotes, profiles and presets have their own commands.
func Keys() []Key {
	return []Key{
		{Name: "data_dir", Env: envDataDir, Default: func() string { return filepath.Join(homeDir(), ".team-worker") }},
		{Name: "user", Env: "TEAM_WORKER_USER", Default: func() string { return "" }},
		{Name: "server_url", Env: "TEAM_WORKER_SERVER_URL", Default: func() string { return "" }},
		{Name: "workspace", Env: "TEAM_WORKER_WORKSPACE", Default: func() string { return "auto" }, Valid: []string{"auto", "always", "never"}},
		{Name: "default_profile", Env: "TEAM_WORKER_DEFAULT_PROFILE", Default: func() string { return "" }},
	}
}

func lookupKey(name string) (Key, error) {
	var names []string
	for _, k := range Keys() {
		if k.Name == name {
			return k, nil
		}
		names = append(names, k.Name)
	}
	return Key{}, fmt.Errorf("Invalid key '%s'. Valid: %s", name, strings.Join(names, ", "))
}

// Effective returns a setting's value and where it comes from: "env",
// "file" or "default", like ClawTeam's get_effective.
func Effective(name string) (value, source string, err error) {
	k, err := lookupKey(name)
	if err != nil {
		return "", "", err
	}
	if v := os.Getenv(k.Env); v != "" {
		return v, "env", nil
	}
	raw, err := ReadRaw(FilePath())
	if err != nil {
		return "", "", err
	}
	if data, ok := raw[name]; ok {
		var v string
		if err := json.Unmarshal(data, &v); err != nil {
			return "", "", fmt.Errorf("%s: %s: %w", FilePath(), name, err)
		}
		if v != "" {
			return v, "file", nil
		}
	}
	return k.Default(), "default", nil
}

// Set saves a setting to config.json, keeping every other key as it was.
// An empty value removes the key, so the default applies again.
func Set(name, value string) error {
	k, err := lookupKey(name)
	if err != nil {
		return err
	}
	if value != "" && len(k.Valid) > 0 && !slices.Contains(k.Valid, value) {
		return fmt.Errorf("Invalid value '%s' for %s. Valid: %s", value, name, strings.Join(k.Valid, ", "))
	}
	path := FilePath()
	raw, err := ReadRaw(path)
	if err != nil {
		return err
	}
	if value == "" {
		delete(raw, name)
	} else {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		raw[name] = data
	}
	return WriteRaw(path, raw)
}

// ReadRaw reads config.json as raw keys; a missing or empty file is empty.
func ReadRaw(path string) (map[string]json.RawMessage, error) {
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return raw, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "" {
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

// WriteRaw replaces config.json atomically with mode 0600, as it may hold
// the API keys of remotes.
func WriteRaw(path string, raw map[string]json.RawMessage) error {
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
