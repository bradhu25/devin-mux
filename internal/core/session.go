package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// SpawnInput is the request for SessionManager.Spawn.
type SpawnInput struct {
	Workspace      string // workspace name or id
	Task           string // initial prompt; also the window name and branch slug
	PermissionMode string
	Model          string
	// Branch overrides the default dmux/<ws>/<slug> for every repo. An
	// existing branch is checked out; a new one is created from the base.
	Branch string
	// In joins an existing session's worktrees instead of creating new
	// ones (explicit shared mode). Session id, prefix, or task text.
	In string
}

// SessionManager owns the session lifecycle: spawn (incl. the worktree
// saga), kill, resume, and removal.
type SessionManager struct {
	Git   Git
	Tmux  Tmux
	Devin Devin
	Store Store
	// Proc and Events are required by Kill and Resume.
	Proc   Proc
	Events EventLog
	// DmuxBin is the absolute path to the dmux binary, used to run the
	// `dmux run` wrapper inside the tmux window.
	DmuxBin string
	// TmuxSessionPrefix defaults to "dmux-".
	TmuxSessionPrefix string
	Now               func() time.Time
}

// ErrWorkspaceNotFound is returned when the workspace name/id is unknown.
var ErrWorkspaceNotFound = errors.New("workspace not found")

// ErrWorkspaceNotReady is returned when the workspace is mid-saga or failed.
var ErrWorkspaceNotReady = errors.New("workspace is not ready")

// SpawnError wraps a failed spawn. Rollback reports whether compensation
// succeeded; if false, `dmux doctor` must finish cleanup.
type SpawnError struct {
	Step     string
	Cause    error
	Rollback error
}

func (e *SpawnError) Error() string {
	msg := fmt.Sprintf("spawn failed at %s: %v", e.Step, e.Cause)
	if e.Rollback != nil {
		msg += fmt.Sprintf(" (rollback incomplete: %v; run `dmux doctor`)", e.Rollback)
	}
	return msg
}

func (e *SpawnError) Unwrap() error { return e.Cause }

// SpawnResult is what Spawn returns for rendering.
type SpawnResult struct {
	Session   Session
	Workspace Workspace
	// JoinedSession is set when In was used: the owner whose worktrees this
	// session shares.
	JoinedSession *Session
}

// Spawn creates a Devin agent in a workspace with its own worktree and
// branch per repo (saga):
//
//  1. Validate: workspace ready; plan worktrees/branches (no side effects).
//  2. Reserve Session{status: creating}.
//  3. mkdir root; git worktree add per repo, recording progress.
//  4. Write the session AGENTS.md map.
//  5. Create the tagged tmux window (cwd = session root).
//  6. Persist the tmux target; mark ready.
//
// On failure after 2, worktrees are removed in reverse order, dmux-created
// branches deleted with -d, the dir removed, and the record dropped when
// rollback succeeded or left as failed for `dmux doctor` when it did not.
//
// With In set, no worktrees are created: the new session shares the target
// session's Root and Repos (SharedWith) — explicit, opt-in sharing.
func (m *SessionManager) Spawn(ctx context.Context, in SpawnInput) (*SpawnResult, error) {
	if m.DmuxBin == "" {
		return nil, errors.New("spawn: dmux binary path not configured")
	}
	if err := m.Tmux.Available(ctx); err != nil {
		return nil, err
	}
	if err := m.Devin.Available(ctx); err != nil {
		return nil, err
	}
	st, err := m.Store.Read()
	if err != nil {
		return nil, err
	}
	ws := st.WorkspaceByName(in.Workspace)
	if ws == nil {
		ws = st.Workspace(in.Workspace)
	}
	if ws == nil {
		return nil, fmt.Errorf("%w: %s", ErrWorkspaceNotFound, in.Workspace)
	}
	if ws.Status != WorkspaceReady {
		return nil, fmt.Errorf("%w: %s is %s (run `dmux doctor`)", ErrWorkspaceNotReady, ws.Name, ws.Status)
	}
	if in.In != "" {
		return m.spawnJoined(ctx, st, ws, in)
	}
	if m.Git == nil {
		return nil, errors.New("spawn: Git port required")
	}

	// --- 2. reserve (the id is needed for the branch/dir names) --------------
	sess := Session{WorkspaceID: ws.ID, Task: in.Task, Status: WorkspaceCreating, CreatedAt: m.now()}
	if err := m.Store.Update(ctx, func(st *State) error {
		if w := st.Workspace(ws.ID); w == nil || w.Status != WorkspaceReady {
			return fmt.Errorf("%w: %s", ErrWorkspaceNotReady, ws.Name)
		}
		sess.ID = NewUniqueID(NewSessionID, func(id string) bool { return st.Session(id) != nil })
		sess.Root = filepath.Join(ws.Root, sess.ID)
		st.Sessions = append(st.Sessions, sess)
		return nil
	}); err != nil {
		return nil, err
	}
	persist := func(mut func(*Session)) error {
		return m.Store.Update(ctx, func(st *State) error {
			s := st.Session(sess.ID)
			if s == nil {
				return errors.New("session record vanished during spawn")
			}
			mut(s)
			return nil
		})
	}
	fail := func(step string, cause error) error {
		rb := rollbackWorktrees(ctx, m.Git, sess.Root, sess.Repos)
		if rb == nil {
			_ = m.Store.Update(ctx, func(st *State) error { st.removeSession(sess.ID); return nil })
		} else {
			_ = persist(func(s *Session) { s.Repos, s.Status = sess.Repos, WorkspaceFailed })
		}
		return &SpawnError{Step: step, Cause: cause, Rollback: rb}
	}

	// --- 1. plan (after reserve only because names embed the id; no side effects yet)
	plans, err := planWorktrees(ctx, m.Git, ws, sess.Root, in.Task, sess.ID, in.Branch)
	if err != nil {
		_ = m.Store.Update(ctx, func(st *State) error { st.removeSession(sess.ID); return nil })
		return nil, err
	}

	// --- 3. worktrees ----------------------------------------------------------
	if err := os.MkdirAll(sess.Root, 0o755); err != nil {
		return nil, fail("mkdir session dir", err)
	}
	for _, p := range plans {
		if err := m.Git.WorktreeAdd(ctx, p.repo.SourcePath, p.repo.WorktreePath, p.opts); err != nil {
			return nil, fail("worktree add "+p.repo.Name, err)
		}
		sess.Repos = append(sess.Repos, p.repo)
		if err := persist(func(s *Session) { s.Repos = sess.Repos }); err != nil {
			return nil, fail("record repo "+p.repo.Name, err)
		}
	}

	// --- 4. map ------------------------------------------------------------------
	if err := os.WriteFile(filepath.Join(sess.Root, "AGENTS.md"), []byte(SessionAgentsMD(ws, &sess)), 0o644); err != nil {
		return nil, fail("write AGENTS.md", err)
	}

	// --- 5. tmux window ------------------------------------------------------------
	target, err := m.spawnWindow(ctx, ws, &sess, LaunchSpec{Prompt: in.Task, PermissionMode: in.PermissionMode, Model: in.Model})
	if err != nil {
		return nil, fail("spawn tmux window", err)
	}

	// --- 6. persist target, ready -------------------------------------------------
	sess.Tmux, sess.Status = target, WorkspaceReady
	if err := persist(func(s *Session) { s.Tmux, s.Status = target, WorkspaceReady }); err != nil {
		_ = m.Tmux.KillWindow(ctx, target.WindowID)
		return nil, fail("record tmux target", err)
	}
	return &SpawnResult{Session: sess, Workspace: *ws}, nil
}

// spawnJoined creates a session that works in another session's worktrees.
func (m *SessionManager) spawnJoined(ctx context.Context, st *State, ws *Workspace, in SpawnInput) (*SpawnResult, error) {
	owner, err := findSession(st, in.In)
	if err != nil {
		return nil, err
	}
	if owner.WorkspaceID != ws.ID {
		return nil, fmt.Errorf("session %s is not in workspace %s", owner.ID, ws.Name)
	}
	if !owner.OwnsWorktrees() {
		if o := st.Session(owner.SharedWith); o != nil {
			owner = o // join the real owner
		}
	}
	if owner.Status != WorkspaceReady || len(owner.Repos) == 0 {
		return nil, fmt.Errorf("session %s has no ready worktrees to share (status %s)", owner.ID, owner.Status)
	}
	sess := Session{WorkspaceID: ws.ID, Task: in.Task, Root: owner.Root, Repos: owner.Repos, SharedWith: owner.ID, Status: WorkspaceReady, CreatedAt: m.now()}
	if err := m.Store.Update(ctx, func(st *State) error {
		if o := st.Session(owner.ID); o == nil || o.Status != WorkspaceReady {
			return fmt.Errorf("session %s is no longer available to join", owner.ID)
		}
		sess.ID = NewUniqueID(NewSessionID, func(id string) bool { return st.Session(id) != nil })
		st.Sessions = append(st.Sessions, sess)
		return nil
	}); err != nil {
		return nil, err
	}
	target, err := m.spawnWindow(ctx, ws, &sess, LaunchSpec{Prompt: in.Task, PermissionMode: in.PermissionMode, Model: in.Model})
	if err != nil {
		_ = m.Store.Update(ctx, func(st *State) error { st.removeSession(sess.ID); return nil })
		return nil, fmt.Errorf("spawn tmux window: %w", err)
	}
	sess.Tmux = target
	if err := m.Store.Update(ctx, func(st *State) error {
		s := st.Session(sess.ID)
		if s == nil {
			return errors.New("session record vanished during spawn")
		}
		s.Tmux = target
		return nil
	}); err != nil {
		_ = m.Tmux.KillWindow(ctx, target.WindowID)
		_ = m.Store.Update(ctx, func(st *State) error { st.removeSession(sess.ID); return nil })
		return nil, fmt.Errorf("record tmux target: %w", err)
	}
	o := *owner
	return &SpawnResult{Session: sess, Workspace: *ws, JoinedSession: &o}, nil
}

// spawnWindow launches `dmux run -- devin ...` in a tagged tmux window with
// cwd = the session root.
func (m *SessionManager) spawnWindow(ctx context.Context, ws *Workspace, sess *Session, spec LaunchSpec) (TmuxTarget, error) {
	argv := append([]string{m.DmuxBin, "run", "--session", sess.ID, "--dir", sess.Root, "--"}, m.Devin.LaunchArgs(spec)...)
	return m.Tmux.SpawnWindow(ctx, SpawnWindowOpts{
		SessionName: m.prefix() + ws.Name,
		WorkspaceID: ws.ID,
		WindowName:  WindowName(sess.Task, sess.ID),
		SessionID:   sess.ID,
		Cwd:         sess.Root,
		Env:         map[string]string{"DMUX_SESSION_ID": sess.ID},
		Argv:        argv,
	})
}

// WindowName derives a short, single-line tmux window name from the task,
// falling back to the session id. Display only; never used as a target.
func WindowName(task, sessionID string) string {
	const maxLen = 24
	fields := strings.FieldsFunc(task, func(r rune) bool { return unicode.IsSpace(r) || r == ':' || r == '.' })
	name := strings.Join(fields, " ")
	if name == "" {
		return sessionID
	}
	if len(name) > maxLen {
		name = strings.TrimSpace(name[:maxLen-1]) + "…"
	}
	return name
}

func (m *SessionManager) prefix() string {
	if m.TmuxSessionPrefix != "" {
		return m.TmuxSessionPrefix
	}
	return "dmux-"
}

func (m *SessionManager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UTC()
}
