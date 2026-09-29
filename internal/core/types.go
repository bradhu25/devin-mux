// Package core holds the orchestration logic and domain model for dmux.
// It depends only on the standard library and the port interfaces in
// ports.go; all interaction with git, tmux, Devin, and the filesystem goes
// through adapters injected at startup.
//
// Four identities, four lifetimes (see PLAN.md): a Workspace (dmux id), a
// tmux window (@N + tag), an OS process (pid), and a Devin conversation
// (slug id) are different things. Never let one stand in for another.
package core

import "time"

// WorkspaceStatus is the saga state of a workspace or session. Anything
// other than Ready is transitional or failed and is reconciled by `dmux
// doctor`.
type WorkspaceStatus string

const (
	WorkspaceCreating WorkspaceStatus = "creating"
	WorkspaceReady    WorkspaceStatus = "ready"
	WorkspaceDeleting WorkspaceStatus = "deleting"
	WorkspaceFailed   WorkspaceStatus = "failed"
)

// Workspace is the user's logical unit of work (a project, workstream,
// feature): a name plus the set of source repos it spans. It owns no
// worktrees itself; every Session spawned in it gets its own worktree and
// branch per repo, so sessions are isolated from each other by default.
type Workspace struct {
	ID        string          `json:"id"`   // stable, e.g. ws_a91c3f
	Name      string          `json:"name"` // display name; renamable without cascading
	Root      string          `json:"root"` // parent dir of session dirs. Named at creation, never moved.
	Repos     []RepoRef       `json:"repos"`
	Status    WorkspaceStatus `json:"status"`
	CreatedAt time.Time       `json:"createdAt"`
}

// RepoRef is one source repo a workspace spans and where sessions branch
// from in it.
type RepoRef struct {
	Name       string `json:"name"`       // subdirectory name inside each session dir
	SourcePath string `json:"sourcePath"` // original checkout; worktree ops run against it
	BaseRef    string `json:"baseRef"`    // ref new session branches start from ("HEAD" = the repo's current HEAD at spawn)
}

// WorkspaceRepo is one repo's worktree inside a session dir.
type WorkspaceRepo struct {
	Name          string `json:"name"`          // subdirectory name under the session Root
	SourcePath    string `json:"sourcePath"`    // original checkout; worktree ops run against it
	WorktreePath  string `json:"worktreePath"`  // Root/Name
	Branch        string `json:"branch"`        // branch checked out in the worktree
	BaseRef       string `json:"baseRef"`       // commit the branch was created from (merge-back helpers)
	CreatedBranch bool   `json:"createdBranch"` // dmux created Branch -> eligible for `git branch -d` on cleanup. Never delete pre-existing branches.
}

// Lifecycle is the OS-process dimension of a session's state.
type Lifecycle string

const (
	LifecycleStarting Lifecycle = "starting"
	LifecycleRunning  Lifecycle = "running"
	LifecycleExited   Lifecycle = "exited"
	LifecycleFailed   Lifecycle = "failed"
)

// Activity is the agent dimension of a session's state, orthogonal to
// Lifecycle. A session can be running+idle, running+awaiting-approval, or
// exited+unknown. Collapse for display only, never in storage.
type Activity string

const (
	ActivityWorking          Activity = "working"
	ActivityIdle             Activity = "idle"
	ActivityAwaitingApproval Activity = "awaiting-approval"
	// ActivityProbablyIdle is reconciler-derived: alive, no tool in flight,
	// silent past StaleAfter. Almost always a Ctrl-C'd turn, which Devin
	// does not report; the question mark in its display label is honest.
	ActivityProbablyIdle Activity = "probably-idle"
	ActivityUnknown      Activity = "unknown"
)

// TmuxTarget identifies where a session runs. IDs ($N/@N) are stable for
// the tmux server's lifetime; names are display-only and user-renamable.
// The window carries user option @dmux_session=<Session.ID> for positive
// identification across server restarts.
type TmuxTarget struct {
	SessionName string `json:"sessionName"`
	SessionID   string `json:"sessionId"` // $N
	WindowID    string `json:"windowId"`  // @N
}

// Session is one Devin agent working on a task inside a workspace, with its
// own worktree and branch per repo under Root (Devin's cwd). Only facts
// dmux authored are stored; Lifecycle and Activity are derived on read from
// events + liveness and are not persisted.
type Session struct {
	ID             string          `json:"id"`          // stable, e.g. s_7f2e9a
	WorkspaceID    string          `json:"workspaceId"` // reference by ID, not name
	Task           string          `json:"task"`
	Root           string          `json:"root"`  // session dir; Devin's cwd; contains one worktree per repo
	Repos          []WorkspaceRepo `json:"repos"` // this session's worktrees (empty while creating)
	Status         WorkspaceStatus `json:"status"`
	SharedWith     string          `json:"sharedWith,omitempty"` // set when spawned with --in: this session works in that session's worktrees and owns none
	Tmux           TmuxTarget      `json:"tmux"`
	DevinSessionID string          `json:"devinSessionId,omitempty"` // captured from hook session_id; enables `devin -r`
	CreatedAt      time.Time       `json:"createdAt"`
}

// OwnsWorktrees reports whether this session's Repos are its own (as
// opposed to joined from another session via --in).
func (s Session) OwnsWorktrees() bool { return s.SharedWith == "" }

// EventType is the normalized event contract. It is independent of Devin's
// raw hook payloads so a future ACP adapter can emit the same events and
// the reducer/UI never change.
type EventType string

const (
	SessionStarted    EventType = "session_started"
	PromptSubmitted   EventType = "prompt_submitted"
	ToolStarted       EventType = "tool_started"
	ToolCompleted     EventType = "tool_completed"
	ApprovalRequested EventType = "approval_requested"
	ApprovalResolved  EventType = "approval_resolved" // reconciler-derived, never emitted by hooks
	TurnCompleted     EventType = "turn_completed"
	SessionEnded      EventType = "session_ended"
	ProcessExited     EventType = "process_exited" // from the `dmux run` launch wrapper
)

// ApprovalOutcome is Data["outcome"] on an ApprovalResolved event.
type ApprovalOutcome string

const (
	OutcomeApproved ApprovalOutcome = "approved"
	OutcomeDenied   ApprovalOutcome = "denied"
	OutcomeCanceled ApprovalOutcome = "canceled"
	OutcomeUnknown  ApprovalOutcome = "unknown"
)

// SessionEvent is one observation about a session, stored as a JSONL line.
type SessionEvent struct {
	EventID        string         `json:"eventId"`
	SessionID      string         `json:"sessionId"` // dmux Session.ID
	DevinSessionID string         `json:"devinSessionId,omitempty"`
	Type           EventType      `json:"type"`
	Timestamp      time.Time      `json:"timestamp"`
	Data           map[string]any `json:"data,omitempty"` // toolUseId, promptId, toolName, exitCode, source, outcome...
}

// State is the persisted document at ~/.devin-mux/state.json.
type State struct {
	Version    int         `json:"version"`
	Workspaces []Workspace `json:"workspaces"`
	Sessions   []Session   `json:"sessions"`
	// KnownRepos are source repos any workspace has ever used, so doctor can
	// find dmux/* branches left behind after the workspace is gone.
	KnownRepos []string `json:"knownRepos,omitempty"`
}

// RememberRepo records a source repo path once.
func (s *State) RememberRepo(path string) {
	for _, p := range s.KnownRepos {
		if p == path {
			return
		}
	}
	s.KnownRepos = append(s.KnownRepos, path)
}

// StateVersion is the current schema version of State. v1 (workspaces
// owned the worktrees) is migrated on read by MigrateV1.
const StateVersion = 2

// Workspace returns the workspace with the given ID, or nil.
func (s *State) Workspace(id string) *Workspace {
	for i := range s.Workspaces {
		if s.Workspaces[i].ID == id {
			return &s.Workspaces[i]
		}
	}
	return nil
}

// WorkspaceByName returns the workspace with the given display name, or nil.
// Names are unique among workspaces but are not stable identifiers.
func (s *State) WorkspaceByName(name string) *Workspace {
	for i := range s.Workspaces {
		if s.Workspaces[i].Name == name {
			return &s.Workspaces[i]
		}
	}
	return nil
}

// Session returns the session with the given ID, or nil.
func (s *State) Session(id string) *Session {
	for i := range s.Sessions {
		if s.Sessions[i].ID == id {
			return &s.Sessions[i]
		}
	}
	return nil
}

// Joiners returns sessions that share ownerID's worktrees (spawned --in).
func (s *State) Joiners(ownerID string) []Session {
	var out []Session
	for _, sess := range s.Sessions {
		if sess.SharedWith == ownerID {
			out = append(out, sess)
		}
	}
	return out
}

// SessionsIn returns all sessions belonging to a workspace.
func (s *State) SessionsIn(workspaceID string) []Session {
	var out []Session
	for _, sess := range s.Sessions {
		if sess.WorkspaceID == workspaceID {
			out = append(out, sess)
		}
	}
	return out
}
