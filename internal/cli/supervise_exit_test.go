package cli

import (
	"os/exec"
	"syscall"
	"testing"

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
