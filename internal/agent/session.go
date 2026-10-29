package agent

import (
	"crypto/rand"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

func SessionClient(command []string) string {
	if len(command) == 0 {
		return ""
	}
	switch name := strings.ToLower(filepath.Base(command[0])); name {
	case "claude", "claude-code":
		return "claude"
	case "codex", "codex-cli":
		return "codex"
	case "qwen", "qwen-code":
		return "qwen"
	case "gemini", "kimi", "opencode", "pi", "dsh":
		return name
	}
	return ""
}

func sessionOption(command []string, names ...string) string {
	for i, arg := range command {
		for _, name := range names {
			if arg == name && i+1 < len(command) && !strings.HasPrefix(command[i+1], "-") {
				return command[i+1]
			}
			if value, ok := strings.CutPrefix(arg, name+"="); ok {
				return value
			}
		}
	}
	return ""
}

// StartSessionCommand chooses a native id before launch where the CLI supports it.
func StartSessionCommand(command []string) ([]string, string, string, error) {
	args := slices.Clone(command)
	client := SessionClient(command)
	id := sessionOption(command, "--session-id", "--session", "--resume", "-r")
	if client == "codex" {
		for i, arg := range command {
			if arg == "resume" && i+1 < len(command) && !strings.HasPrefix(command[i+1], "-") {
				id = command[i+1]
			}
		}
	}
	if id == "" && (client == "claude" || client == "pi") && !slices.Contains(command, "--continue") && !slices.Contains(command, "-c") {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, client, "", err
		}
		b[6], b[8] = (b[6]&15)|64, (b[8]&63)|128
		id = fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
		args = append(args, "--session-id", id)
	}
	return args, client, id, nil
}

// ResumeCommand never falls back to a directory's latest conversation.
func ResumeCommand(command []string, id string, interactive bool) ([]string, error) {
	client := SessionClient(command)
	if client == "" {
		return nil, fmt.Errorf("CLI does not support session resume")
	}
	if id == "" {
		return nil, fmt.Errorf("no native session id is available")
	}
	args := []string{command[0]}
	start := 1
	if client == "codex" {
		if start < len(command) && (command[start] == "exec" || command[start] == "e") {
			start++
		}
		if start < len(command) && command[start] == "resume" {
			start++
			if start < len(command) && !strings.HasPrefix(command[start], "-") {
				start++
			}
		}
	}
	flags := []string{"--session-id", "--session", "--resume", "-r"}
	if client == "codex" {
		flags = nil
	}
	for i := start; i < len(command); i++ {
		arg := command[i]
		if arg == "--continue" || (arg == "-c" && client != "codex" && client != "dsh") || (arg == "--last" && client == "codex") {
			continue
		}
		matched := false
		for _, flag := range flags {
			if arg == flag {
				if i+1 < len(command) && !strings.HasPrefix(command[i+1], "-") {
					i++
				}
				matched = true
				break
			}
			if strings.HasPrefix(arg, flag+"=") {
				matched = true
				break
			}
		}
		if !matched {
			args = append(args, arg)
		}
	}
	if client == "codex" {
		prefix := []string{command[0]}
		if !interactive {
			prefix = append(prefix, "exec")
		}
		return append(append(prefix, "resume", id), args[1:]...), nil
	}
	flag := "--resume"
	if client == "kimi" || client == "opencode" || client == "pi" {
		flag = "--session"
	}
	return append(args, flag, id), nil
}
