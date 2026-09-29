package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

func hasPrefixFold(s, prefix string) bool {
	return strings.HasPrefix(strings.ToLower(s), strings.ToLower(prefix))
}

func containsFold(s, sub string) bool {
	return sub != "" && strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// KillOptions tunes Kill.
type KillOptions struct {
	// Grace is how long to wait after SIGTERM before SIGKILL. Default 5s.
	Grace time.Duration
	// Poll interval while waiting. Default 100ms.
	Poll time.Duration
}

// KillResult describes what Kill did.
type KillResult struct {
	Session    Session
	Method     string // "already-exited" | "terminated" | "killed"
	WindowGone bool   // tmux window was removed
}

// Kill ends a session's process: SIGTERM to the pane's process group (the
// wrapper forwards it to Devin and records process_exited), wait up to
// Grace for the process to go, escalate to SIGKILL, then remove the tmux
// window. The Session record is kept: kill ends a process, it never
// deletes work or history (PLAN.md Cleanup: kill != rm). Use `dmux resume`
// to continue the conversation later.
func (m *SessionManager) Kill(ctx context.Context, query string, opts KillOptions) (*KillResult, error) {
	if m.Proc == nil || m.Events == nil {
		return nil, errors.New("kill: Proc and Events ports required")
	}
	if opts.Grace == 0 {
		opts.Grace = 5 * time.Second
	}
	if opts.Poll == 0 {
		opts.Poll = 100 * time.Millisecond
	}

	st, err := m.Store.Read()
	if err != nil {
		return nil, err
	}
	sess, err := findSession(st, query)
	if err != nil {
		return nil, err
	}
	res := &KillResult{Session: *sess}

	wins, err := m.Tmux.ListWindows(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tmux windows: %w", err)
	}
	var win *TmuxWindow
	for i := range wins {
		if wins[i].DmuxSession == sess.ID && wins[i].WindowID == sess.Tmux.WindowID {
			win = &wins[i]
			break
		}
	}
	if win == nil {
		res.Method = "already-exited"
		return res, nil // nothing to kill; record stays for resume
	}

	// If the events already record an exit, only the wrapper is left holding
	// the pane: there is no agent to stop, just a window to close.
	exited := false
	if evs, err := m.Events.Read(sess.ID); err == nil {
		lc := Reduce(evs).Lifecycle
		exited = lc == LifecycleExited || lc == LifecycleFailed
	}
	alive := !exited && !win.PaneDead && m.Proc.Alive(win.PanePID)
	if alive {
		if err := m.Proc.Terminate(win.PanePID); err != nil {
			return nil, fmt.Errorf("terminate pid %d: %w", win.PanePID, err)
		}
		res.Method = "terminated"
		if !m.waitExited(ctx, sess.ID, win.PanePID, opts) {
			if err := m.Proc.Kill(win.PanePID); err != nil {
				return nil, fmt.Errorf("kill pid %d: %w", win.PanePID, err)
			}
			res.Method = "killed"
			m.waitExited(ctx, sess.ID, win.PanePID, KillOptions{Grace: 2 * time.Second, Poll: opts.Poll})
		}
	} else {
		res.Method = "already-exited"
	}

	if err := m.Tmux.KillWindow(ctx, win.WindowID); err != nil {
		return res, fmt.Errorf("process ended but tmux window %s could not be removed: %w", win.WindowID, err)
	}
	res.WindowGone = true
	return res, nil
}

// KillManyResult is one session's outcome within KillAll.
type KillManyResult struct {
	Session Session
	Result  *KillResult
	Err     error
}

// KillAll stops every session with a live tmux window, optionally limited to
// one workspace (by name or id). Sessions are stopped concurrently so one
// stubborn process's grace period does not delay the rest. Sessions that
// are already exited are skipped, not reported. Records are kept.
func (m *SessionManager) KillAll(ctx context.Context, workspaceQuery string, opts KillOptions) ([]KillManyResult, error) {
	st, err := m.Store.Read()
	if err != nil {
		return nil, err
	}
	var ws *Workspace
	if workspaceQuery != "" {
		ws = st.WorkspaceByName(workspaceQuery)
		if ws == nil {
			ws = st.Workspace(workspaceQuery)
		}
		if ws == nil {
			return nil, fmt.Errorf("%w: %s", ErrWorkspaceNotFound, workspaceQuery)
		}
	}
	wins, err := m.Tmux.ListWindows(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tmux windows: %w", err)
	}
	live := map[string]bool{}
	for _, w := range wins {
		if w.DmuxSession != "" {
			live[w.DmuxSession] = true
		}
	}
	var targets []Session
	for _, s := range st.Sessions {
		if live[s.ID] && (ws == nil || s.WorkspaceID == ws.ID) {
			targets = append(targets, s)
		}
	}
	results := make([]KillManyResult, len(targets))
	var wg sync.WaitGroup
	for i, s := range targets {
		wg.Add(1)
		go func(i int, s Session) {
			defer wg.Done()
			res, err := m.Kill(ctx, s.ID, opts)
			results[i] = KillManyResult{Session: s, Result: res, Err: err}
		}(i, s)
	}
	wg.Wait()
	return results, nil
}

// waitExited polls until process_exited is recorded for the session or the
// process is gone, or grace elapses. The wrapper itself keeps running (it
// holds the pane), so the event is the primary signal.
func (m *SessionManager) waitExited(ctx context.Context, sessionID string, pid int, opts KillOptions) bool {
	deadline := time.Now().Add(opts.Grace)
	for time.Now().Before(deadline) {
		if evs, err := m.Events.Read(sessionID); err == nil {
			st := Reduce(evs)
			if st.Lifecycle == LifecycleExited || st.Lifecycle == LifecycleFailed {
				return true
			}
		}
		if !m.Proc.Alive(pid) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(opts.Poll):
		}
	}
	return false
}

// findSession resolves a session by exact id, unique id prefix, or unique
// task substring across all recorded sessions (live or not).
func findSession(st *State, query string) (*Session, error) {
	if s := st.Session(query); s != nil {
		return s, nil
	}
	var matches []*Session
	for i := range st.Sessions {
		s := &st.Sessions[i]
		if hasPrefixFold(s.ID, query) || containsFold(s.Task, query) {
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%w: %q", ErrSessionNotFound, query)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, len(matches))
		for i, s := range matches {
			ids[i] = s.ID
		}
		return nil, fmt.Errorf("%w: %q -> %v", ErrAmbiguous, query, ids)
	}
}
