package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// fakeGit is an in-memory core.Git. It records calls and can be told to
// fail specific operations, which is how saga rollback is tested.
type fakeGit struct {
	mu       sync.Mutex
	repos    map[string]map[string]bool // toplevel -> branches
	worktree map[string]string          // worktree path -> repo
	checked  map[string]string          // worktree path -> branch checked out there
	calls    []string
	failOn   map[string]error // "op:arg" -> error
}

// newFakeGit creates repos whose main checkout has "main" checked out, as
// real git does.
func newFakeGit(repos ...string) *fakeGit {
	f := &fakeGit{repos: map[string]map[string]bool{}, worktree: map[string]string{}, checked: map[string]string{}, failOn: map[string]error{}}
	for _, r := range repos {
		f.repos[r] = map[string]bool{"main": true}
		f.worktree[r] = r
		f.checked[r] = "main"
	}
	return f
}

func (f *fakeGit) record(op string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := op
	if len(args) > 0 {
		key += ":" + args[0]
	}
	f.calls = append(f.calls, key)
	if err, ok := f.failOn[key]; ok {
		return err
	}
	return nil
}

func (f *fakeGit) IsRepo(_ context.Context, path string) (string, bool, error) {
	if err := f.record("isrepo", path); err != nil {
		return "", false, err
	}
	// Any path inside a known repo resolves to that repo.
	for top := range f.repos {
		if path == top || len(path) > len(top) && path[:len(top)+1] == top+"/" {
			return top, true, nil
		}
	}
	return "", false, nil
}

func (f *fakeGit) BranchExists(_ context.Context, repo, branch string) (bool, error) {
	if err := f.record("branchexists", branch); err != nil {
		return false, err
	}
	return f.repos[repo][branch], nil
}

func (f *fakeGit) ResolveRef(_ context.Context, repo, ref string) (string, error) {
	if err := f.record("resolveref", ref); err != nil {
		return "", err
	}
	if ref == "HEAD" || f.repos[repo][ref] {
		return "0123456789abcdef0123456789abcdef01234567", nil
	}
	return "", fmt.Errorf("unknown ref %q", ref)
}

func (f *fakeGit) WorktreeAdd(_ context.Context, repo, path string, opts WorktreeAddOpts) error {
	if err := f.record("worktreeadd", path); err != nil {
		return err
	}
	if opts.NewBranch != "" {
		if f.repos[repo][opts.NewBranch] {
			return errors.New("branch exists")
		}
		f.repos[repo][opts.NewBranch] = true
	} else if !f.repos[repo][opts.ExistingBranch] {
		return errors.New("no such branch")
	}
	branch := opts.NewBranch
	if branch == "" {
		branch = opts.ExistingBranch
	}
	for p, b := range f.checked {
		if f.worktree[p] == repo && b == branch {
			return fmt.Errorf("fatal: '%s' is already checked out at '%s'", branch, p)
		}
	}
	f.worktree[path] = repo
	f.checked[path] = branch
	return os.MkdirAll(path, 0o755) // mimic git creating the directory
}

func (f *fakeGit) WorktreeRemove(_ context.Context, _ string, path string, _ bool) error {
	if err := f.record("worktreeremove", path); err != nil {
		return err
	}
	delete(f.worktree, path)
	delete(f.checked, path)
	return os.RemoveAll(path)
}

func (f *fakeGit) WorktreeList(_ context.Context, repo string) ([]Worktree, error) {
	if err := f.record("worktreelist", repo); err != nil {
		return nil, err
	}
	var out []Worktree
	for p, r := range f.worktree {
		if r == repo {
			out = append(out, Worktree{Path: p, Branch: f.checked[p]})
		}
	}
	return out, nil
}

func (f *fakeGit) WorktreePrune(_ context.Context, repo string) error { return f.record("prune", repo) }

func (f *fakeGit) BranchDeleteSafe(_ context.Context, repo, branch string) error {
	if err := f.record("branchdelete", branch); err != nil {
		return err
	}
	delete(f.repos[repo], branch)
	return nil
}

func (f *fakeGit) StatusPorcelain(_ context.Context, wt string) ([]string, error) {
	return nil, f.record("status", wt)
}

func (f *fakeGit) hasCall(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == key {
			return true
		}
	}
	return false
}

// fakeStore is an in-memory core.Store.
type fakeStore struct {
	mu    sync.Mutex
	state State
	// failNext, if set, makes the next Update fail once.
	failNext error
}

func (s *fakeStore) Read() (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.state
	cp.Workspaces = append([]Workspace(nil), s.state.Workspaces...)
	cp.Sessions = append([]Session(nil), s.state.Sessions...)
	return &cp, nil
}

func (s *fakeStore) Update(_ context.Context, fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext != nil {
		err := s.failNext
		s.failNext = nil
		return err
	}
	cp := s.state
	if err := fn(&cp); err != nil {
		return err
	}
	s.state = cp
	return nil
}

func (s *fakeStore) workspace(id string) *Workspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Workspace(id)
}

// dirExists is a small test helper.
func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func fileExists(p string) bool {
	st, err := os.Stat(filepath.Clean(p))
	return err == nil && !st.IsDir()
}
