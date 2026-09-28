// Package tmux implements core.Tmux by shelling out to the tmux binary.
// This is one of the only packages allowed to import os/exec.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

// DefaultTimeout bounds every tmux invocation.
const DefaultTimeout = 10 * time.Second

// Adapter runs tmux commands. Zero value targets the default server.
type Adapter struct {
	// Bin is the tmux executable; defaults to "tmux".
	Bin string
	// Socket, if set, is passed as -L so tests can use an isolated server.
	Socket string
	// Timeout per invocation; defaults to DefaultTimeout.
	Timeout time.Duration
	// LookupEnv is injectable for tests; defaults to os.LookupEnv.
	LookupEnv func(string) (string, bool)
}

var _ core.Tmux = (*Adapter)(nil)

// Error is returned when tmux exits non-zero.
type Error struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.ExitCode)
	}
	return fmt.Sprintf("tmux %s: %s", strings.Join(e.Args, " "), msg)
}

func (a *Adapter) bin() string {
	if a.Bin != "" {
		return a.Bin
	}
	return "tmux"
}

func (a *Adapter) baseArgs() []string {
	if a.Socket != "" {
		return []string{"-L", a.Socket}
	}
	return nil
}

func (a *Adapter) run(ctx context.Context, args ...string) (string, error) {
	timeout := a.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, a.bin(), append(a.baseArgs(), args...)...)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("tmux %s: %w", strings.Join(args, " "), ctx.Err())
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", &Error{Args: args, ExitCode: ee.ExitCode(), Stderr: stderr.String()}
		}
		return "", fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func (a *Adapter) Available(ctx context.Context) error {
	if _, err := exec.LookPath(a.bin()); err != nil {
		return fmt.Errorf("tmux not found on PATH: %w", err)
	}
	_, err := a.run(ctx, "-V")
	return err
}

// SanitizeSessionName applies tmux's own rule: '.' and ':' are not allowed
// in session names (they are target separators) and get replaced by '_'.
// Callers must still rely on the returned $N id, never the name.
func SanitizeSessionName(name string) string {
	return strings.NewReplacer(".", "_", ":", "_").Replace(name)
}

func (a *Adapter) hasSession(ctx context.Context, name string) (bool, error) {
	_, err := a.run(ctx, "has-session", "-t", "="+name)
	var te *Error
	if errors.As(err, &te) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (a *Adapter) SpawnWindow(ctx context.Context, opts SpawnOpts) (core.TmuxTarget, error) {
	return a.spawn(ctx, core.SpawnWindowOpts(opts))
}

// SpawnOpts is an alias so callers outside core can name the type.
type SpawnOpts = core.SpawnWindowOpts

func (a *Adapter) spawn(ctx context.Context, opts core.SpawnWindowOpts) (core.TmuxTarget, error) {
	if len(opts.Argv) == 0 {
		return core.TmuxTarget{}, errors.New("SpawnWindow: empty Argv")
	}
	if opts.SessionID == "" {
		return core.TmuxTarget{}, errors.New("SpawnWindow: SessionID required for tagging")
	}
	name := SanitizeSessionName(opts.SessionName)
	shellCmd := ShellJoin(opts.Argv)

	common := []string{"-d", "-P", "-F", "#{session_id}\t#{window_id}"}
	if opts.WindowName != "" {
		common = append(common, "-n", opts.WindowName)
	}
	if opts.Cwd != "" {
		common = append(common, "-c", opts.Cwd)
	}
	for k, v := range opts.Env {
		common = append(common, "-e", k+"="+v)
	}

	exists, err := a.hasSession(ctx, name)
	if err != nil {
		return core.TmuxTarget{}, err
	}
	var out string
	if exists {
		out, err = a.run(ctx, append(append([]string{"new-window", "-t", "=" + name + ":"}, common...), shellCmd)...)
	} else {
		out, err = a.run(ctx, append(append([]string{"new-session", "-s", name}, common...), shellCmd)...)
	}
	if err != nil {
		return core.TmuxTarget{}, err
	}
	sid, wid, ok := strings.Cut(out, "\t")
	if !ok || !strings.HasPrefix(sid, "$") || !strings.HasPrefix(wid, "@") {
		return core.TmuxTarget{}, fmt.Errorf("unexpected tmux id output %q", out)
	}
	target := core.TmuxTarget{SessionName: name, SessionID: sid, WindowID: wid}

	// Tag and harden. If any of this fails, kill the window so we never leave
	// an untagged managed window behind.
	steps := [][]string{
		{"set-option", "-w", "-t", wid, core.TagSession, opts.SessionID},
		{"set-option", "-w", "-t", wid, "remain-on-exit", "on"},
	}
	if !exists && opts.WorkspaceID != "" {
		steps = append(steps, []string{"set-option", "-t", sid, core.TagWorkspace, opts.WorkspaceID})
	}
	for _, s := range steps {
		if _, err := a.run(ctx, s...); err != nil {
			_ = a.KillWindow(ctx, wid)
			return core.TmuxTarget{}, fmt.Errorf("tag window: %w", err)
		}
	}
	return target, nil
}

const listFormat = "#{session_id}\t#{session_name}\t#{window_id}\t#{window_name}\t#{pane_pid}\t#{pane_dead}\t#{" + core.TagWorkspace + "}\t#{" + core.TagSession + "}"

func (a *Adapter) ListWindows(ctx context.Context) ([]core.TmuxWindow, error) {
	out, err := a.run(ctx, "list-windows", "-a", "-F", listFormat)
	var te *Error
	if errors.As(err, &te) && isNoServer(te) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseListWindows(out), nil
}

// isNoServer recognizes "the tmux server is not running", whose message
// differs across tmux versions ("no server running on ..." vs "error
// connecting to ... (No such file or directory)").
func isNoServer(e *Error) bool {
	s := e.Stderr
	return strings.Contains(s, "no server running") || strings.Contains(s, "error connecting to")
}

func parseListWindows(out string) []core.TmuxWindow {
	var wins []core.TmuxWindow
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 8 {
			continue
		}
		pid, _ := strconv.Atoi(f[4])
		wins = append(wins, core.TmuxWindow{
			SessionID: f[0], SessionName: f[1], WindowID: f[2], WindowName: f[3],
			PanePID: pid, PaneDead: f[5] == "1", WorkspaceID: f[6], DmuxSession: f[7],
		})
	}
	return wins
}

func (a *Adapter) KillWindow(ctx context.Context, windowID string) error {
	if !strings.HasPrefix(windowID, "@") {
		return fmt.Errorf("KillWindow: %q is not a window id", windowID)
	}
	_, err := a.run(ctx, "kill-window", "-t", windowID)
	return err
}

func (a *Adapter) InsideTmux() bool {
	lookup := a.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	v, ok := lookup("TMUX")
	return ok && v != ""
}

func (a *Adapter) SwitchClient(ctx context.Context, t core.TmuxTarget) error {
	if _, err := a.run(ctx, "switch-client", "-t", t.SessionID); err != nil {
		return err
	}
	_, err := a.run(ctx, "select-window", "-t", t.WindowID)
	return err
}

// Attach selects the window then execs `tmux attach-session`, handing the
// TTY to tmux. It only returns on failure.
func (a *Adapter) Attach(t core.TmuxTarget) error {
	if _, err := a.run(context.Background(), "select-window", "-t", t.WindowID); err != nil {
		return err
	}
	path, err := exec.LookPath(a.bin())
	if err != nil {
		return err
	}
	argv := append([]string{path}, a.baseArgs()...)
	argv = append(argv, "attach-session", "-t", t.SessionID)
	return syscall.Exec(path, argv, os.Environ())
}

// ShellJoin quotes argv for tmux's shell-command argument (run via sh -c).
func ShellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !isShellSafe(r) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

func isShellSafe(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '-', r == '_', r == '.', r == '/', r == '=', r == ':', r == '@':
		return true
	}
	return false
}
