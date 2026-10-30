// Package proc checks and stops agent processes for both spawn backends.
package proc

import (
	"errors"
	"os"
	"strings"

	"github.com/benlocal/team-worker/internal/model"
	"github.com/benlocal/team-worker/internal/sysproc"
	"github.com/benlocal/team-worker/internal/tmux"
)

// Probe reports whether an agent's process is alive. known is false when
// the backend cannot be checked, for example an unknown backend.
func Probe(a model.Agent) (alive, known bool) {
	switch a.Backend {
	case "tmux":
		if a.Target == "" {
			return false, false
		}
		exists, dead := paneState(a)
		return exists && !dead, true
	case "subprocess":
		if a.PID <= 0 {
			return false, false
		}
		return pidAlive(a.PID), true
	}
	return false, false
}

// paneState reports the agent's own tmux pane. Pane ids restart at %0 with a
// new tmux server, so after a restart a recorded id can name another agent's
// pane; a pane started with a different process than the recorded one is not
// the agent's and counts as gone.
func paneState(a model.Agent) (exists, dead bool) {
	exists, dead, pid := tmux.PaneState(a.Target)
	if exists && a.PID > 0 && pid != a.PID {
		return false, false
	}
	return exists, dead
}

func pidAlive(pid int) bool { return sysproc.Alive(pid) }

// Stop ends an agent's process: the tmux pane, or the subprocess's whole
// process group (subprocess agents run in their own session).
func Stop(a model.Agent) error {
	switch a.Backend {
	case "tmux":
		if a.Target == "" {
			return nil
		}
		if exists, _ := paneState(a); !exists {
			return nil
		}
		return tmux.KillPane(a.Target)
	case "subprocess":
		if a.PID <= 1 {
			return nil
		}
		return sysproc.TerminateGroup(a.PID)
	}
	return nil
}

// Screen returns what an agent shows: the tmux pane text, or the tail of a
// subprocess agent's log. history adds that many earlier lines.
func Screen(a model.Agent, history int) (string, error) {
	if a.Backend == "tmux" {
		if exists, _ := paneState(a); !exists {
			return "", errors.New("the agent's terminal is closed; the agent has exited")
		}
		return tmux.Capture(a.Target, history)
	}
	data, err := os.ReadFile(a.Target)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	if keep := 50 + history; len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	return strings.Join(lines, "\n"), nil
}
