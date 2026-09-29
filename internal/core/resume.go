package core

import (
	"context"
	"errors"
	"fmt"
)

// ErrSessionRunning is returned when resume is asked for a session whose
// process is still alive; use `dmux jump` instead.
var ErrSessionRunning = errors.New("session is still running")

// ErrNoDevinSession is returned when a session never recorded a Devin
// conversation id, so there is nothing to resume.
var ErrNoDevinSession = errors.New("no Devin conversation id recorded for session")

// ResumeInput is the request for Resume.
type ResumeInput struct {
	Query          string // session id, prefix, or task text
	Prompt         string // optional prompt to submit on resume
	PermissionMode string
	Model          string
}

// ResumeResult is what Resume returns for rendering.
type ResumeResult struct {
	Session        Session
	Workspace      Workspace
	DevinSessionID string
	OldWindow      string // previous tmux window id, if any was closed
}

// Resume relaunches an exited session's Devin conversation:
//
//   - the conversation is identified by Session.DevinSessionID (captured
//     from the SessionStart hook); if it is missing, the event log is
//     consulted; if still missing, Resume fails. It never falls back to
//     `devin -c` — with several sessions per workspace that would resume
//     the wrong conversation (PLAN.md Resumption semantics).
//   - the process must not be running; a live session is jumped to, not
//     resumed.
//   - Devin is launched from the workspace root (Spike 4: resuming from a
//     different cwd rebinds the conversation's directory).
//   - a leftover held pane is closed and the record's tmux target replaced.
func (m *SessionManager) Resume(ctx context.Context, in ResumeInput) (*ResumeResult, error) {
	if m.Events == nil {
		return nil, errors.New("resume: Events port required")
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
	sess, err := findSession(st, in.Query)
	if err != nil {
		return nil, err
	}
	ws := st.Workspace(sess.WorkspaceID)
	if ws == nil {
		return nil, fmt.Errorf("%w: %s", ErrWorkspaceNotFound, sess.WorkspaceID)
	}
	if ws.Status != WorkspaceReady {
		return nil, fmt.Errorf("%w: %s is %s", ErrWorkspaceNotReady, ws.Name, ws.Status)
	}

	// Conversation id: record first, event log second.
	devinID := sess.DevinSessionID
	if devinID == "" {
		if evs, err := m.Events.Read(sess.ID); err == nil {
			devinID = Reduce(evs).DevinSessionID
		}
	}
	if devinID == "" {
		return nil, fmt.Errorf("%w %s (the hook that records it may not have been installed; run `dmux init`)", ErrNoDevinSession, sess.ID)
	}

	// Liveness: refuse to resume something that is running.
	wins, err := m.Tmux.ListWindows(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tmux windows: %w", err)
	}
	oldWindow := ""
	for _, w := range wins {
		if w.DmuxSession != sess.ID || w.WindowID != sess.Tmux.WindowID {
			continue
		}
		exited := false
		if evs, err := m.Events.Read(sess.ID); err == nil {
			lc := Reduce(evs).Lifecycle
			exited = lc == LifecycleExited || lc == LifecycleFailed
		}
		if !w.PaneDead && !exited && (m.Proc == nil || m.Proc.Alive(w.PanePID)) {
			return nil, fmt.Errorf("%w: %s (use `dmux jump %s`)", ErrSessionRunning, sess.ID, sess.ID)
		}
		oldWindow = w.WindowID // held or dead pane; close it below
	}

	argv := append([]string{m.DmuxBin, "run", "--session", sess.ID, "--dir", ws.Root, "--"},
		m.Devin.LaunchArgs(LaunchSpec{ResumeID: devinID, Prompt: in.Prompt, PermissionMode: in.PermissionMode, Model: in.Model})...)
	target, err := m.Tmux.SpawnWindow(ctx, SpawnWindowOpts{
		SessionName: m.prefix() + ws.Name,
		WorkspaceID: ws.ID,
		WindowName:  WindowName(sess.Task, sess.ID),
		SessionID:   sess.ID,
		Cwd:         ws.Root,
		Env:         map[string]string{"DMUX_SESSION_ID": sess.ID},
		Argv:        argv,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn tmux window: %w", err)
	}
	if err := m.Store.Update(ctx, func(st *State) error {
		s := st.Session(sess.ID)
		if s == nil {
			return errors.New("session record vanished during resume")
		}
		s.Tmux = target
		if s.DevinSessionID == "" {
			s.DevinSessionID = devinID
		}
		return nil
	}); err != nil {
		_ = m.Tmux.KillWindow(ctx, target.WindowID)
		return nil, fmt.Errorf("record tmux target: %w", err)
	}
	if oldWindow != "" && oldWindow != target.WindowID {
		_ = m.Tmux.KillWindow(ctx, oldWindow) // best effort; the new record no longer points at it
	}
	sess.Tmux, sess.DevinSessionID = target, devinID
	return &ResumeResult{Session: *sess, Workspace: *ws, DevinSessionID: devinID, OldWindow: oldWindow}, nil
}
