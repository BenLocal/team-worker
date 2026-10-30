//go:build !windows

package sysproc

import (
	"errors"
	"syscall"
)

// Detached starts a child in a new session: it becomes its own process
// group, and closing the parent's terminal does not kill it.
func Detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// Alive reports whether pid exists, including processes owned by another user.
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// TerminateGroup sends SIGTERM to the process group led by pid. A group that
// is already gone is not an error.
func TerminateGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
