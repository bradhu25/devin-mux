package proc

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

// startGroup starts `sh -c 'sleep 30'` as its own process group leader with a
// child, mirroring a tmux pane running the dmux wrapper around devin.
func startGroup(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "sleep 30 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	time.Sleep(100 * time.Millisecond)
	return cmd
}

func TestTerminate_KillsWholeGroup(t *testing.T) {
	cmd := startGroup(t)
	a := Adapter{}
	if !a.Alive(cmd.Process.Pid) {
		t.Fatal("leader should be alive")
	}
	if err := a.Terminate(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("leader did not exit after SIGTERM to group")
	}
	// The sleep child was in the same group and must be gone too.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if out, _ := exec.CommandContext(context.Background(), "pgrep", "-g", itoa(cmd.Process.Pid)).Output(); len(out) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("child in process group survived")
}

func TestKill_AndGoneIsNotAnError(t *testing.T) {
	cmd := startGroup(t)
	a := Adapter{}
	if err := a.Kill(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if a.Alive(cmd.Process.Pid) {
		t.Fatal("reaped process reported alive")
	}
	if err := a.Terminate(cmd.Process.Pid); err != nil {
		t.Fatalf("signalling a gone group must not error: %v", err)
	}
}

func TestRefusesDangerousPids(t *testing.T) {
	a := Adapter{}
	for _, pid := range []int{0, 1, -5} {
		if err := a.Terminate(pid); err == nil {
			t.Errorf("pid %d must be refused", pid)
		}
		if a.Alive(pid) && pid <= 0 {
			t.Errorf("pid %d must not be alive", pid)
		}
	}
}

func itoa(i int) string {
	b := []byte{}
	if i == 0 {
		return "0"
	}
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestDescendantsFromPS(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	ps := `
    1     0 18-08:15:19 /sbin/launchd
65917 53423       15:12 /Users/bradleyhu/devin-mux/bin/dmux
65921 65917       15:12 devin
65922 65921       15:10 /Users/bradleyhu/.local/bin/devin
70001 65922       00:03 /bin/sh
70002 70001       00:03 /usr/local/go/bin/go
80164 77856 01-09:32:55 devin
  garbage line
`
	got := descendantsFromPS(ps, 65917, now)
	if len(got) != 4 {
		t.Fatalf("want dmux's 4 descendants, got %d: %+v", len(got), got)
	}
	byPID := map[int]core.ProcInfo{}
	for _, p := range got {
		byPID[p.PID] = p
	}
	if byPID[65922].Comm != "devin" || byPID[70001].Comm != "sh" || byPID[70002].Comm != "go" {
		t.Fatalf("comm parsing: %+v", byPID)
	}
	if want := now.Add(-3 * time.Second); !byPID[70002].Started.Equal(want) {
		t.Fatalf("started: %v want %v", byPID[70002].Started, want)
	}
	if want := now.Add(-(15*time.Minute + 12*time.Second)); !byPID[65921].Started.Equal(want) {
		t.Fatalf("started mm:ss: %v want %v", byPID[65921].Started, want)
	}
	if _, foreign := byPID[80164]; foreign {
		t.Fatal("unrelated process must not be included")
	}
}

func TestParseEtime(t *testing.T) {
	for in, want := range map[string]time.Duration{"00:03": 3 * time.Second, "15:12": 15*time.Minute + 12*time.Second, "01:09:32": time.Hour + 9*time.Minute + 32*time.Second, "18-08:15:19": 18*24*time.Hour + 8*time.Hour + 15*time.Minute + 19*time.Second} {
		if got := parseEtime(in); got != want {
			t.Errorf("parseEtime(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDescendants_Real(t *testing.T) {
	cmd := startGroup(t) // sh -c 'sleep 30 & wait' => sh with a sleep child
	got, err := Adapter{}.Descendants(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range got {
		if p.Comm == "sleep" && time.Since(p.Started) < 10*time.Second {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a recent sleep child: %+v", got)
	}
}
