package spawn

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/benlocal/team-worker/internal/agent"
	"github.com/benlocal/team-worker/internal/agent/builtin"
	"github.com/benlocal/team-worker/internal/sysproc"
	"github.com/benlocal/team-worker/internal/tmux"
)

type Options struct {
	RunID           string
	KeepAlive       bool
	StartupPaste    string
	Backend         string
	Command         []string
	Adapter         agent.Agent
	Team            string
	Agent           string
	Prompt          string
	Cwd             string
	SkipPermissions bool
	IsLeader        bool
	// EnvLayers apply after the adapter's environment; Identity comes last.
	EnvLayers []EnvLayer
	Identity  map[string]string
	// WorkerBin is the team-worker binary the exit hook runs.
	WorkerBin string
	// RunDir holds short-lived env files; LogPath receives subprocess output.
	RunDir  string
	LogPath string
}

// Built is a prepared agent start: the final command and environment.
type Built struct {
	opts Options
	Plan agent.Plan
	Env  Env
}

type Result struct {
	Backend string   `json:"backend"`
	Target  string   `json:"target"`
	PID     int      `json:"pid"`
	Command []string `json:"command"`

	adapter agent.Agent
	paste   string
}

// FinishStartup answers the agent CLI's startup screens and pastes the
// prompt where needed. Call it after the agent is registered, so an agent
// that exits during startup is already known when its exit hook runs.
func (r Result) FinishStartup() {
	if r.Backend == "tmux" {
		handleStartup(context.Background(), r.Target, r.adapter, r.paste, 20*time.Second)
	}
}

// SessionName is the tmux session holding a team's agent windows.
func SessionName(team string) string { return "team-worker-" + team }

// Build prepares the command and environment without starting anything.
func Build(opts Options) (Built, error) {
	if len(opts.Command) == 0 {
		return Built{}, errors.New("no agent command given")
	}
	if opts.Backend != "tmux" && opts.Backend != "subprocess" {
		return Built{}, fmt.Errorf("unknown backend '%s': use tmux or subprocess", opts.Backend)
	}
	if opts.Adapter == nil {
		opts.Adapter = builtin.Registry().For(opts.Command)
	}
	plan := opts.Adapter.Prepare(agent.Session{Command: opts.Command, Prompt: opts.Prompt,
		Interactive: opts.Backend == "tmux", SkipPermissions: opts.SkipPermissions,
		Team: opts.Team, AgentName: opts.Agent, Leader: opts.IsLeader, WorkerBin: opts.WorkerBin,
		StateDir: opts.RunDir})
	env, err := ComposeEnv(EnvSpec{
		Caller: os.Environ(), Adapter: plan.Env,
		AdapterName: opts.Adapter.Name() + " (" + opts.Adapter.Source() + ")",
		Layers:      opts.EnvLayers, Identity: opts.Identity, WorkerBin: opts.WorkerBin,
	})
	if err != nil {
		return Built{}, err
	}
	if _, err := lookPath(plan.Args[0], env.Vars["PATH"]); err != nil {
		return Built{}, fmt.Errorf("agent command '%s' not found in PATH; check that it runs on its own first", plan.Args[0])
	}
	opts.StartupPaste = plan.Paste
	return Built{opts: opts, Plan: plan, Env: env}, nil
}

// Start writes the adapter's support files and runs the agent in its backend.
func (b Built) Start() (Result, error) {
	for _, f := range b.Plan.Files {
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(f.Path, f.Content, 0o600); err != nil {
			return Result{}, err
		}
	}
	if b.opts.Backend == "tmux" {
		return b.startTmux()
	}
	return b.startSubprocess()
}

// Launch builds and starts an agent.
func Launch(opts Options) (Result, error) {
	b, err := Build(opts)
	if err != nil {
		return Result{}, err
	}
	return b.Start()
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
	if opts.KeepAlive {
		command := []string{bin, "lifecycle", "supervise", "--team", opts.Team, "--agent", opts.Agent, "--run-id", opts.RunID}
		if opts.StartupPaste != "" {
			command = append(command, "--paste", opts.StartupPaste)
		}
		command = append(append(command, "--"), args...)
		return fmt.Sprintf("cd %s && exec %s", shellQuote(opts.Cwd), shellJoin(command))
	}
	hook := fmt.Sprintf("%s lifecycle on-exit --team %s --agent %s >/dev/null 2>&1",
		shellQuote(bin), shellQuote(opts.Team), shellQuote(opts.Agent))
	if opts.RunID != "" {
		hook += " --run-id " + shellQuote(opts.RunID) + ` --code "$__tw_status"`
	}
	return fmt.Sprintf("cd %s && %s; __tw_status=$?; %s; exit $__tw_status",
		shellQuote(opts.Cwd), shellJoin(args), hook)
}

var shellNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// writeEnvFile stores the environment in a file the pane sources and then
// deletes; passing it inline would exceed tmux's command-length limit. The
// file sets exactly these variables, so ones the adapter dropped stay unset.
func writeEnvFile(dir string, env map[string]string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b := make([]byte, 6)
	rand.Read(b)
	path := filepath.Join(dir, "env-"+hex.EncodeToString(b)+".sh")
	var sb strings.Builder
	// tmux starts the pane with its server's environment; clear it first.
	sb.WriteString("for __tw_v in $(env | sed -n 's/^\\([A-Za-z_][A-Za-z0-9_]*\\)=.*/\\1/p'); do unset \"$__tw_v\" 2>/dev/null; done; unset __tw_v\n")
	for k, v := range env {
		if shellNameRE.MatchString(k) {
			fmt.Fprintf(&sb, "export %s=%s\n", k, shellQuote(v))
		}
	}
	fmt.Fprintf(&sb, "rm -f %s\n", shellQuote(path))
	return path, os.WriteFile(path, []byte(sb.String()), 0o600)
}

func (b Built) startTmux() (Result, error) {
	if !tmux.Available() {
		return Result{}, errors.New("tmux is not installed")
	}
	envFile, err := writeEnvFile(b.opts.RunDir, b.Env.Vars)
	if err != nil {
		return Result{}, err
	}
	shellCmd := ". " + shellQuote(envFile) + "; " + wrapCommand(b.opts, b.Plan.Args)
	pane, err := tmux.NewWindow(SessionName(b.opts.Team), b.opts.Agent, shellCmd)
	if err != nil {
		os.Remove(envFile)
		return Result{}, err
	}
	// Keep the leader's screen after it exits, and any agent's screen when it
	// fails, so `agent screen` and the board can still show what went wrong.
	keep := "failed"
	if b.opts.IsLeader {
		keep = "on"
	}
	tmux.SetOption(pane, "remain-on-exit", keep)
	_, _, pid := tmux.PaneState(pane)
	return Result{Backend: "tmux", Target: pane, PID: pid, Command: b.Plan.Args,
		adapter: b.opts.Adapter, paste: b.Plan.Paste}, nil
}

func (b Built) startSubprocess() (Result, error) {
	if err := os.MkdirAll(filepath.Dir(b.opts.LogPath), 0o700); err != nil {
		return Result{}, err
	}
	logFile, err := os.OpenFile(b.opts.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Result{}, err
	}
	defer logFile.Close()
	shell, err := lookPath("sh", b.Env.Vars["PATH"])
	if err != nil {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, "-c", wrapCommand(b.opts, b.Plan.Args))
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if b.Plan.Stdin != "" {
		cmd.Stdin = strings.NewReader(b.Plan.Stdin)
	}
	cmd.Env = make([]string, 0, len(b.Env.Vars))
	for k, v := range b.Env.Vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// A new session makes the agent its own process group, so stopping it
	// reaches every child and closing the CLI's terminal does not kill it.
	cmd.SysProcAttr = sysproc.Detached()
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	pid := cmd.Process.Pid
	if b.Plan.Stdin != "" {
		// The stdin copy needs a running goroutine; wait in the background.
		go cmd.Wait()
	} else {
		cmd.Process.Release()
	}
	return Result{Backend: "subprocess", Target: b.opts.LogPath, PID: pid, Command: b.Plan.Args}, nil
}

// handleStartup answers the confirmation screens agent CLIs show on first
// start in a directory, then pastes the prompt for CLIs that need it. It
// returns once the screen stops changing.
func handleStartup(ctx context.Context, pane string, ad agent.Agent, paste string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	var last string
	stable := 0
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return
		}
		time.Sleep(300 * time.Millisecond)
		exists, dead, _ := tmux.PaneState(pane)
		if !exists || dead {
			return
		}
		text, err := tmux.Capture(pane, 0)
		if err != nil {
			return
		}
		if strings.TrimSpace(text) != "" && text == last {
			stable++
		} else {
			stable, last = 0, text
		}
		// A dialog is drawn before the TUI reads raw keys; keys sent on the
		// first frame are echoed as text. Act once the screen has settled.
		if keys := ad.StartupKeys(strings.ToLower(text)); len(keys) > 0 {
			if stable < 1 {
				continue
			}
			for i, k := range keys {
				if i > 0 {
					time.Sleep(400 * time.Millisecond)
				}
				tmux.SendKeys(pane, k)
			}
			stable, last = 0, ""
			continue
		}
		if stable >= 3 || (paste != "" && promptVisible(pane, ad)) {
			break
		}
	}
	if paste != "" && ctx.Err() == nil {
		tmux.Paste(pane, paste)
	}
}

// promptVisible reports whether the adapter's screen rules see its input
// prompt, so the first prompt can be pasted before the screen settles.
func promptVisible(pane string, ad agent.Agent) bool {
	screen, err := tmux.Screen(pane)
	if err != nil {
		return false
	}
	title, _ := tmux.Title(pane)
	d, ok := ad.DetectScreen(agent.ScreenInput{Screen: screen, Title: title})
	return ok && d.State == agent.ScreenIdle && d.VisibleIdle
}

// WaitReady applies the adapter's startup interaction to a supervised tmux child.
func WaitReady(ctx context.Context, pane string, adapter agent.Agent, paste string) {
	handleStartup(ctx, pane, adapter, paste, 20*time.Second)
}
