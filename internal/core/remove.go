package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RemoveOptions are the explicit flags that unlock destructive steps.
// Every default is the safe choice.
type RemoveOptions struct {
	Stop           bool // kill running sessions first; otherwise refuse
	Discard        bool // remove worktrees with uncommitted changes / unmerged commits; otherwise refuse
	DeleteBranches bool // `git branch -d` branches dmux created; otherwise keep
	KillGrace      time.Duration
}

// RemoveResult reports what happened.
type RemoveResult struct {
	Workspace        *Workspace // set when a whole workspace was removed
	RemovedSessions  []string
	StoppedSessions  []string
	RemovedWorktrees []string // "<session>/<repo>"
	DeletedBranches  []string
	KeptBranches     []string
	DevinSessionIDs  []string // conversations that outlive the removal (in Devin's store)
	Warnings         []string
}

// RemoveRefusal explains why rm did nothing. It is returned before any
// side effect, so everything is untouched.
type RemoveRefusal struct {
	Running []string     // live session ids (need --stop)
	Unsafe  []RepoStatus // dirty/ahead repos (need --discard)
	Locked  []RepoStatus // never removable
	Joiners []string     // sessions sharing the worktrees being removed (remove them first)
}

func (r *RemoveRefusal) Error() string {
	var parts []string
	if len(r.Running) > 0 {
		parts = append(parts, fmt.Sprintf("%d session(s) running (%s); pass --stop to end them", len(r.Running), strings.Join(r.Running, ", ")))
	}
	for _, u := range r.Unsafe {
		parts = append(parts, fmt.Sprintf("%s has uncommitted changes or unmerged commits; pass --discard to lose them", u.Repo.WorktreePath))
	}
	for _, l := range r.Locked {
		parts = append(parts, fmt.Sprintf("%s is locked (git worktree lock: %s); unlock it first — dmux never removes locked worktrees", l.Repo.WorktreePath, firstNonEmptyStr(l.LockReason, "no reason")))
	}
	if len(r.Joiners) > 0 {
		parts = append(parts, fmt.Sprintf("session(s) %s share these worktrees (spawned --in); remove them first", strings.Join(r.Joiners, ", ")))
	}
	return "refusing to remove: " + strings.Join(parts, "; ")
}

// RemoveSession removes one session: its process (with Stop), its tmux
// window, its worktrees and branches per policy, its dir, and its record.
// A joiner owns nothing, so only its process, window, and record go.
//
// Saga: decide (running/unsafe/locked/joiners) before any side effect;
// mark deleting; stop; remove worktrees (force only with Discard); branch
// policy (keep by default; -d only dmux-created); prune; remove dir; drop
// record last. A failure after marking leaves status=deleting for doctor.
func (m *WorkspaceManager) RemoveSession(ctx context.Context, sessions *SessionManager, query string, opts RemoveOptions) (*RemoveResult, error) {
	st, err := m.Store.Read()
	if err != nil {
		return nil, err
	}
	s, err := findSession(st, query)
	if err != nil {
		return nil, err
	}
	live, err := liveSessions(ctx, sessions, st)
	if err != nil {
		return nil, err
	}
	res := &RemoveResult{}
	refusal := m.refusalFor(ctx, st, []Session{*s}, live, opts)
	if refusal != nil {
		return nil, refusal
	}
	if err := m.removeSessions(ctx, sessions, st, []Session{*s}, live, opts, res); err != nil {
		return nil, err
	}
	return res, nil
}

// Remove removes a whole workspace: every session (joiners before owners)
// through the session saga, then the workspace dir and record.
func (m *WorkspaceManager) Remove(ctx context.Context, sessions *SessionManager, query string, opts RemoveOptions) (*RemoveResult, error) {
	st, err := m.Store.Read()
	if err != nil {
		return nil, err
	}
	ws := st.WorkspaceByName(query)
	if ws == nil {
		ws = st.Workspace(query)
	}
	if ws == nil {
		return nil, fmt.Errorf("%w: %s", ErrWorkspaceNotFound, query)
	}
	live, err := liveSessions(ctx, sessions, st)
	if err != nil {
		return nil, err
	}
	all := st.SessionsIn(ws.ID)
	if refusal := m.refusalFor(ctx, st, all, live, opts); refusal != nil {
		refusal.Joiners = nil // removing everything: joiners go too
		if len(refusal.Running)+len(refusal.Unsafe)+len(refusal.Locked) > 0 {
			return nil, refusal
		}
	}
	res := &RemoveResult{Workspace: ws}
	if err := m.Store.Update(ctx, func(st *State) error {
		if w := st.Workspace(ws.ID); w != nil {
			w.Status = WorkspaceDeleting
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := m.removeSessions(ctx, sessions, st, all, live, opts, res); err != nil {
		return nil, fmt.Errorf("%w (workspace %s left in status deleting; run `dmux doctor`)", err, ws.Name)
	}
	if err := os.Remove(ws.Root); err != nil && !errors.Is(err, os.ErrNotExist) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("workspace root %s not removed (not empty?): %v", ws.Root, err))
	}
	if err := m.Store.Update(ctx, func(st *State) error { st.removeWorkspace(ws.ID); return nil }); err != nil {
		return nil, fmt.Errorf("delete workspace record: %w (run `dmux doctor`)", err)
	}
	return res, nil
}

// liveSessions returns ids of sessions whose process is alive.
func liveSessions(ctx context.Context, sessions *SessionManager, st *State) (map[string]bool, error) {
	live := map[string]bool{}
	if sessions == nil {
		return live, nil
	}
	wins, err := sessions.Tmux.ListWindows(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tmux windows: %w", err)
	}
	for _, s := range st.Sessions {
		for _, w := range wins {
			if w.DmuxSession == s.ID && w.WindowID == s.Tmux.WindowID && !w.PaneDead && (sessions.Proc == nil || sessions.Proc.Alive(w.PanePID)) {
				live[s.ID] = true
			}
		}
	}
	return live, nil
}

// refusalFor decides, without side effects, whether the given sessions can
// be removed under opts. nil means go ahead.
func (m *WorkspaceManager) refusalFor(ctx context.Context, st *State, targets []Session, live map[string]bool, opts RemoveOptions) *RemoveRefusal {
	ref := &RemoveRefusal{}
	removing := map[string]bool{}
	for _, s := range targets {
		removing[s.ID] = true
	}
	for _, s := range targets {
		if live[s.ID] && !opts.Stop {
			ref.Running = append(ref.Running, s.ID)
		}
		if !s.OwnsWorktrees() {
			continue
		}
		for _, j := range st.Joiners(s.ID) {
			if !removing[j.ID] {
				ref.Joiners = append(ref.Joiners, j.ID)
			}
		}
		for _, r := range inspectSession(ctx, m.Git, s).Repos {
			switch {
			case r.Locked:
				ref.Locked = append(ref.Locked, r)
			case r.Missing:
			case !r.Clean() && !opts.Discard:
				ref.Unsafe = append(ref.Unsafe, r)
			}
		}
	}
	if len(ref.Running)+len(ref.Unsafe)+len(ref.Locked)+len(ref.Joiners) == 0 {
		return nil
	}
	return ref
}

// removeSessions executes the saga for a set of sessions: joiners first so
// owners have nobody depending on them.
func (m *WorkspaceManager) removeSessions(ctx context.Context, sessions *SessionManager, st *State, targets []Session, live map[string]bool, opts RemoveOptions, res *RemoveResult) error {
	ordered := make([]Session, 0, len(targets))
	for _, s := range targets {
		if !s.OwnsWorktrees() {
			ordered = append(ordered, s)
		}
	}
	for _, s := range targets {
		if s.OwnsWorktrees() {
			ordered = append(ordered, s)
		}
	}
	for _, s := range ordered {
		if s.DevinSessionID != "" {
			res.DevinSessionIDs = append(res.DevinSessionIDs, s.DevinSessionID)
		}
		if err := m.Store.Update(ctx, func(st *State) error {
			if x := st.Session(s.ID); x != nil {
				x.Status = WorkspaceDeleting
			}
			return nil
		}); err != nil {
			return err
		}
		fail := func(step string, cause error) error {
			return fmt.Errorf("remove session %s failed at %s: %w (left in status deleting; run `dmux doctor`)", s.ID, step, cause)
		}
		// Process and window.
		if sessions != nil {
			if live[s.ID] {
				kr, err := sessions.Kill(ctx, s.ID, KillOptions{Grace: opts.KillGrace})
				if err != nil {
					return fail("stop", err)
				}
				res.StoppedSessions = append(res.StoppedSessions, kr.Session.ID)
			}
			if wins, err := sessions.Tmux.ListWindows(ctx); err == nil {
				for _, w := range wins {
					if w.DmuxSession == s.ID {
						_ = sessions.Tmux.KillWindow(ctx, w.WindowID)
					}
				}
			}
		}
		// Worktrees and branches (owners only).
		if s.OwnsWorktrees() {
			insp := inspectSession(ctx, m.Git, s)
			for i, repo := range s.Repos {
				if !insp.Repos[i].Missing {
					if err := m.Git.WorktreeRemove(ctx, repo.SourcePath, repo.WorktreePath, opts.Discard); err != nil {
						return fail("remove worktree "+repo.Name, err)
					}
				}
				res.RemovedWorktrees = append(res.RemovedWorktrees, s.ID+"/"+repo.Name)
				switch {
				case !repo.CreatedBranch:
					res.KeptBranches = append(res.KeptBranches, repo.Branch+" (pre-existing)")
				case !opts.DeleteBranches:
					res.KeptBranches = append(res.KeptBranches, repo.Branch)
				default:
					if err := m.Git.BranchDeleteSafe(ctx, repo.SourcePath, repo.Branch); err != nil {
						res.Warnings = append(res.Warnings, fmt.Sprintf("kept branch %s in %s: %v", repo.Branch, repo.SourcePath, err))
						res.KeptBranches = append(res.KeptBranches, repo.Branch+" (not fully merged)")
					} else {
						res.DeletedBranches = append(res.DeletedBranches, repo.Branch)
					}
				}
			}
			for _, repo := range s.Repos {
				if err := m.Git.WorktreePrune(ctx, repo.SourcePath); err != nil {
					res.Warnings = append(res.Warnings, fmt.Sprintf("prune %s: %v", repo.SourcePath, err))
				}
			}
			_ = os.Remove(filepath.Join(s.Root, "AGENTS.md"))
			if err := os.Remove(s.Root); err != nil && !errors.Is(err, os.ErrNotExist) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("session dir %s not removed (not empty?): %v", s.Root, err))
			}
		}
		if err := m.Store.Update(ctx, func(st *State) error { st.removeSession(s.ID); return nil }); err != nil {
			return fail("delete record", err)
		}
		res.RemovedSessions = append(res.RemovedSessions, s.ID)
	}
	return nil
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
