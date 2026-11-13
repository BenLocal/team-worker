package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/agent"
	"github.com/benlocal/team-worker/internal/agent/builtin"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/spawn"
	"github.com/benlocal/team-worker/internal/sysproc"
)

// limitedOutput bounds process output even when a CLI misbehaves.
type limitedOutput struct {
	data     []byte
	limit    int
	overflow bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	left := b.limit - len(b.data)
	if len(p) > left {
		b.overflow = true
		p = p[:left]
	}
	b.data = append(b.data, p...)
	return n, nil
}

// Reflect runs a fresh batch session in a disposable directory. Credentials
// and models come from the existing profile/adapter configuration.
func Reflect(ctx context.Context, cfg model.ReflectionConfig, prompt string) (string, error) {
	req := Request{Command: cfg.Command}
	if err := ApplyProfile(&req, cfg.Profile); err != nil {
		return "", err
	}
	// Saved commands must not resume or mutate a previous conversation.
	for _, arg := range req.Command[1:] {
		for _, flag := range []string{"--session-id", "--session", "--resume", "--continue", "--last", "--fork-session"} {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return "", fmt.Errorf("reflection requires a fresh command; configure its command or profile")
			}
		}
		if arg == "resume" || arg == "fork" || arg == "-r" {
			return "", fmt.Errorf("reflection cannot resume a conversation")
		}
	}
	client := agent.SessionClient(req.Command)
	if client == "pi" {
		req.Command = append(req.Command, "--no-tools", "--no-session", "--no-extensions", "--no-mcp", "--no-skills", "--no-context-files")
	}
	if client == "claude" {
		req.Command = append(req.Command, "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
	}
	if client == "codex" {
		req.Command = slices.DeleteFunc(req.Command, func(arg string) bool { return arg == "--dangerously-bypass-approvals-and-sandbox" })
		if !slices.Contains(req.Command, "--no-daemon") {
			req.Command = append(req.Command, "--no-daemon")
		}
		req.Command = append(req.Command, "--sandbox", "read-only", "--skip-git-repo-check", "--ephemeral")
	}
	registry, err := builtin.Load(config.AgentsDir())
	if err != nil {
		return "", err
	}
	adapter := registry.For(req.Command)
	plan := adapter.Prepare(agent.Session{Command: req.Command, Prompt: prompt})
	// Pass large evidence through stdin to avoid the OS per-argument limit.
	if client == "claude" || client == "pi" || client == "codex" {
		for i := len(plan.Args) - 1; i >= 0; i-- {
			if plan.Args[i] == prompt {
				if client == "codex" {
					plan.Args[i] = "-"
				} else {
					plan.Args = append(plan.Args[:i], plan.Args[i+1:]...)
				}
				plan.Stdin = prompt
				break
			}
		}
	}
	if plan.Paste != "" || len(plan.Args) == 0 {
		return "", fmt.Errorf("reflection requires a batch-capable adapter")
	}
	// Reflection never receives a member's coordination identity or hooks.
	caller := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "TEAM_WORKER_") {
			caller = append(caller, v)
		}
	}
	env, err := spawn.ComposeEnv(spawn.EnvSpec{Caller: caller, Adapter: plan.Env, AdapterName: adapter.Name(), Layers: req.EnvLayers})
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "team-worker-reflection-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, plan.Args[0], plan.Args[1:]...)
	cmd.Dir = dir
	cmd.SysProcAttr = sysproc.Detached()
	cmd.Cancel = func() error { return sysproc.TerminateGroup(cmd.Process.Pid) }
	cmd.WaitDelay = 2 * time.Second
	for k, v := range env.Vars {
		if !strings.HasPrefix(k, "TEAM_WORKER_") {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	if plan.Stdin != "" {
		cmd.Stdin = strings.NewReader(plan.Stdin)
	}
	stdout := &limitedOutput{limit: 1 << 20}
	stderr := &limitedOutput{limit: 16 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("reflection command: %w: %s", err, stderr.data)
	}
	if stdout.overflow {
		return "", fmt.Errorf("reflection output exceeds 1 MiB")
	}
	return string(stdout.data), nil
}
