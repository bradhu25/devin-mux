package launcher

import (
	"bytes"
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

type recorder struct{ events []core.SessionEvent }

func (r *recorder) emit(ev core.SessionEvent) error { r.events = append(r.events, ev); return nil }

func run(t *testing.T, argv []string, mutate func(*Options)) (Result, *recorder, *bytes.Buffer, error) {
	t.Helper()
	rec := &recorder{}
	var out bytes.Buffer
	o := Options{SessionID: "s_test", Argv: argv, Stdout: &out, Stderr: &out, Stdin: strings.NewReader(""), Emit: rec.emit}
	if mutate != nil {
		mutate(&o)
	}
	res, err := Run(context.Background(), o)
	return res, rec, &out, err
}

func TestRun_ExitZero(t *testing.T) {
	res, rec, out, err := run(t, []string{"sh", "-c", "echo hello; exit 0"}, nil)
	if err != nil || res.ExitCode != 0 || res.Signal != "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.Contains(out.String(), "hello") {
		t.Fatalf("child stdout not forwarded: %q", out.String())
	}
	if len(rec.events) != 1 || rec.events[0].Type != core.ProcessExited || rec.events[0].SessionID != "s_test" {
		t.Fatalf("events: %+v", rec.events)
	}
	if rec.events[0].Data["exitCode"] != 0 || rec.events[0].Data["durationMs"] == nil {
		t.Fatalf("event data: %+v", rec.events[0].Data)
	}
}

func TestRun_NonZeroExit(t *testing.T) {
	res, rec, _, err := run(t, []string{"sh", "-c", "exit 3"}, nil)
	if err != nil || res.ExitCode != 3 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if rec.events[0].Data["exitCode"] != 3 {
		t.Fatalf("event: %+v", rec.events[0].Data)
	}
}

func TestRun_ChildEnvHasSessionID(t *testing.T) {
	_, _, out, _ := run(t, []string{"sh", "-c", "echo id=$DMUX_SESSION_ID"}, nil)
	if !strings.Contains(out.String(), "id=s_test") {
		t.Fatalf("DMUX_SESSION_ID not set for child: %q", out.String())
	}
}

func TestRun_Dir(t *testing.T) {
	dir := t.TempDir()
	_, _, out, _ := run(t, []string{"pwd"}, func(o *Options) { o.Dir = dir })
	if !strings.Contains(out.String(), dir[strings.LastIndex(dir, "/"):]) {
		t.Fatalf("cwd not applied: %q", out.String())
	}
}

func TestRun_StartFailureStillEmits(t *testing.T) {
	res, rec, _, err := run(t, []string{"/definitely/not/a/binary"}, nil)
	if err == nil || res.ExitCode != 127 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(rec.events) != 1 || rec.events[0].Data["exitCode"] != 127 || rec.events[0].Data["error"] == nil {
		t.Fatalf("start failure must emit process_exited with error: %+v", rec.events)
	}
}

// The wrapper forwards signals to the child and reports signal termination.
func TestRun_SignalIsForwardedAndReported(t *testing.T) {
	rec := &recorder{}
	done := make(chan struct{})
	var res Result
	go func() {
		defer close(done)
		res, _ = Run(context.Background(), Options{SessionID: "s_sig", Argv: []string{"sleep", "30"}, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Emit: rec.emit})
	}()
	time.Sleep(200 * time.Millisecond) // let the child start and Notify register
	// Signal *ourselves*: the wrapper must forward SIGTERM to the child.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("wrapper did not exit after SIGTERM was forwarded")
	}
	if res.Signal != "terminated" || res.ExitCode != -1 {
		t.Fatalf("expected signal termination, got %+v", res)
	}
	if rec.events[0].Data["signal"] != "terminated" {
		t.Fatalf("event should record signal: %+v", rec.events[0].Data)
	}
}

func TestRun_ContextCancelTerminatesChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	rec := &recorder{}
	start := time.Now()
	res, _ := Run(ctx, Options{SessionID: "s_ctx", Argv: []string{"sleep", "30"}, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Emit: rec.emit})
	if time.Since(start) > 3*time.Second {
		t.Fatal("child outlived context")
	}
	if res.Signal == "" {
		t.Fatalf("expected signal termination via ctx, got %+v", res)
	}
}

func TestRun_Validation(t *testing.T) {
	if _, err := Run(context.Background(), Options{Argv: []string{"true"}}); err == nil {
		t.Fatal("missing session id must error")
	}
	if _, err := Run(context.Background(), Options{SessionID: "s"}); err == nil {
		t.Fatal("empty argv must error")
	}
}

// Hold is a no-op when stdin is not a terminal, so tests and non-tty runs
// never block.
func TestRun_HoldSkippedWithoutTTY(t *testing.T) {
	start := time.Now()
	res, _, _, err := run(t, []string{"true"}, func(o *Options) { o.Hold = true; o.HoldTimeout = 10 * time.Second })
	if err != nil || res.ExitCode != 0 || time.Since(start) > 2*time.Second {
		t.Fatalf("hold should be skipped without a tty: %+v %v", res, err)
	}
}
