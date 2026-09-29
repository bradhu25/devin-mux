package proc

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
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
