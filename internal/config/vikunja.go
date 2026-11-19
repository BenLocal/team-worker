package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const vikunjaHooksFile = "vikunja-hooks.json"
const maxVikunjaHooksSize = 64 << 10

var ErrInvalidVikunjaHooks = errors.New("Invalid Vikunja hook settings")

type VikunjaHook struct {
	Secret string   `json:"secret"`
	Events []string `json:"events"`
}

func (h VikunjaHook) AllowsEvent(name string) bool {
	if h.Events == nil {
		return true
	}
	for _, pattern := range h.Events {
		if prefix, wildcard := strings.CutSuffix(pattern, "*"); wildcard {
			if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
				return true
			}
		} else if name == pattern {
			return true
		}
	}
	return false
}

// ReadVikunjaHooks supports both the original secret strings and settings objects.
func ReadVikunjaHooks(dataDir string) (map[string]VikunjaHook, error) {
	file, err := os.Open(filepath.Join(dataDir, vikunjaHooksFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("Vikunja secrets must be in a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxVikunjaHooksSize+1))
	if err != nil {
		return nil, err
	}
	var entries map[string]json.RawMessage
	if len(data) > maxVikunjaHooksSize || json.Unmarshal(data, &entries) != nil || entries == nil {
		return nil, errors.New("Invalid Vikunja secrets file")
	}
	hooks := make(map[string]VikunjaHook, len(entries))
	for team, entry := range entries {
		var hook VikunjaHook
		if entry[0] == '"' {
			err = json.Unmarshal(entry, &hook.Secret)
		} else {
			decoder := json.NewDecoder(strings.NewReader(string(entry)))
			decoder.DisallowUnknownFields()
			err = decoder.Decode(&hook)
		}
		if err != nil {
			return nil, errors.New("Invalid Vikunja team settings")
		}
		hooks[team] = hook
	}
	if err := validateVikunjaHooks(hooks); err != nil {
		return nil, err
	}
	return hooks, nil
}

func validateVikunjaHooks(hooks map[string]VikunjaHook) error {
	seen := make(map[string]bool, len(hooks))
	for team, hook := range hooks {
		if team == "" || hook.Secret == "" || seen[hook.Secret] {
			return errors.New("Each Vikunja team must have a distinct, nonempty signing secret")
		}
		seen[hook.Secret] = true
		for _, event := range hook.Events {
			name, _ := strings.CutSuffix(event, ".*")
			if name == "" || strings.ContainsAny(name, "* \t\r\n") {
				return errors.New("Use an event name or a prefix ending in .*")
			}
		}
	}
	return nil
}

func SaveVikunjaHooks(dataDir string, hooks map[string]VikunjaHook) error {
	if err := validateVikunjaHooks(hooks); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidVikunjaHooks, err)
	}
	data, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxVikunjaHooksSize {
		return fmt.Errorf("%w: settings are too large", ErrInvalidVikunjaHooks)
	}
	file, err := os.CreateTemp(dataDir, ".vikunja-hooks-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dataDir, vikunjaHooksFile))
}
