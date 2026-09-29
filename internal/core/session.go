package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// SpawnInput is the request for SessionManager.Spawn.
type SpawnInput struct {
	Workspace      string // workspace name or id
	Task           string // initial prompt; also the window name (truncated)
	PermissionMode string
	Model          string
}

// SessionManager owns the session lifecycle: spawn, and later kill/resume.
type SessionManager struct {
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

// SpawnResult is what Spawn returns for rendering.
type SpawnResult struct {
	Session   Session
	Workspace Workspace
	// OtherRunning is the number of other sessions already recorded in the
	// same workspace. Isolation is workspace-level, so the CLI warns when >0.
	OtherRunning int
}

// Spawn creates a Devin session in a workspace:
//
//  1. Resolve and validate the workspace (must be ready).
//  2. Reserve the Session record (no tmux target yet).
//  3. Create the tagged tmux window running `dmux run -- devin ...` with
//     cwd = workspace root and DMUX_SESSION_ID in the environment.
//  4. Persist the tmux target.
//
// If step 3 fails the reservation is removed; if step 4 fails the window is
// killed so no untracked managed window survives.
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
	others := len(st.SessionsIn(ws.ID))

	// --- 2. reserve ----------------------------------------------------------
	sess := Session{WorkspaceID: ws.ID, Task: in.Task, CreatedAt: m.now()}
	if err := m.Store.Update(ctx, func(st *State) error {
		if w := st.Workspace(ws.ID); w == nil || w.Status != WorkspaceReady {
			return fmt.Errorf("%w: %s", ErrWorkspaceNotReady, ws.Name)
		}
		sess.ID = NewUniqueID(NewSessionID, func(id string) bool { return st.Session(id) != nil })
		st.Sessions = append(st.Sessions, sess)
		return nil
	}); err != nil {
		return nil, err
	}
	unreserve := func() {
		_ = m.Store.Update(ctx, func(st *State) error {
			for i := range st.Sessions {
				if st.Sessions[i].ID == sess.ID {
					st.Sessions = append(st.Sessions[:i], st.Sessions[i+1:]...)
					break
				}
			}
			return nil
		})
	}

	// --- 3. tmux window ------------------------------------------------------
	argv := append([]string{m.DmuxBin, "run", "--session", sess.ID, "--dir", ws.Root, "--"},
		m.Devin.LaunchArgs(LaunchSpec{Prompt: in.Task, PermissionMode: in.PermissionMode, Model: in.Model})...)
	target, err := m.Tmux.SpawnWindow(ctx, SpawnWindowOpts{
		SessionName: m.prefix() + ws.Name,
		WorkspaceID: ws.ID,
		WindowName:  WindowName(in.Task, sess.ID),
		SessionID:   sess.ID,
		Cwd:         ws.Root,
		Env:         map[string]string{"DMUX_SESSION_ID": sess.ID},
		Argv:        argv,
	})
	if err != nil {
		unreserve()
		return nil, fmt.Errorf("spawn tmux window: %w", err)
	}

	// --- 4. persist target ---------------------------------------------------
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
		unreserve()
		return nil, fmt.Errorf("record tmux target: %w", err)
	}
	return &SpawnResult{Session: sess, Workspace: *ws, OtherRunning: others}, nil
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
