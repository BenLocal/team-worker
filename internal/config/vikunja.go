package config

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// ReadVikunjaHooks loads a map of team names to webhook signing secrets.
func ReadVikunjaHooks(dataDir string) (map[string]string, error) {
	file, err := os.Open(filepath.Join(dataDir, "vikunja-hooks.json"))
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
	const maxFileSize = 64 << 10
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("Vikunja secrets must be in a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	var secrets map[string]string
	if len(data) > maxFileSize || json.Unmarshal(data, &secrets) != nil || secrets == nil {
		return nil, errors.New("Invalid Vikunja secrets file")
	}
	seen := make(map[string]bool, len(secrets))
	for team, secret := range secrets {
		if team == "" || secret == "" || seen[secret] {
			return nil, errors.New("Each Vikunja team must have a distinct, nonempty signing secret")
		}
		seen[secret] = true
	}
	return secrets, nil
}
