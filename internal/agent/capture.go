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

	"github.com/klauspost/compress/zstd"
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
		valid := firstErr == nil && json.Unmarshal(first, &meta) == nil && meta.Type == "session_meta" && meta.Payload.ID != "" && sameSessionWorkspace(meta.Payload.Cwd, cwd)
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

func sameSessionWorkspace(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	first, err := os.Stat(a)
	if err != nil {
		return false
	}
	second, err := os.Stat(b)
	return err == nil && os.SameFile(first, second)
}

// CaptureDshSession finds the session a dsh agent started in cwd since a
// time. dsh cannot be given the id of a new session; it keeps each session's
// log under <root>/<cwd as a directory name>/<id>/, and the log's first
// record names the session's id, workspace and creation time. Like Codex
// transcripts, a log counts only when it also holds the agent's injected
// identity, and several matches never fall back to the latest.
func CaptureDshSession(root, cwd, agentID string, since time.Time) (string, error) {
	if agentID == "" {
		return "", nil
	}
	dir := filepath.Join(root, DshSessionDir(cwd))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	found := ""
	for _, entry := range entries {
		if !entry.IsDir() || !looksLikeUUID(entry.Name()) {
			continue
		}
		id, err := dshSessionOf(filepath.Join(dir, entry.Name(), "session.v4.jsonl.zstd"), cwd, agentID, since)
		if err != nil {
			return "", err
		}
		if id == "" {
			continue
		}
		if found != "" && found != id {
			return "", fmt.Errorf("multiple dsh sessions match this agent; save an exact session id explicitly")
		}
		found = id
	}
	return found, nil
}

// dshSessionOf returns the id in a dsh session log when the session started
// in cwd since a time and carries the agent's identity, or "".
func dshSessionOf(path, cwd, agentID string, since time.Time) (string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// A log untouched since the agent started belongs to an earlier run.
	if !since.IsZero() && info.ModTime().Before(since) {
		return "", nil
	}
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	dec, err := zstd.NewReader(file)
	if err != nil {
		return "", nil
	}
	defer dec.Close()
	reader := bufio.NewReader(io.LimitReader(dec, 8<<20))
	first, err := reader.ReadBytes('\n')
	var meta struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		Cwd       string `json:"cwd"`
		CreatedAt int64  `json:"createdAt"`
	}
	if err != nil || json.Unmarshal(first, &meta) != nil || meta.Type != "session" || meta.ID == "" ||
		!sameSessionWorkspace(meta.Cwd, cwd) || (!since.IsZero() && time.UnixMilli(meta.CreatedAt).Before(since)) {
		return "", nil
	}
	data, err := io.ReadAll(reader)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		// A log being written can end mid-frame; what was read still counts.
		return "", nil
	}
	needle := "- ID: " + agentID
	if !bytes.Contains(data, []byte(needle+`\n`)) && !bytes.Contains(data, []byte(needle+`"`)) {
		return "", nil
	}
	return meta.ID, nil
}

// DshSessionDir is the directory name dsh gives a workspace's sessions:
// /home/node/work becomes --home-node-work--.
func DshSessionDir(cwd string) string {
	return "-" + strings.ReplaceAll(filepath.Clean(cwd), "/", "-") + "--"
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case !strings.ContainsRune("0123456789abcdef", c):
			return false
		}
	}
	return true
}
