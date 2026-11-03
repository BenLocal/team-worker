package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DoctorResult reports what `profile doctor` changed; field names follow
// ClawTeam.
type DoctorResult struct {
	Client                 string `json:"client"`
	Path                   string `json:"path"`
	Created                bool   `json:"created"`
	HasCompletedOnboarding bool   `json:"hasCompletedOnboarding"`
}

// Doctor repairs a client's local state so profiles that point it at
// another endpoint start without its first-run screens. Only Claude Code
// is supported: it marks onboarding complete in ~/.claude.json under home,
// keeping every other key of that file.
func Doctor(home, client string) (DoctorResult, error) {
	switch strings.ToLower(strings.TrimSpace(client)) {
	case "claude", "claude-code":
	default:
		return DoctorResult{}, fmt.Errorf("Unsupported profile doctor target '%s'. Supported: claude", client)
	}
	path := filepath.Join(home, ".claude.json")
	res := DoctorResult{Client: "claude", Path: path, HasCompletedOnboarding: true}
	state := map[string]json.RawMessage{}
	mode := os.FileMode(0o600)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		res.Created = true
	case err != nil:
		return DoctorResult{}, err
	case strings.TrimSpace(string(data)) != "":
		// Unlike ClawTeam, never replace a file we cannot parse: it holds
		// the user's Claude Code settings and project history.
		if err := json.Unmarshal(data, &state); err != nil || state == nil {
			return DoctorResult{}, fmt.Errorf("%s is not a JSON object; fix or move it first: %v", path, err)
		}
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
	}
	state["hasCompletedOnboarding"] = json.RawMessage("true")
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return DoctorResult{}, err
	}
	// Write through a symlinked ~/.claude.json instead of replacing the link.
	target := path
	if t, err := filepath.EvalSymlinks(path); err == nil {
		target = t
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".claude-*.json")
	if err != nil {
		return DoctorResult{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		tmp.Close()
		return DoctorResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return DoctorResult{}, err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return DoctorResult{}, err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return DoctorResult{}, err
	}
	return res, nil
}
