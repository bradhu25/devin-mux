package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

// Tier 2 integration tests: real git against throwaway repos in t.TempDir().

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// initRepo creates a repo with one commit on main and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", dir},
		{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestIsRepo(t *testing.T) {
	requireGit(t)
	a := &Adapter{}
	ctx := context.Background()
	repo := initRepo(t)

	top, ok, err := a.IsRepo(ctx, repo)
	if err != nil || !ok {
		t.Fatalf("IsRepo(repo) = %v %v", ok, err)
	}
	if real, _ := filepath.EvalSymlinks(repo); real != top {
		t.Fatalf("toplevel %q != %q", top, real)
	}
	_, ok, err = a.IsRepo(ctx, t.TempDir())
	if err != nil || ok {
		t.Fatalf("IsRepo(non-repo) = %v %v, want false nil", ok, err)
	}
}

func TestBranchAndRef(t *testing.T) {
	requireGit(t)
	a := &Adapter{}
	ctx := context.Background()
	repo := initRepo(t)

	if ok, err := a.BranchExists(ctx, repo, "main"); err != nil || !ok {
		t.Fatalf("main should exist: %v %v", ok, err)
	}
	if ok, err := a.BranchExists(ctx, repo, "nope"); err != nil || ok {
		t.Fatalf("nope should not exist: %v %v", ok, err)
	}
	sha, err := a.ResolveRef(ctx, repo, "main")
	if err != nil || len(sha) != 40 {
		t.Fatalf("ResolveRef(main) = %q %v", sha, err)
	}
	if _, err := a.ResolveRef(ctx, repo, "does-not-exist"); err == nil {
		t.Fatal("ResolveRef of missing ref must error")
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	requireGit(t)
	a := &Adapter{}
	ctx := context.Background()
	repo := initRepo(t)
	wtPath := filepath.Join(t.TempDir(), "ws", "api") // parent dir does not exist yet

	// Add with new branch from main.
	if err := a.WorktreeAdd(ctx, repo, wtPath, core.WorktreeAddOpts{NewBranch: "dmux/feature-x", BaseRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := a.BranchExists(ctx, repo, "dmux/feature-x"); !ok {
		t.Fatal("branch not created")
	}
	list, err := a.WorktreeList(ctx, repo)
	if err != nil || len(list) != 2 {
		t.Fatalf("WorktreeList = %+v %v", list, err)
	}
	realWT, _ := filepath.EvalSymlinks(wtPath)
	if list[1].Path != realWT || list[1].Branch != "dmux/feature-x" || list[1].Locked || list[1].Detached {
		t.Fatalf("worktree entry: %+v", list[1])
	}

	// Source checkout untouched.
	if st, _ := a.StatusPorcelain(ctx, repo); len(st) != 0 {
		t.Fatalf("source repo dirty: %v", st)
	}

	// Dirty worktree: clean status first, then dirty.
	if st, err := a.StatusPorcelain(ctx, wtPath); err != nil || len(st) != 0 {
		t.Fatalf("fresh worktree should be clean: %v %v", st, err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := a.StatusPorcelain(ctx, wtPath)
	if len(st) != 1 || !strings.HasPrefix(st[0], "??") {
		t.Fatalf("expected one untracked entry, got %v", st)
	}

	// Remove without force must refuse a dirty worktree; with force succeeds.
	err = a.WorktreeRemove(ctx, repo, wtPath, false)
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("expected git error removing dirty worktree, got %v", err)
	}
	if err := a.WorktreeRemove(ctx, repo, wtPath, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatal("worktree dir still exists after remove")
	}

	// Branch is fully merged (no commits) -> -d succeeds.
	if err := a.BranchDeleteSafe(ctx, repo, "dmux/feature-x"); err != nil {
		t.Fatal(err)
	}
	if err := a.WorktreePrune(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if list, _ := a.WorktreeList(ctx, repo); len(list) != 1 {
		t.Fatalf("expected only main worktree after prune, got %d", len(list))
	}
}

func TestWorktreeAdd_ExistingBranchAndValidation(t *testing.T) {
	requireGit(t)
	a := &Adapter{}
	ctx := context.Background()
	repo := initRepo(t)

	// Existing branch path.
	first := filepath.Join(t.TempDir(), "first")
	if err := a.WorktreeAdd(ctx, repo, first, core.WorktreeAddOpts{NewBranch: "shared", BaseRef: "main"}); err != nil {
		t.Fatal(err)
	}
	_ = a.WorktreeRemove(ctx, repo, first, false)
	second := filepath.Join(t.TempDir(), "second")
	if err := a.WorktreeAdd(ctx, repo, second, core.WorktreeAddOpts{ExistingBranch: "shared"}); err != nil {
		t.Fatalf("checkout existing branch: %v", err)
	}

	// Option validation happens before touching git.
	for _, opts := range []core.WorktreeAddOpts{{}, {NewBranch: "a"}, {NewBranch: "a", BaseRef: "main", ExistingBranch: "b"}} {
		if err := a.WorktreeAdd(ctx, repo, filepath.Join(t.TempDir(), "x"), opts); err == nil {
			t.Errorf("opts %+v should be rejected", opts)
		}
	}
}

func TestBranchDeleteSafe_RefusesUnmerged(t *testing.T) {
	requireGit(t)
	a := &Adapter{}
	ctx := context.Background()
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := a.WorktreeAdd(ctx, repo, wt, core.WorktreeAddOpts{NewBranch: "feat", BaseRef: "main"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", wt, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "unmerged")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if err := a.WorktreeRemove(ctx, repo, wt, false); err != nil {
		t.Fatal(err)
	}
	err := a.BranchDeleteSafe(ctx, repo, "feat")
	var ge *Error
	if !errors.As(err, &ge) || !strings.Contains(ge.Stderr, "not fully merged") {
		t.Fatalf("expected 'not fully merged' refusal, got %v", err)
	}
	if ok, _ := a.BranchExists(ctx, repo, "feat"); !ok {
		t.Fatal("unmerged branch must survive BranchDeleteSafe")
	}
}

func TestLockedWorktreeRefusesRemove(t *testing.T) {
	requireGit(t)
	a := &Adapter{}
	ctx := context.Background()
	repo := initRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := a.WorktreeAdd(ctx, repo, wt, core.WorktreeAddOpts{NewBranch: "feat", BaseRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", repo, "worktree", "lock", "--reason", "keep", wt).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	list, _ := a.WorktreeList(ctx, repo)
	if len(list) != 2 || !list[1].Locked || list[1].LockMsg != "keep" {
		t.Fatalf("lock not reported: %+v", list)
	}
	if err := a.WorktreeRemove(ctx, repo, wt, false); err == nil {
		t.Fatal("removing a locked worktree must fail")
	}
}

func TestTimeoutIsEnforced(t *testing.T) {
	requireGit(t)
	// Point Bin at a script that sleeps; the adapter's timeout must fire.
	script := filepath.Join(t.TempDir(), "slowgit")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Bin: script, Timeout: 200 * time.Millisecond}
	start := time.Now()
	_, err := a.run(context.Background(), "", "status")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout did not cut the command short")
	}
}

func TestCommitsAhead(t *testing.T) {
	requireGit(t)
	a := &Adapter{}
	ctx := context.Background()
	repo := initRepo(t)
	base, _ := a.ResolveRef(ctx, repo, "main")
	wt := filepath.Join(t.TempDir(), "wt")
	if err := a.WorktreeAdd(ctx, repo, wt, core.WorktreeAddOpts{NewBranch: "feat", BaseRef: base}); err != nil {
		t.Fatal(err)
	}
	if n, err := a.CommitsAhead(ctx, wt, base); err != nil || n != 0 {
		t.Fatalf("fresh branch: %d %v", n, err)
	}
	for i := 0; i < 2; i++ {
		cmd := exec.CommandContext(ctx, "git", "-C", wt, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "c")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	if n, err := a.CommitsAhead(ctx, wt, base); err != nil || n != 2 {
		t.Fatalf("after 2 commits: %d %v", n, err)
	}
	if _, err := a.CommitsAhead(ctx, wt, "deadbeef"); err == nil {
		t.Fatal("unresolvable base must error")
	}
}
