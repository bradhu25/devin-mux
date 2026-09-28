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

// Tag names for tmux user options that give managed targets a positive
// identity independent of renamable names and server-lifetime ids.
const (
	TagWorkspace = "@dmux_workspace" // on the tmux session: Workspace.ID
	TagSession   = "@dmux_session"   // on the tmux window: Session.ID
)

// TmuxWindow is one window on the tmux server, with dmux tags resolved.
type TmuxWindow struct {
	SessionID   string // $N
	SessionName string
	WindowID    string // @N
	WindowName  string
	PanePID     int
	PaneDead    bool
	WorkspaceID string // value of TagWorkspace on the session, "" if untagged
	DmuxSession string // value of TagSession on the window, "" if untagged
}

// SpawnWindowOpts describes a window to create for a dmux session.
type SpawnWindowOpts struct {
	SessionName string            // tmux session to create or reuse (dmux-<workspace>)
	WorkspaceID string            // stamped as TagWorkspace when the session is created
	WindowName  string            // display only
	SessionID   string            // dmux Session.ID; stamped as TagSession on the window
	Cwd         string            // working directory for the command
	Env         map[string]string // environment for the command (e.g. DMUX_SESSION_ID)
	Argv        []string          // command to run in the window
}

// Tmux is the port for the terminal multiplexer. The adapter shares the
// user's tmux server (a separate socket would break switch-client) and only
// ever acts on windows carrying dmux tags.
type Tmux interface {
	// Available reports whether tmux can be invoked.
	Available(ctx context.Context) error
	// SpawnWindow creates the session if missing (tagging it), adds a window
	// running Argv with remain-on-exit set, tags the window, and returns the
	// stable target ids.
	SpawnWindow(ctx context.Context, opts SpawnWindowOpts) (TmuxTarget, error)
	// ListWindows returns every window on the server with tags resolved.
	// Callers filter to DmuxSession != "" before acting on anything.
	ListWindows(ctx context.Context) ([]TmuxWindow, error)
	// KillWindow kills a window by @N id.
	KillWindow(ctx context.Context, windowID string) error
	// InsideTmux reports whether the current process runs inside a tmux
	// client ($TMUX set).
	InsideTmux() bool
	// SwitchClient moves the current client to the window (inside tmux only).
	SwitchClient(ctx context.Context, target TmuxTarget) error
	// Attach replaces the current process with a tmux client attached to
	// the target (outside tmux only). On success it never returns.
	Attach(target TmuxTarget) error
}

// Store is the port for persisted State. Read returns an atomic snapshot
// without locking; Update runs fn in an exclusive read-modify-write
// transaction and persists only if fn returns nil.
type Store interface {
	Read() (*State, error)
	Update(ctx context.Context, fn func(*State) error) error
}
