package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/benlocal/team-worker/internal/client"
	"github.com/benlocal/team-worker/internal/store"
)

func TestChildExitCodeTellsCrashesFromInterruptions(t *testing.T) {
	for _, c := range []struct {
		script string
		want   int
	}{
		{"exit 0", 0},
		{"exit 3", 3},
		// A crash keeps the agent down after a restart.
		{"kill -SEGV $$", 128 + int(syscall.SIGSEGV)},
		{"kill -ABRT $$", 128 + int(syscall.SIGABRT)},
		// A shutdown signals the child and its supervisor alike; if the child
		// is seen first, its run must still count as interrupted.
		{"kill -TERM $$", store.InterruptedExit},
		{"kill -HUP $$", store.InterruptedExit},
		{"kill -KILL $$", store.InterruptedExit},
	} {
		err := exec.Command("sh", "-c", c.script).Run()
		if got := childExitCode(err); got != c.want {
			t.Errorf("%s: exit code %d, want %d (%v)", c.script, got, c.want, err)
		}
	}
}

func TestRetryDaemonWaitsOnlyWhileTheDaemonIsDown(t *testing.T) {
	oldFor, oldDelay := daemonRetryFor, daemonRetryDelay
	daemonRetryFor, daemonRetryDelay = time.Second, time.Millisecond
	t.Cleanup(func() { daemonRetryFor, daemonRetryDelay = oldFor, oldDelay })
	a := &app{errOut: io.Discard}
	ctx := context.Background()

	down := []error{
		&client.APIError{Status: http.StatusServiceUnavailable, Msg: "stopping"},
		fmt.Errorf("dial: %w", syscall.ECONNREFUSED),
		errors.New("team-workerd did not become ready"),
	}
	calls := 0
	err := a.retryDaemon(ctx, "test", func() error {
		calls++
		if calls <= len(down) {
			return down[calls-1]
		}
		return nil
	})
	if err != nil || calls != len(down)+1 {
		t.Fatalf("want success after %d retries, got %v after %d calls", len(down), err, calls)
	}

	// An answer from the daemon is final.
	calls = 0
	notFound := &client.APIError{Status: http.StatusNotFound, Msg: "no such agent"}
	if err := a.retryDaemon(ctx, "test", func() error { calls++; return notFound }); !errors.Is(err, notFound) || calls != 1 {
		t.Fatalf("an API error was retried: %v after %d calls", err, calls)
	}

	// It gives up after daemonRetryFor, and at once when ctx ends.
	start := time.Now()
	if err := a.retryDaemon(ctx, "test", func() error { return down[0] }); err == nil || time.Since(start) < daemonRetryFor {
		t.Fatalf("gave up after %s with %v", time.Since(start), err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	calls = 0
	if err := a.retryDaemon(cancelled, "test", func() error { calls++; return down[0] }); err == nil || calls != 1 {
		t.Fatalf("retried after ctx ended: %v after %d calls", err, calls)
	}
}
