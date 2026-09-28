// Package git implements core.Git by shelling out to the git binary.
// This is one of the only packages allowed to import os/exec.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

// DefaultTimeout bounds every git invocation so a hung git (credential
// helper, hook, network) can never hang dmux.
const DefaultTimeout = 30 * time.Second

// Adapter runs git commands. Zero value is usable.
type Adapter struct {
	// Bin is the git executable; defaults to "git" on PATH.
	Bin string
	// Timeout per invocation; defaults to DefaultTimeout.
	Timeout time.Duration
}

var _ core.Git = (*Adapter)(nil)

// Error is returned when git exits non-zero.
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
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

// run executes git with dir as -C and returns trimmed stdout.
func (a *Adapter) run(ctx context.Context, dir string, args ...string) (string, error) {
	bin := a.Bin
	if bin == "" {
		bin = "git"
	}
	timeout := a.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, bin, full...)
	// Never prompt, never block on optional locks (e.g. index refresh).
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
	// On timeout, kill the whole process group: git spawns children
	// (credential helpers, hooks) that would otherwise keep our stdout/stderr
	// pipes open and make Wait hang. WaitDelay is the backstop for anything
	// that escapes the group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", &Error{Args: args, ExitCode: ee.ExitCode(), Stderr: stderr.String()}
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func (a *Adapter) IsRepo(ctx context.Context, path string) (string, bool, error) {
	out, err := a.run(ctx, path, "rev-parse", "--show-toplevel")
	var ge *Error
	if errors.As(err, &ge) {
		return "", false, nil // not a repo (or bare): git exits 128
	}
	if err != nil {
		return "", false, err
	}
	return out, true, nil
}

func (a *Adapter) BranchExists(ctx context.Context, repo, branch string) (bool, error) {
	_, err := a.run(ctx, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	var ge *Error
	if errors.As(err, &ge) && ge.ExitCode == 1 {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (a *Adapter) ResolveRef(ctx context.Context, repo, ref string) (string, error) {
	out, err := a.run(ctx, repo, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q to a commit in %s: %w", ref, repo, err)
	}
	return out, nil
}

func (a *Adapter) WorktreeAdd(ctx context.Context, repo, path string, opts core.WorktreeAddOpts) error {
	args := []string{"worktree", "add"}
	switch {
	case opts.NewBranch != "" && opts.ExistingBranch != "":
		return errors.New("WorktreeAdd: NewBranch and ExistingBranch are mutually exclusive")
	case opts.NewBranch != "":
		if opts.BaseRef == "" {
			return errors.New("WorktreeAdd: BaseRef required with NewBranch")
		}
		args = append(args, "-b", opts.NewBranch, path, opts.BaseRef)
	case opts.ExistingBranch != "":
		args = append(args, path, opts.ExistingBranch)
	default:
		return errors.New("WorktreeAdd: one of NewBranch or ExistingBranch required")
	}
	_, err := a.run(ctx, repo, args...)
	return err
}

func (a *Adapter) WorktreeRemove(ctx context.Context, repo, path string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	_, err := a.run(ctx, repo, append(args, path)...)
	return err
}

func (a *Adapter) WorktreeList(ctx context.Context, repo string) ([]core.Worktree, error) {
	out, err := a.run(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktreeList(out), nil
}

func (a *Adapter) WorktreePrune(ctx context.Context, repo string) error {
	_, err := a.run(ctx, repo, "worktree", "prune")
	return err
}

func (a *Adapter) BranchDeleteSafe(ctx context.Context, repo, branch string) error {
	_, err := a.run(ctx, repo, "branch", "-d", branch)
	return err
}

func (a *Adapter) StatusPorcelain(ctx context.Context, worktree string) ([]string, error) {
	out, err := a.run(ctx, worktree, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// parseWorktreeList parses `git worktree list --porcelain`: stanzas
// separated by blank lines, each a sequence of "key[ value]" lines.
func parseWorktreeList(out string) []core.Worktree {
	var (
		result []core.Worktree
		cur    *core.Worktree
	)
	flush := func() {
		if cur != nil {
			result = append(result, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &core.Worktree{Path: val}
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "locked":
			if cur != nil {
				cur.Locked, cur.LockMsg = true, val
			}
		}
	}
	flush()
	return result
}
