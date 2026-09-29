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
	// CommitsAhead returns how many commits HEAD of worktree has that are
	// not reachable from baseRef (`rev-list --count base..HEAD`). An
	// unresolvable baseRef is an error.
	CommitsAhead(ctx context.Context, worktree, baseRef string) (int, error)
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
	// RenameSession renames a tmux session by $N id (display only; ids are
	// unaffected).
	RenameSession(ctx context.Context, sessionID, name string) error
	// RenameWindow renames a tmux window by @N id (display only).
	RenameWindow(ctx context.Context, windowID, name string) error
	// InsideTmux reports whether the current process runs inside a tmux
	// client ($TMUX set).
	InsideTmux() bool
	// SwitchClient moves the current client to the window (inside tmux only).
	SwitchClient(ctx context.Context, target TmuxTarget) error
	// Attach replaces the current process with a tmux client attached to
	// the target (outside tmux only). On success it never returns.
	Attach(target TmuxTarget) error
}

// LaunchSpec describes a Devin invocation to build.
type LaunchSpec struct {
	Prompt         string // optional initial prompt, passed after `--`
	ResumeID       string // optional Devin session id to resume (`-r`)
	PermissionMode string // optional --permission-mode value
	Model          string // optional --model value
}

// ToolCallOutcome is Devin's recorded final state of a tool call, read from
// its own session store. It resolves permission requests that hooks cannot
// observe (Spike 3: deny and cancel fire no hook).
type ToolCallOutcome struct {
	ToolUseID string
	Outcome   ApprovalOutcome // approved (ran), denied, canceled
}

// Devin is the port for the Devin CLI: how to invoke it and how to read
// what it knows. The adapter isolates the version-specific CLI contract.
type Devin interface {
	// Available reports whether the devin binary can be found.
	Available(ctx context.Context) error
	// LaunchArgs returns the argv (including the binary) that starts an
	// interactive Devin session per spec. It never includes a cwd; the
	// caller sets that.
	LaunchArgs(spec LaunchSpec) []string
	// ToolCallOutcomes returns the final outcome of the given tool calls in
	// a Devin session, for those that have one. Best-effort and read-only:
	// callers must treat an error as "unknown", never as a failure.
	ToolCallOutcomes(ctx context.Context, devinSessionID string, toolUseIDs []string) ([]ToolCallOutcome, error)
}

// Proc is the port for OS process control. dmux only ever signals pids it
// obtained from tagged tmux panes it created.
type Proc interface {
	// Terminate sends SIGTERM to the process group led by pid.
	Terminate(pid int) error
	// Kill sends SIGKILL to the process group led by pid.
	Kill(pid int) error
	// Alive reports whether pid still exists.
	Alive(pid int) bool
}

// EventLog is the port for reading a session's normalized events.
type EventLog interface {
	Read(sessionID string) ([]SessionEvent, error)
}

// Store is the port for persisted State. Read returns an atomic snapshot
// without locking; Update runs fn in an exclusive read-modify-write
// transaction and persists only if fn returns nil.
type Store interface {
	Read() (*State, error)
	Update(ctx context.Context, fn func(*State) error) error
}
