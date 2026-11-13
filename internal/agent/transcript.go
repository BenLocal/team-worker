package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TranscriptEvidence reads only the exact saved session's human and assistant
// messages. It rejects ambiguous matches instead of guessing by working directory.
func TranscriptEvidence(root, client, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	if len(id) > 128 || strings.ContainsAny(id, `/\.*?[]`) {
		return "", fmt.Errorf("invalid transcript session id")
	}
	var matches []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), id+".jsonl") {
			matches = append(matches, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", nil
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("ambiguous transcript session")
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return "", err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 1<<20)
	var messages []string
	size := 0
	matched := false
	omitted := false
	for scan.Scan() {
		var rec struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			SessionID string          `json:"sessionId"`
			Message   json.RawMessage `json:"message"`
			Payload   struct {
				ID      string          `json:"id"`
				Role    string          `json:"role"`
				Type    string          `json:"type"`
				Content json.RawMessage `json:"content"`
				Message string          `json:"message"`
			} `json:"payload"`
		}
		if json.Unmarshal(scan.Bytes(), &rec) != nil {
			continue
		}
		if rec.SessionID == id || rec.Type == "session" && rec.ID == id || rec.Type == "session_meta" && rec.Payload.ID == id {
			matched = true
		}
		var role, text string
		switch client {
		case "claude", "pi":
			var msg struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(rec.Message, &msg) == nil {
				role = msg.Role
				text = messageText(msg.Content)
			}
		case "codex":
			if rec.Type == "response_item" {
				role = rec.Payload.Role
				text = messageText(rec.Payload.Content)
			}
			if rec.Type == "event_msg" && rec.Payload.Type == "user_message" {
				role = "user"
				text = rec.Payload.Message
			}
		default:
			return "", fmt.Errorf("unsupported transcript client %q", client)
		}
		if (role != "user" && role != "assistant") || text == "" {
			continue
		}
		entry := role + ": " + text + "\n"
		if len(entry) > 128<<10 {
			entry = entry[len(entry)-(128<<10):]
			omitted = true
		}
		messages = append(messages, entry)
		size += len(entry)
		for size > 256<<10 && len(messages) > 1 {
			size -= len(messages[0])
			messages = messages[1:]
			omitted = true
		}
	}
	if err = scan.Err(); err != nil {
		return "", err
	}
	if !matched {
		return "", fmt.Errorf("transcript did not confirm the saved session identity")
	}
	if len(messages) == 0 {
		return "", nil
	}
	prefix := "Session evidence:\n"
	if omitted {
		prefix += "[Older messages omitted due to the evidence budget.]\n"
	}
	return prefix + strings.Join(messages, ""), nil
}
func messageText(raw json.RawMessage) string {
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Type == "text" || p.Type == "input_text" || p.Type == "output_text" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}
