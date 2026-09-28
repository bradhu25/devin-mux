// Package launcher implements `dmux run`, the process that owns a Devin
// session inside its tmux pane. Being Devin's parent, it is the one
// component that always observes exit — including crashes and signals —
// regardless of whether Devin's hooks fired. It emits process_exited and
// then holds the pane open so the tmux target does not vanish under the
// Session record (Spike 1 finding).
package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

// Options for Run.
type Options struct {
	SessionID string   // dmux Session.ID; also exported as DMUX_SESSION_ID
	Argv      []string // command to run (e.g. devin ...)
	Dir       string   // working directory; "" = inherit
	// Hold keeps the pane open after exit until the user presses Enter, or
	// until HoldTimeout elapses. Disabled when stdin is not a terminal.
	Hold        bool
	HoldTimeout time.Duration

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Emit records an event; defaults to state.AppendSessionEvent.
	Emit func(core.SessionEvent) error
	// Now is injectable for tests.
	Now func() time.Time
}

// Result describes how the child ended.
type Result struct {
	ExitCode int    // -1 if killed by a signal
	Signal   string // non-empty if terminated by a signal
}

// Run executes the child, forwarding SIGINT/SIGTERM/SIGHUP, waits for it,
// emits process_exited, and optionally holds the pane. It returns the
// child's result; the caller decides the wrapper's own exit code.
func Run(ctx context.Context, o Options) (Result, error) {
	if o.SessionID == "" {
		return Result{}, errors.New("run: session id required")
	}
	if len(o.Argv) == 0 {
		return Result{}, errors.New("run: empty command")
	}
	now := o.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}

	//nolint:gosec // Argv is constructed by dmux (spawn), not from untrusted input.
	cmd := exec.CommandContext(ctx, o.Argv[0], o.Argv[1:]...)
	cmd.Dir = o.Dir
	cmd.Env = append(os.Environ(), "DMUX_SESSION_ID="+o.SessionID)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = o.Stdin, o.Stdout, o.Stderr
	// Forward signals rather than dying first: if the wrapper is killed the
	// child would be orphaned and process_exited never recorded.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second

	started := now()
	if err := cmd.Start(); err != nil {
		res := Result{ExitCode: 127}
		_ = emit(o, core.SessionEvent{
			EventID: core.NewEventID(), SessionID: o.SessionID, Type: core.ProcessExited, Timestamp: now(),
			Data: map[string]any{"exitCode": res.ExitCode, "error": err.Error(), "startedAt": started},
		})
		return res, fmt.Errorf("start %q: %w", o.Argv[0], err)
	}

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var waitErr error
loop:
	for {
		select {
		case s := <-sigs:
			_ = cmd.Process.Signal(s)
		case waitErr = <-done:
			break loop
		}
	}

	res := classify(waitErr)
	data := map[string]any{"exitCode": res.ExitCode, "startedAt": started, "durationMs": now().Sub(started).Milliseconds()}
	if res.Signal != "" {
		data["signal"] = res.Signal
	}
	emitErr := emit(o, core.SessionEvent{EventID: core.NewEventID(), SessionID: o.SessionID, Type: core.ProcessExited, Timestamp: now(), Data: data})

	if o.Hold {
		hold(o, res)
	}
	return res, emitErr
}

func emit(o Options, ev core.SessionEvent) error {
	if o.Emit == nil {
		return nil
	}
	return o.Emit(ev)
}

func classify(waitErr error) Result {
	if waitErr == nil {
		return Result{ExitCode: 0}
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return Result{ExitCode: -1, Signal: ws.Signal().String()}
		}
		return Result{ExitCode: ee.ExitCode()}
	}
	return Result{ExitCode: -1}
}

// hold prints a summary and waits for Enter (or timeout). Only meaningful
// when stdin is a terminal; otherwise it returns immediately.
func hold(o Options, res Result) {
	if f, ok := o.Stdin.(*os.File); !ok || !isTerminal(f) {
		return
	}
	switch {
	case res.Signal != "":
		fmt.Fprintf(o.Stdout, "\n[dmux] devin terminated by %s. Press Enter to close this window.\n", res.Signal)
	case res.ExitCode == 0:
		fmt.Fprintf(o.Stdout, "\n[dmux] devin exited. Press Enter to close this window, or run `dmux resume` to continue this conversation.\n")
	default:
		fmt.Fprintf(o.Stdout, "\n[dmux] devin exited with code %d. Press Enter to close this window.\n", res.ExitCode)
	}
	timeout := o.HoldTimeout
	if timeout == 0 {
		timeout = 24 * time.Hour
	}
	ch := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := o.Stdin.Read(buf)
			if err != nil || (n == 1 && (buf[0] == '\n' || buf[0] == '\r')) {
				close(ch)
				return
			}
		}
	}()
	select {
	case <-ch:
	case <-time.After(timeout):
	}
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
