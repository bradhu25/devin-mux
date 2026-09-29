// Package proc implements core.Proc with POSIX signals.
package proc

import (
	"errors"
	"syscall"

	"github.com/bradhu25/devin-mux/internal/core"
)

// Adapter signals process groups. Zero value is usable.
type Adapter struct{}

var _ core.Proc = Adapter{}

// Terminate sends SIGTERM to pid's process group (pid is the group leader:
// tmux starts each pane's command as one).
func (Adapter) Terminate(pid int) error { return signalGroup(pid, syscall.SIGTERM) }

// Kill sends SIGKILL to pid's process group.
func (Adapter) Kill(pid int) error { return signalGroup(pid, syscall.SIGKILL) }

// Alive reports whether pid exists (signal 0). A zombie still counts as
// alive until reaped; callers combine this with tmux's pane_dead.
func (Adapter) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func signalGroup(pid int, sig syscall.Signal) error {
	if pid <= 1 {
		return errors.New("refusing to signal pid <= 1")
	}
	err := syscall.Kill(-pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil // already gone
	}
	return err
}
