package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// JumpCandidate is a session with a live, validated tmux window.
type JumpCandidate struct {
	Session   Session
	Workspace Workspace
	Window    TmuxWindow
}

// ErrSessionNotFound is returned when no session matches the query.
var ErrSessionNotFound = errors.New("session not found")

// ErrWindowGone is returned when a session's tmux window no longer exists or
// no longer carries its tag. The record is not modified here; the reconciler
// (M3) and `dmux doctor` own that transition.
var ErrWindowGone = errors.New("tmux window for session is gone")

// ErrAmbiguous is returned when a query matches more than one session.
var ErrAmbiguous = errors.New("query matches multiple sessions")

// Candidates returns every session whose tmux window exists AND carries the
// matching @dmux_session tag, grouped by workspace name then creation time.
// Sessions whose windows are missing are reported separately so callers can
// offer `dmux resume`.
func (m *SessionManager) Candidates(ctx context.Context) (live []JumpCandidate, gone []Session, err error) {
	st, err := m.Store.Read()
	if err != nil {
		return nil, nil, err
	}
	wins, err := m.Tmux.ListWindows(ctx)
	if err != nil {
		return nil, nil, err
	}
	byTag := make(map[string]TmuxWindow, len(wins))
	for _, w := range wins {
		if w.DmuxSession != "" {
			byTag[w.DmuxSession] = w
		}
	}
	for _, s := range st.Sessions {
		ws := st.Workspace(s.WorkspaceID)
		if ws == nil {
			continue
		}
		w, ok := byTag[s.ID]
		if !ok || w.WindowID != s.Tmux.WindowID {
			gone = append(gone, s)
			continue
		}
		live = append(live, JumpCandidate{Session: s, Workspace: *ws, Window: w})
	}
	sort.SliceStable(live, func(i, j int) bool {
		if live[i].Workspace.Name != live[j].Workspace.Name {
			return live[i].Workspace.Name < live[j].Workspace.Name
		}
		return live[i].Session.CreatedAt.Before(live[j].Session.CreatedAt)
	})
	return live, gone, nil
}

// Resolve finds a single live candidate by session id, id prefix, exact
// task, or case-insensitive task substring. If the query matches only
// sessions whose windows are gone, it returns ErrWindowGone so the caller
// can point at `dmux resume` instead of claiming the session doesn't exist.
func Resolve(live []JumpCandidate, gone []Session, query string) (JumpCandidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return JumpCandidate{}, fmt.Errorf("%w: empty query", ErrSessionNotFound)
	}
	matchesSession := func(s Session) bool {
		return strings.HasPrefix(s.ID, q) || strings.Contains(strings.ToLower(s.Task), strings.ToLower(q))
	}
	var matches []JumpCandidate
	for _, c := range live {
		if c.Session.ID == q {
			return c, nil
		}
		if matchesSession(c.Session) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		for _, s := range gone {
			if s.ID == q || matchesSession(s) {
				return JumpCandidate{}, fmt.Errorf("%w: %s (%q) has no live tmux window; use `dmux resume %s` once available", ErrWindowGone, s.ID, WindowName(s.Task, s.ID), s.ID)
			}
		}
		return JumpCandidate{}, fmt.Errorf("%w: %q", ErrSessionNotFound, q)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, len(matches))
		for i, c := range matches {
			ids[i] = c.Session.ID
		}
		return JumpCandidate{}, fmt.Errorf("%w: %q -> %s", ErrAmbiguous, q, strings.Join(ids, ", "))
	}
}

// Jump navigates to a candidate: inside tmux it switches the current
// client; outside it execs into `tmux attach` (and does not return on
// success). The candidate must come from Candidates so the window has
// already been validated against its tag.
func (m *SessionManager) Jump(ctx context.Context, c JumpCandidate) error {
	if c.Window.DmuxSession != c.Session.ID || c.Window.WindowID != c.Session.Tmux.WindowID {
		return fmt.Errorf("%w: %s", ErrWindowGone, c.Session.ID)
	}
	target := TmuxTarget{SessionName: c.Window.SessionName, SessionID: c.Window.SessionID, WindowID: c.Window.WindowID}
	if m.Tmux.InsideTmux() {
		return m.Tmux.SwitchClient(ctx, target)
	}
	return m.Tmux.Attach(target)
}
