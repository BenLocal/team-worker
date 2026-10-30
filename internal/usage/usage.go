// Package usage reads token counts from the session files agent CLIs keep,
// so team-worker can record usage without asking agents to report it.
package usage

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Tokens is what one session used. Model is the last model seen, if any.
type Tokens struct {
	Provider string
	Model    string
	Input    int64
	Output   int64
}

// maxLine bounds one JSONL record; tool results can be large.
const maxLine = 64 << 20

func scanLines(r io.Reader, fn func([]byte)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		fn(sc.Bytes())
	}
	return sc.Err()
}

// Claude totals a Claude Code project transcript. Claude writes one line per
// content block and repeats the message's usage on each, so usage is counted
// once per message id. Cache reads and writes are input tokens.
func Claude(r io.Reader) (Tokens, error) {
	type use struct {
		Input         int64 `json:"input_tokens"`
		CacheCreation int64 `json:"cache_creation_input_tokens"`
		CacheRead     int64 `json:"cache_read_input_tokens"`
		Output        int64 `json:"output_tokens"`
	}
	t := Tokens{Provider: "anthropic"}
	byMessage := map[string]use{}
	var order []string
	err := scanLines(r, func(line []byte) {
		var rec struct {
			Type    string `json:"type"`
			Message struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage *use   `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Type != "assistant" || rec.Message.Usage == nil || rec.Message.ID == "" {
			return
		}
		if _, seen := byMessage[rec.Message.ID]; !seen {
			order = append(order, rec.Message.ID)
		}
		// Later lines of a message carry the final output count.
		byMessage[rec.Message.ID] = *rec.Message.Usage
		if rec.Message.Model != "" && rec.Message.Model != "<synthetic>" {
			t.Model = rec.Message.Model
		}
	})
	for _, id := range order {
		u := byMessage[id]
		t.Input += u.Input + u.CacheCreation + u.CacheRead
		t.Output += u.Output
	}
	return t, err
}

// Codex reads a Codex rollout. token_count events carry running totals, so
// the last one is the session's usage.
func Codex(r io.Reader) (Tokens, error) {
	t := Tokens{Provider: "openai"}
	err := scanLines(r, func(line []byte) {
		var rec struct {
			Type    string `json:"type"`
			Payload struct {
				Type  string `json:"type"`
				Model string `json:"model"`
				Info  *struct {
					Total struct {
						Input  int64 `json:"input_tokens"`
						Output int64 `json:"output_tokens"`
					} `json:"total_token_usage"`
				} `json:"info"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &rec) != nil {
			return
		}
		switch {
		case rec.Type == "turn_context" && rec.Payload.Model != "":
			t.Model = rec.Payload.Model
		case rec.Type == "event_msg" && rec.Payload.Type == "token_count" && rec.Payload.Info != nil:
			t.Input, t.Output = rec.Payload.Info.Total.Input, rec.Payload.Info.Total.Output
		}
	})
	return t, err
}

// ClaudeFile finds the transcript of a Claude Code session under
// <home>/.claude/projects, whose directory names encode the working directory.
func ClaudeFile(claudeDir, sessionID string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(claudeDir, "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return "", err
	}
	return matches[0], nil
}

// CodexFile finds the rollout of a Codex session under <codex home>/sessions;
// rollout names end with the session id.
func CodexFile(codexDir, sessionID string) (string, error) {
	found := ""
	err := filepath.WalkDir(filepath.Join(codexDir, "sessions"), func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), sessionID+".jsonl") {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}

// Session reads the usage of one native session of client ("claude" or
// "codex"). It reports ok=false when the client is unsupported or the session
// file does not exist (yet).
func Session(client, sessionID string) (t Tokens, ok bool, err error) {
	if sessionID == "" || strings.ContainsAny(sessionID, `/\`) {
		return t, false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return t, false, err
	}
	var path string
	var parse func(io.Reader) (Tokens, error)
	switch client {
	case "claude":
		dir := os.Getenv("CLAUDE_CONFIG_DIR")
		if dir == "" {
			dir = filepath.Join(home, ".claude")
		}
		path, err = ClaudeFile(dir, sessionID)
		parse = Claude
	case "codex":
		dir := os.Getenv("CODEX_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".codex")
		}
		path, err = CodexFile(dir, sessionID)
		parse = Codex
	default:
		return t, false, nil
	}
	if err != nil || path == "" {
		return t, false, err
	}
	f, err := os.Open(path)
	if err != nil {
		return t, false, err
	}
	defer f.Close()
	t, err = parse(f)
	return t, err == nil, err
}
