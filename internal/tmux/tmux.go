// Package tmux wraps the tmux commands used to run and observe agents.
package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func run(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("tmux %s: %s", args[0], msg)
	}
	return stdout.String(), nil
}

func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

func HasSession(name string) bool {
	_, err := run("has-session", "-t", "="+name)
	return err == nil
}

// NewWindow runs shellCmd in a new window of session (creating the session
// when needed) and returns the new pane id, e.g. "%12". Pane ids stay valid
// when windows are renamed or moved.
func NewWindow(session, window, shellCmd string) (string, error) {
	var out string
	var err error
	if HasSession(session) {
		out, err = run("new-window", "-d", "-P", "-F", "#{pane_id}", "-t", "="+session+":", "-n", window, shellCmd)
	} else {
		out, err = run("new-session", "-d", "-P", "-F", "#{pane_id}", "-s", session, "-n", window, shellCmd)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// PaneState reports whether a pane exists, whether its process has exited,
// and the pid of the process started in it.
func PaneState(pane string) (exists, dead bool, pid int) {
	out, err := run("display-message", "-p", "-t", pane, "#{pane_id} #{pane_dead} #{pane_pid}")
	if err != nil {
		return false, false, 0
	}
	fields := strings.Fields(out)
	if len(fields) < 3 || fields[0] != pane {
		return false, false, 0
	}
	pid, _ = strconv.Atoi(fields[2])
	return true, fields[1] == "1", pid
}

func SetOption(pane, name, value string) error {
	_, err := run("set-option", "-p", "-t", pane, name, value)
	return err
}

// Capture returns the visible text of a pane; history adds that many
// scrollback lines above it.
func Capture(pane string, history int) (string, error) {
	args := []string{"capture-pane", "-p", "-J", "-t", pane}
	if history > 0 {
		args = append(args, "-S", "-"+strconv.Itoa(history))
	}
	return run(args...)
}

// Screen returns the rows a pane shows now, as screen rules read them: no
// trailing spaces, no trailing blank rows, joined with "\n" and ending in
// "\n". Wrapped lines stay separate rows, unlike Capture's joined lines.
func Screen(pane string) (string, error) {
	out, err := run("capture-pane", "-p", "-t", pane)
	if err != nil {
		return "", err
	}
	rows := strings.Split(out, "\n")
	for i, r := range rows {
		rows[i] = strings.TrimRightFunc(r, unicode.IsSpace)
	}
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	if len(rows) == 0 {
		return "", nil
	}
	return strings.Join(rows, "\n") + "\n", nil
}

// Title returns the title the pane's program set (OSC 0/2). -u keeps it
// UTF-8 when the caller's locale is not; tmux would print "_" otherwise.
func Title(pane string) (string, error) {
	out, err := run("-u", "display-message", "-p", "-t", pane, "#{pane_title}")
	return strings.TrimSuffix(out, "\n"), err
}

func SendKeys(pane string, keys ...string) error {
	_, err := run(append([]string{"send-keys", "-t", pane}, keys...)...)
	return err
}

// Paste delivers text as one bracketed paste and submits it, so newlines in
// the text do not act as separate Enter presses.
func Paste(pane, text string) error {
	f, err := os.CreateTemp("", "team-worker-paste-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	f.Close()
	buf := "team-worker-" + strings.TrimPrefix(pane, "%") + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := run("load-buffer", "-b", buf, f.Name()); err != nil {
		return err
	}
	if _, err := run("paste-buffer", "-d", "-p", "-r", "-b", buf, "-t", pane); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	return SendKeys(pane, "Enter")
}

func KillPane(pane string) error {
	_, err := run("kill-pane", "-t", pane)
	if err != nil {
		message := err.Error()
		if strings.Contains(message, "can't find") || strings.Contains(message, "no server running") ||
			(strings.Contains(message, "error connecting to") && strings.Contains(message, "No such file or directory")) {
			return nil
		}
	}
	return err
}
