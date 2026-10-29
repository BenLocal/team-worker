package spawn

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/benlocal/team-worker/internal/tmux"
)

type Options struct {
	Backend         string
	Command         []string
	Team            string
	Agent           string
	Prompt          string
	Cwd             string
	SkipPermissions bool
	IsLeader        bool
	// Env is added to the caller's environment for the agent process.
	Env map[string]string
	// WorkerBin is the team-worker binary the exit hook runs.
	WorkerBin string
	// RunDir holds short-lived env files; LogPath receives subprocess output.
	RunDir  string
	LogPath string
}

type Result struct {
	Backend string   `json:"backend"`
	Target  string   `json:"target"`
	PID     int      `json:"pid"`
	Command []string `json:"command"`

	cli   string
	paste string
}

// FinishStartup answers the agent CLI's startup screens and pastes the
// prompt where needed. Call it after the agent is registered, so an agent
// that exits during startup is already known when its exit hook runs.
func (r Result) FinishStartup() {
	if r.Backend == "tmux" {
		handleStartup(r.Target, r.cli, r.paste, 20*time.Second)
	}
}

// SessionName is the tmux session holding a team's agent windows.
func SessionName(team string) string { return "team-worker-" + team }

func Launch(opts Options) (Result, error) {
	if len(opts.Command) == 0 {
		return Result{}, errors.New("no agent command given")
	}
	interactive := opts.Backend == "tmux"
	prep := Prepare(opts.Command, opts.Prompt, opts.SkipPermissions, interactive)
	env := buildEnv(opts.Env, prep.Env, opts.WorkerBin)
	if _, err := lookPath(prep.Args[0], env["PATH"]); err != nil {
		return Result{}, fmt.Errorf("agent command '%s' not found in PATH; check that it runs on its own first", prep.Args[0])
	}
	switch opts.Backend {
	case "tmux":
		return launchTmux(opts, prep, env)
	case "subprocess":
		return launchSubprocess(opts, prep, env)
	}
	return Result{}, fmt.Errorf("unknown backend '%s': use tmux or subprocess", opts.Backend)
}

// buildEnv copies the caller's environment, adds the agent's variables and
// puts the team-worker binary's directory first on PATH so agents find it.
func buildEnv(extra, adapter map[string]string, workerBin string) map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	// Interactive CLIs such as Codex refuse TERM=dumb, which non-interactive shells pass down.
	if strings.EqualFold(env["TERM"], "dumb") || env["TERM"] == "" {
		env["TERM"] = "xterm-256color"
	}
	// A leader running inside Claude Code must not make workers think they are nested.
	for _, k := range []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION", "TMUX", "TMUX_PANE"} {
		delete(env, k)
	}
	for k, v := range adapter {
		env[k] = v
	}
	for k, v := range extra {
		env[k] = v
	}
	if workerBin != "" {
		dir := filepath.Dir(workerBin)
		env["PATH"] = dir + string(os.PathListSeparator) + env["PATH"]
	}
	return env
}

func lookPath(name, path string) (string, error) {
	if strings.Contains(name, "/") {
		return exec.LookPath(name)
	}
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, name)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// wrapCommand runs the agent, then reports its exit to the daemon.
func wrapCommand(opts Options, args []string) string {
	bin := opts.WorkerBin
	if bin == "" {
		bin = "team-worker"
	}
	hook := fmt.Sprintf("%s lifecycle on-exit --team %s --agent %s >/dev/null 2>&1",
		shellQuote(bin), shellQuote(opts.Team), shellQuote(opts.Agent))
	return fmt.Sprintf("cd %s && %s; __tw_status=$?; %s; exit $__tw_status",
		shellQuote(opts.Cwd), shellJoin(args), hook)
}

var shellNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// writeEnvFile stores the environment in a file the pane sources and then
// deletes; passing it inline would exceed tmux's command-length limit.
func writeEnvFile(dir string, env map[string]string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b := make([]byte, 6)
	rand.Read(b)
	path := filepath.Join(dir, "env-"+hex.EncodeToString(b)+".sh")
	var sb strings.Builder
	for k, v := range env {
		if shellNameRE.MatchString(k) {
			fmt.Fprintf(&sb, "export %s=%s\n", k, shellQuote(v))
		}
	}
	fmt.Fprintf(&sb, "rm -f %s\n", shellQuote(path))
	return path, os.WriteFile(path, []byte(sb.String()), 0o600)
}

func launchTmux(opts Options, prep Prepared, env map[string]string) (Result, error) {
	if !tmux.Available() {
		return Result{}, errors.New("tmux is not installed")
	}
	envFile, err := writeEnvFile(opts.RunDir, env)
	if err != nil {
		return Result{}, err
	}
	shellCmd := ". " + shellQuote(envFile) + "; " + wrapCommand(opts, prep.Args)
	pane, err := tmux.NewWindow(SessionName(opts.Team), opts.Agent, shellCmd)
	if err != nil {
		os.Remove(envFile)
		return Result{}, err
	}
	if opts.IsLeader {
		// Keep the leader's output on screen after it exits so it can be inspected.
		tmux.SetOption(pane, "remain-on-exit", "on")
	}
	_, _, pid := tmux.PaneState(pane)
	return Result{Backend: "tmux", Target: pane, PID: pid, Command: prep.Args,
		cli: cliName(prep.Args), paste: prep.Paste}, nil
}

func launchSubprocess(opts Options, prep Prepared, env map[string]string) (Result, error) {
	if err := os.MkdirAll(filepath.Dir(opts.LogPath), 0o700); err != nil {
		return Result{}, err
	}
	logFile, err := os.OpenFile(opts.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Result{}, err
	}
	defer logFile.Close()
	cmd := exec.Command("sh", "-c", wrapCommand(opts, prep.Args))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if prep.Stdin != "" {
		cmd.Stdin = strings.NewReader(prep.Stdin)
	}
	cmd.Env = make([]string, 0, len(env))
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// A new session makes the agent its own process group, so stopping it
	// reaches every child and closing the CLI's terminal does not kill it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	pid := cmd.Process.Pid
	if prep.Stdin != "" {
		// The stdin copy needs a running goroutine; wait in the background.
		go cmd.Wait()
	} else {
		cmd.Process.Release()
	}
	return Result{Backend: "subprocess", Target: opts.LogPath, PID: pid, Command: prep.Args}, nil
}

// handleStartup answers the confirmation screens agent CLIs show on first
// start in a directory, then pastes the prompt for CLIs that need it. It
// returns once the screen stops changing.
func handleStartup(pane, cli, paste string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	var last string
	stable := 0
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		exists, dead, _ := tmux.PaneState(pane)
		if !exists || dead {
			return
		}
		text, err := tmux.Capture(pane, 0)
		if err != nil {
			return
		}
		lower := strings.ToLower(text)
		switch startupAction(cli, lower) {
		case "enter":
			tmux.SendKeys(pane, "Enter")
			stable, last = 0, ""
			continue
		case "down-enter":
			tmux.SendKeys(pane, "Down")
			time.Sleep(200 * time.Millisecond)
			tmux.SendKeys(pane, "Enter")
			stable, last = 0, ""
			continue
		}
		if strings.TrimSpace(text) != "" && text == last {
			stable++
		} else {
			stable, last = 0, text
		}
		if stable >= 3 || (paste != "" && looksReady(text)) {
			break
		}
	}
	if paste != "" {
		tmux.Paste(pane, paste)
	}
}

// startupAction returns the keys that dismiss a known startup screen.
func startupAction(cli, text string) string {
	switch cli {
	case "claude", "claude-code":
		if strings.Contains(text, "yes, i accept") &&
			(strings.Contains(text, "permission") || strings.Contains(text, "dangerously")) {
			return "down-enter"
		}
		if (strings.Contains(text, "trust this folder") || strings.Contains(text, "trust the contents")) &&
			(strings.Contains(text, "enter to confirm") || strings.Contains(text, "press enter") ||
				strings.Contains(text, "enter to continue")) {
			return "enter"
		}
	case "codex", "codex-cli":
		if strings.Contains(text, "press enter to continue") &&
			(strings.Contains(text, "trust the contents of this directory") ||
				(strings.Contains(text, "update available") && strings.Contains(text, "skip"))) {
			return "enter"
		}
	case "gemini":
		if strings.Contains(text, "trust folder") || strings.Contains(text, "trust parent folder") {
			return "enter"
		}
	}
	return ""
}

func looksReady(text string) bool {
	lines := strings.Split(strings.TrimRight(text, "\n "), "\n")
	start := max(0, len(lines)-10)
	for _, l := range lines[start:] {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "❯") || strings.HasPrefix(l, ">") || strings.HasPrefix(l, "›") {
			return true
		}
	}
	return false
}
