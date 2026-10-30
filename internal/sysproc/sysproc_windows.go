//go:build windows

package sysproc

import (
	"errors"
	"os"
	"syscall"
)

// Detached starts a child in a new process group so console signals sent to
// the parent do not reach it.
func Detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// Alive reports whether pid exists; opening a process fails once it has exited.
func Alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}

// TerminateGroup kills the process. Windows has no process-group signal, so
// its children are not reached. A process that is already gone is not an error.
func TerminateGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	defer p.Release()
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
