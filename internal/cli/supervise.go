package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/benlocal/team-worker/internal/agent"
	"github.com/benlocal/team-worker/internal/agent/builtin"
	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/config"
	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/spawn"
	"github.com/benlocal/team-worker/internal/store"
	"github.com/spf13/cobra"
)

func (a *app) superviseCmd() *cobra.Command {
	var team, name, runID, paste string
	cmd := &cobra.Command{Use: "supervise", Hidden: true, Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
		defer cancel()
		if err := a.supervise(ctx, team, name, runID, paste, args); err != nil {
			return a.fail(err)
		}
		return nil
	}}
	cmd.Flags().StringVar(&team, "team", "", "Team name")
	cmd.Flags().StringVar(&name, "agent", "", "Agent name")
	cmd.Flags().StringVar(&runID, "run-id", "", "Current run id")
	cmd.Flags().StringVar(&paste, "paste", "", "Initial prompt to paste after startup")
	for _, flag := range []string{"team", "agent", "run-id"} {
		cmd.MarkFlagRequired(flag)
	}
	return cmd
}

func waitRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (a *app) supervise(ctx context.Context, team, name, runID, paste string, command []string) error {
	c := a.client()
	user := config.IdentityFromEnv().User
	defer func() {
		if ctx.Err() == nil {
			return
		}
		report, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		current, err := c.GetAgent(report, team, name)
		if err != nil || current.RunID != runID {
			return
		}
		// A signal to the supervisor is not a stop request: a shutdown sends
		// one too, and team-workerd resumes interrupted agents when it
		// starts. agent stop records its request before ending the process.
		if _, err := c.AgentExitedRun(report, team, name, runID, store.InterruptedExit); err != nil {
			fmt.Fprintf(a.errOut, "record supervisor exit: %v\n", err)
		}
	}()

	// The outer spawn publishes the process details before a fast child may exit.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var rec model.Agent
		if err := a.retryDaemon(ctx, "read agent", func() (err error) {
			rec, err = c.GetAgent(ctx, team, name)
			return err
		}); err != nil {
			return err
		}
		if rec.RunID != runID {
			return nil
		}
		if rec.StopRequested {
			return a.retryDaemon(ctx, "record exit", func() error {
				_, err := c.AgentExitedRun(ctx, team, name, runID, 0)
				return err
			})
		}
		if rec.PID > 0 || rec.Target != "" {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("spawn did not publish the agent process")
		}
		if !waitRetry(ctx, 20*time.Millisecond) {
			return nil
		}
	}
	registry, err := builtin.Load(config.AgentsDir())
	if err != nil {
		return err
	}
	stdin := a.in
	restarts := 0
	for {
		for marker := config.StartBlockedFile(); marker != ""; {
			if _, err := os.Stat(marker); errors.Is(err, os.ErrNotExist) {
				break
			} else if err != nil {
				return fmt.Errorf("read startup marker: %w", err)
			}
			if !waitRetry(ctx, time.Second) {
				return nil
			}
		}
		var session model.Session
		var rec model.Agent
		if err := a.retryDaemon(ctx, "read agent", func() (err error) {
			if session, err = c.GetSession(ctx, team, name, user); err != nil {
				return err
			}
			rec, err = c.GetAgent(ctx, team, name)
			return err
		}); err != nil {
			return err
		}
		if rec.RunID != runID {
			return nil
		}
		if rec.StopRequested {
			return a.retryDaemon(ctx, "record exit", func() error {
				_, err := c.AgentExitedRun(ctx, team, name, runID, 0)
				return err
			})
		}
		started := time.Now()
		childCtx, cancelChild := context.WithCancel(ctx)
		child := exec.CommandContext(childCtx, command[0], command[1:]...)
		child.Dir = session.Cwd
		child.Stdin, child.Stdout, child.Stderr = stdin, a.out, a.errOut
		child.Env = append(os.Environ(), "TEAM_WORKER_RUN_ID="+runID)
		child.Cancel = func() error { return child.Process.Signal(syscall.SIGTERM) }
		child.WaitDelay = 5 * time.Second
		code := 127
		if err := child.Start(); err != nil {
			cancelChild()
			if _, recordErr := c.AgentExitedRun(ctx, team, name, runID, code); recordErr != nil {
				return errors.Join(err, recordErr)
			}
			return fmt.Errorf("start agent: %w", err)
		}
		// `agent stop` may run on another machine; it can only record the
		// request, so this machine ends the child when it sees one.
		watchDone := make(chan struct{})
		go watchStop(childCtx, watchDone, cancelChild, func() (model.Agent, error) { return c.GetAgent(ctx, team, name) }, runID)
		readyCtx, readyCancel := context.WithCancel(ctx)
		waitDone := make(chan error, 1)
		go func() {
			waitDone <- child.Wait()
			readyCancel()
		}()
		if session.Backend == "tmux" {
			spawn.WaitReady(readyCtx, rec.Target, registry.For(session.Command), paste)
		}
		waitErr := <-waitDone
		close(watchDone)
		cancelChild()
		readyCancel()
		code = childExitCode(waitErr)
		// A cancelled supervisor records its exit in the deferred report.
		if ctx.Err() != nil {
			return nil
		}
		// The exit is retried while team-workerd restarts: given up, the agent
		// would stay recorded as running and keepalive would end with it.
		if err := a.retryDaemon(ctx, "record exit", func() error {
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			// Reading the session first lets the daemon save a Codex session
			// id that the restart below resumes.
			if _, err := c.GetSession(callCtx, team, name, user); err != nil && !client.IsNotFound(err) {
				if daemonDown(err) {
					return err
				}
				fmt.Fprintf(a.errOut, "read session: %v\n", err)
			}
			_, err := c.AgentExitedRun(callCtx, team, name, runID, code)
			return err
		}); err != nil {
			return err
		}
		if err := a.retryDaemon(ctx, "read agent", func() (err error) {
			if rec, err = c.GetAgent(ctx, team, name); err != nil {
				return err
			}
			session, err = c.GetSession(ctx, team, name, user)
			return err
		}); err != nil {
			return err
		}
		if rec.RunID != runID || rec.StopRequested {
			return nil
		}
		if !session.KeepAlive {
			return waitErr
		}
		if time.Since(started) >= time.Minute {
			restarts = 0
		}
		if restarts >= session.MaxRestarts {
			return fmt.Errorf("keepalive restart limit reached after %d restart(s); last exit code %d", restarts, code)
		}
		if session.SessionID == "" {
			return fmt.Errorf("keepalive cannot resume without an exact native session id; use session save")
		}
		delay := time.Duration(session.RestartDelayMillis) * time.Millisecond
		if delay <= 0 {
			delay = time.Second
		}
		for i := 0; i < restarts && delay < time.Minute; i++ {
			delay *= 2
		}
		if delay > time.Minute {
			delay = time.Minute
		}
		fmt.Fprintf(a.errOut, "Agent exited (%d); resuming exact session in %s (%d/%d)\n", code, delay, restarts+1, session.MaxRestarts)
		if !waitRetry(ctx, delay) {
			return nil
		}
		native, err := agent.ResumeCommand(session.Command, session.SessionID, session.Backend == "tmux")
		if err != nil {
			return err
		}
		role := "worker"
		if config.IdentityFromEnv().IsLeader {
			role = "leader"
		}
		var contextText string
		if err := a.retryDaemon(ctx, "read team context", func() (err error) {
			contextText, err = a.teamContext(ctx, hookTarget{team: team, agent: name, user: user, role: role}, session.Cwd)
			return err
		}); err != nil {
			return err
		}
		plan := registry.For(session.Command).Prepare(agent.Session{Command: native, Prompt: "Resume your saved conversation. Re-check task ownership before continuing.\n" + contextText,
			Interactive: session.Backend == "tmux", SkipPermissions: session.SkipPermissions, Team: team, AgentName: name,
			Leader: config.IdentityFromEnv().IsLeader, WorkerBin: workerBinary(), StateDir: filepath.Join(config.DataDir(), "run")})
		for _, file := range plan.Files {
			if err := os.MkdirAll(filepath.Dir(file.Path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(file.Path, file.Content, 0o600); err != nil {
				return err
			}
		}
		next := rec
		next.PreviousRunID, next.RunID, next.StopRequested = runID, store.NewAgentID(), false
		if err := a.retryDaemon(ctx, "register restart", func() error {
			_, err := c.RegisterAgent(ctx, next)
			return err
		}); err != nil {
			current, readErr := c.GetAgent(ctx, team, name)
			// A retry after a lost answer finds its own registration.
			registered := readErr == nil && current.RunID == next.RunID && !current.StopRequested
			if !registered {
				if readErr == nil && (current.StopRequested || current.RunID != runID) {
					return nil
				}
				return err
			}
		}
		runID, session.RunID = next.RunID, next.RunID
		if err := a.retryDaemon(ctx, "save session", func() error {
			_, err := c.SaveSession(ctx, session)
			return err
		}); err != nil {
			if _, exitErr := c.AgentExitedRun(ctx, team, name, runID, -1); exitErr != nil {
				return errors.Join(err, exitErr)
			}
			return err
		}
		command, paste = plan.Args, plan.Paste
		stdin = nil
		if plan.Stdin != "" {
			stdin = strings.NewReader(plan.Stdin)
		}
		restarts++
	}
}

// childExitCode turns the agent CLI's end into the exit code its run records.
// A crash signal (SIGSEGV, SIGABRT, ...) is 128 plus the signal, like a shell
// reports it, so the agent stays down. A termination signal counts as
// interrupted, like the supervisor's own: a shutdown signals both, and when
// the child's end is seen first its run must still be resumed on restart.
func childExitCode(waitErr error) int {
	if waitErr == nil {
		return 0
	}
	var exit *exec.ExitError
	if !errors.As(waitErr, &exit) {
		return store.InterruptedExit
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return exit.ExitCode()
	}
	switch status.Signal() {
	case syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL:
		return store.InterruptedExit
	}
	return 128 + int(status.Signal())
}

// daemonRetryFor is how long the supervisor keeps calling team-workerd while
// it cannot be reached, e.g. during a restart; daemonRetryDelay is the first
// pause, doubled up to five seconds.
var (
	daemonRetryFor   = 2 * time.Minute
	daemonRetryDelay = 500 * time.Millisecond
)

// retryDaemon runs call until it succeeds, team-workerd answers with an
// error, daemonRetryFor passes or ctx ends. Only failures to reach the daemon
// are retried: connection errors, a failed start and 503 while it stops.
func (a *app) retryDaemon(ctx context.Context, what string, call func() error) error {
	deadline := time.Now().Add(daemonRetryFor)
	delay := daemonRetryDelay
	for {
		err := call()
		if err == nil || !daemonDown(err) || ctx.Err() != nil || time.Now().After(deadline) {
			return err
		}
		fmt.Fprintf(a.errOut, "%s: team-workerd unavailable, retrying in %s: %v\n", what, delay, err)
		if !waitRetry(ctx, delay) {
			return err
		}
		delay = min(2*delay, 5*time.Second)
	}
}

// daemonDown reports an error from not reaching team-workerd rather than an
// answer from it. A call that timed out counts as not reached; the caller's
// own context ending does not.
func daemonDown(err error) bool {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusServiceUnavailable
	}
	return !errors.Is(err, context.Canceled)
}

// stopPollInterval is how often a running child checks for a stop request.
var stopPollInterval = 2 * time.Second

// watchStop cancels the child when its record asks it to stop or a newer run
// replaced it, until done closes or ctx ends.
func watchStop(ctx context.Context, done <-chan struct{}, cancel context.CancelFunc, read func() (model.Agent, error), runID string) {
	ticker := time.NewTicker(stopPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if rec, err := read(); err == nil && (rec.StopRequested || rec.RunID != runID) {
				cancel()
				return
			}
		}
	}
}
