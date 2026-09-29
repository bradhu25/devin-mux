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

// WorkspaceStatus is the saga state of a workspace. Anything other than
// Ready is transitional or failed and is reconciled by `dmux doctor`.
type WorkspaceStatus string

const (
	WorkspaceCreating WorkspaceStatus = "creating"
	WorkspaceReady    WorkspaceStatus = "ready"
	WorkspaceDeleting WorkspaceStatus = "deleting"
	WorkspaceFailed   WorkspaceStatus = "failed"
)

// Workspace is a named unit containing git worktrees of 1..N repos.
// Isolation is workspace-level, not session-level: sessions within one
// workspace share files and branches.
type Workspace struct {
	ID        string          `json:"id"`   // stable, e.g. ws_a91c3f
	Name      string          `json:"name"` // display name; renamable without cascading
	Root      string          `json:"root"` // physical dir; Devin's cwd. Named at creation, never moved.
	Repos     []WorkspaceRepo `json:"repos"`
	Status    WorkspaceStatus `json:"status"`
	CreatedAt time.Time       `json:"createdAt"`
}

// WorkspaceRepo is one repo's worktree inside a workspace.
type WorkspaceRepo struct {
	Name          string `json:"name"`          // subdirectory name under Root
	SourcePath    string `json:"sourcePath"`    // original checkout; worktree ops run against it
	WorktreePath  string `json:"worktreePath"`  // Root/Name
	Branch        string `json:"branch"`        // branch checked out in the worktree
	BaseRef       string `json:"baseRef"`       // ref the branch was created from (merge-back helpers)
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

// Session is one Devin CLI session scoped to a task inside a workspace.
// Only facts dmux authored are stored; Lifecycle and Activity are derived
// on read from events + liveness and are not persisted.
type Session struct {
	ID             string     `json:"id"`          // stable, e.g. s_7f2e9a
	WorkspaceID    string     `json:"workspaceId"` // reference by ID, not name
	Task           string     `json:"task"`
	Tmux           TmuxTarget `json:"tmux"`
	DevinSessionID string     `json:"devinSessionId,omitempty"` // captured from hook session_id; enables `devin -r`
	CreatedAt      time.Time  `json:"createdAt"`
}

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
}

// StateVersion is the current schema version of State.
const StateVersion = 1

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
