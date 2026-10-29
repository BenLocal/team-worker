package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CaptureCodexSession matches both workspace and injected agent identity.
// Ambiguous or incomplete transcripts never fall back to the latest file.
func CaptureCodexSession(root, cwd, agentID string, since time.Time) (string, error) {
	if agentID == "" {
		return "", nil
	}
	found := ""
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, fs.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !since.IsZero() && info.ModTime().Before(since) {
			return nil
		}
		file, err := os.Open(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		reader := bufio.NewReader(io.LimitReader(file, 2<<20))
		first, firstErr := reader.ReadBytes('\n')
		var meta struct {
			Type    string                   `json:"type"`
			Payload struct{ ID, Cwd string } `json:"payload"`
		}
		valid := firstErr == nil && json.Unmarshal(first, &meta) == nil && meta.Type == "session_meta" && meta.Payload.ID != "" && filepath.Clean(meta.Payload.Cwd) == filepath.Clean(cwd)
		var data []byte
		if valid {
			data, err = io.ReadAll(reader)
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		needle := "- ID: " + agentID
		if !valid || !(bytes.Contains(data, []byte(needle+`\n`)) || bytes.Contains(data, []byte(needle+`"`))) {
			return nil
		}
		if found != "" && found != meta.Payload.ID {
			return fmt.Errorf("multiple Codex sessions match this agent; save an exact session id explicitly")
		}
		found = meta.Payload.ID
		return nil
	})
	return found, err
}
