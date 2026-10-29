// Package spawn starts agent CLIs in tmux windows or as background processes.
package spawn

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Prepared is an agent command ready to run.
type Prepared struct {
	Args []string
	// Paste is delivered into the tmux pane once the CLI is ready, for CLIs
	// that cannot take the prompt as an argument.
	Paste string
	// Stdin is fed to a subprocess agent for CLIs without a prompt argument.
	Stdin string
	Env   map[string]string
}

func cliName(command []string) string {
	if len(command) == 0 {
		return ""
	}
	return strings.ToLower(filepath.Base(command[0]))
}

// codexSubcommands run Codex without its interactive TUI.
var codexSubcommands = []string{"exec", "e", "review", "resume", "fork", "cloud", "mcp", "mcp-server",
	"app-server", "completion", "sandbox", "debug", "apply", "login", "logout", "features"}

// Prepare adds permission and prompt arguments for known agent CLIs.
// interactive is true for the tmux backend, where the CLI keeps running in a
// visible terminal; subprocess agents run their prompt once and exit.
func Prepare(command []string, prompt string, skipPermissions, interactive bool) Prepared {
	args := slices.Clone(command)
	p := Prepared{Env: map[string]string{}}
	name := cliName(args)

	if skipPermissions {
		switch name {
		case "claude", "claude-code":
			// Claude Code refuses this flag when running as root.
			if os.Getuid() != 0 {
				args = append(args, "--dangerously-skip-permissions")
			}
		case "codex", "codex-cli":
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		case "gemini", "kimi", "qwen", "qwen-code", "opencode":
			args = append(args, "--yolo")
		case "dsh":
			p.Env["DSH_PERMISSION_MODE"] = "danger-full-access"
		}
	}
	if prompt == "" {
		p.Args = args
		return p
	}

	switch name {
	case "claude", "claude-code":
		if interactive {
			args = append(args, prompt)
		} else {
			args = append(args, "-p", prompt)
		}
	case "codex", "codex-cli":
		hasSub := len(args) > 1 && slices.Contains(codexSubcommands, args[1])
		if !interactive && !hasSub {
			args = slices.Insert(args, 1, "exec")
		}
		args = append(args, prompt)
	case "gemini":
		if interactive {
			args = append(args, "-i", prompt)
		} else {
			args = append(args, "-p", prompt)
		}
	case "kimi":
		args = append(args, "--print", "-p", prompt)
	case "dsh":
		args = append(args, prompt)
	case "pi":
		if interactive {
			args = append(args, prompt)
		} else {
			args = append(args, "-p", prompt)
		}
	default:
		if interactive {
			p.Paste = prompt
		} else {
			p.Stdin = prompt
		}
	}
	p.Args = args
	return p
}
