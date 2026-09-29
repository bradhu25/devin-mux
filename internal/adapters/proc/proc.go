// Package proc implements core.Proc with POSIX signals.
package proc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

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

// Descendants lists every process below pid using one `ps` invocation
// (portable across macOS and Linux; /proc is Linux-only). Start times are
// derived from etime, so they are accurate to the second.
func (Adapter) Descendants(pid int) ([]core.ProcInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,etime=,comm=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return descendantsFromPS(string(out), pid, time.Now()), nil
}

// descendantsFromPS parses `ps -axo pid=,ppid=,etime=,comm=` output.
func descendantsFromPS(psOut string, root int, now time.Time) []core.ProcInfo {
	byParent := map[int][]core.ProcInfo{}
	for _, line := range strings.Split(psOut, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		info := core.ProcInfo{PID: pid, PPID: ppid, Comm: filepath.Base(strings.Join(f[3:], " ")), Started: now.Add(-parseEtime(f[2]))}
		byParent[ppid] = append(byParent[ppid], info)
	}
	var out []core.ProcInfo
	queue := []int{root}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range byParent[p] {
			out = append(out, c)
			queue = append(queue, c.PID)
		}
	}
	return out
}

// parseEtime handles ps's [[dd-]hh:]mm:ss.
func parseEtime(s string) time.Duration {
	var days int
	if i := strings.IndexByte(s, '-'); i >= 0 {
		days, _ = strconv.Atoi(s[:i])
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	var secs int
	for _, p := range parts {
		n, _ := strconv.Atoi(p)
		secs = secs*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(secs)*time.Second
}
