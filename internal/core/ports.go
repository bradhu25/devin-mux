package core

import "context"

// Worktree is one entry from `git worktree list --porcelain`.
type Worktree struct {
	Path     string // absolute path
	Head     string // commit sha
	Branch   string // short branch name; empty when detached
	Detached bool
	Locked   bool
	LockMsg  string
}

// Git is the port for everything the workspace sagas need from git. The
// adapter in internal/adapters/git implements it; tests use a fake.
// All operations are scoped to a repo path and must never prompt.
type Git interface {
	// IsRepo reports whether path is inside a git working tree and returns
	// its top-level directory.
	IsRepo(ctx context.Context, path string) (toplevel string, ok bool, err error)
	// BranchExists reports whether refs/heads/<branch> exists in repo.
	BranchExists(ctx context.Context, repo, branch string) (bool, error)
	// ResolveRef returns the commit sha that ref points to, or an error if
	// it cannot be resolved to a commit.
	ResolveRef(ctx context.Context, repo, ref string) (string, error)
	// WorktreeAdd creates a worktree at path. If newBranch is non-empty the
	// branch is created from baseRef (`-b`); otherwise existing branch
	// `branch` is checked out.
	WorktreeAdd(ctx context.Context, repo, path string, opts WorktreeAddOpts) error
	// WorktreeRemove removes a worktree. It fails on locked worktrees and,
	// unless force is set, on worktrees with uncommitted changes.
	WorktreeRemove(ctx context.Context, repo, path string, force bool) error
	// WorktreeList returns all worktrees of repo, including the main one.
	WorktreeList(ctx context.Context, repo string) ([]Worktree, error)
	// WorktreePrune removes stale administrative entries.
	WorktreePrune(ctx context.Context, repo string) error
	// BranchDeleteSafe runs `git branch -d` (never -D): it refuses to
	// delete a branch that is not fully merged.
	BranchDeleteSafe(ctx context.Context, repo, branch string) error
	// StatusPorcelain returns `git status --porcelain` lines for a worktree;
	// empty means clean.
	StatusPorcelain(ctx context.Context, worktree string) ([]string, error)
}

// WorktreeAddOpts controls WorktreeAdd. Exactly one of NewBranch or
// ExistingBranch must be set.
type WorktreeAddOpts struct {
	NewBranch      string // create this branch from BaseRef
	BaseRef        string // required with NewBranch
	ExistingBranch string // check out this existing branch
}

// Store is the port for persisted State. Read returns an atomic snapshot
// without locking; Update runs fn in an exclusive read-modify-write
// transaction and persists only if fn returns nil.
type Store interface {
	Read() (*State, error)
	Update(ctx context.Context, fn func(*State) error) error
}
