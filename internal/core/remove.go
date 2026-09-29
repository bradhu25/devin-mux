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
	Workspace       Workspace
	StoppedSessions []string
	RemovedRepos    []string
	DeletedBranches []string
	KeptBranches    []string
	DevinSessionIDs []string // conversations that outlive the workspace (in Devin's store)
	Warnings        []string
}

// RemoveRefusal explains why rm did nothing. It is returned before any
// side effect, so the workspace is untouched.
type RemoveRefusal struct {
	Running []string     // live session ids (need --stop)
	Unsafe  []RepoStatus // dirty/ahead repos (need --discard)
	Locked  []RepoStatus // never removable
}

func (r *RemoveRefusal) Error() string {
	var parts []string
	if len(r.Running) > 0 {
		parts = append(parts, fmt.Sprintf("%d session(s) running (%s); pass --stop to end them", len(r.Running), strings.Join(r.Running, ", ")))
	}
	for _, u := range r.Unsafe {
		parts = append(parts, fmt.Sprintf("%s has uncommitted changes or unmerged commits; pass --discard to lose them", u.Repo.Name))
	}
	for _, l := range r.Locked {
		parts = append(parts, fmt.Sprintf("%s is locked (git worktree lock: %s); unlock it first — dmux never removes locked worktrees", l.Repo.Name, firstNonEmptyStr(l.LockReason, "no reason")))
	}
	return "refusing to remove workspace: " + strings.Join(parts, "; ")
}

// Remove runs the workspace removal saga (PLAN.md Cleanup):
//
//  1. Identify sessions; running ones require Stop.
//  2. Inspect repos; locked => refuse always; unsafe => require Discard.
//     (Both checks happen before any side effect.)
//  3. state: status -> deleting
//  4. Stop sessions (Kill) and close their windows.
//  5. Remove worktrees (force only with Discard).
//  6. Branches: keep unless DeleteBranches; then `-d` only createdBranch.
//  7. Prune each source repo.
//  8. Remove root dir; state: delete workspace + session records (last).
//
// A failure after step 3 leaves status=deleting for `dmux doctor`.
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
	res := &RemoveResult{Workspace: *ws}

	// --- 1 & 2: decide before touching anything ------------------------------
	refusal := &RemoveRefusal{}
	sess := st.SessionsIn(ws.ID)
	var liveIDs []string
	if len(sess) > 0 && sessions != nil {
		wins, err := sessions.Tmux.ListWindows(ctx)
		if err != nil {
			return nil, fmt.Errorf("list tmux windows: %w", err)
		}
		for _, s := range sess {
			if s.DevinSessionID != "" {
				res.DevinSessionIDs = append(res.DevinSessionIDs, s.DevinSessionID)
			}
			for _, w := range wins {
				if w.DmuxSession == s.ID && w.WindowID == s.Tmux.WindowID && !w.PaneDead && sessions.Proc != nil && sessions.Proc.Alive(w.PanePID) {
					liveIDs = append(liveIDs, s.ID)
				}
			}
		}
	}
	if len(liveIDs) > 0 && !opts.Stop {
		refusal.Running = liveIDs
	}
	insp := &WorkspaceInspection{Workspace: *ws}
	for _, repo := range ws.Repos {
		insp.Repos = append(insp.Repos, m.inspectRepo(ctx, repo))
	}
	for _, r := range insp.Repos {
		switch {
		case r.Locked:
			refusal.Locked = append(refusal.Locked, r)
		case r.Missing:
			// Already gone from git; nothing to protect. Handled in step 5.
		case !r.Clean() && !opts.Discard:
			refusal.Unsafe = append(refusal.Unsafe, r)
		}
	}
	if len(refusal.Running)+len(refusal.Unsafe)+len(refusal.Locked) > 0 {
		return nil, refusal
	}

	// --- 3: mark deleting ------------------------------------------------------
	if err := m.Store.Update(ctx, func(st *State) error {
		w := st.Workspace(ws.ID)
		if w == nil {
			return fmt.Errorf("%w: %s", ErrWorkspaceNotFound, ws.ID)
		}
		w.Status = WorkspaceDeleting
		return nil
	}); err != nil {
		return nil, err
	}
	fail := func(step string, cause error) error {
		return fmt.Errorf("remove workspace %s failed at %s: %w (workspace left in status deleting; run `dmux doctor`)", ws.Name, step, cause)
	}

	// --- 4: stop sessions -------------------------------------------------------
	for _, id := range liveIDs {
		kr, err := sessions.Kill(ctx, id, KillOptions{Grace: opts.KillGrace})
		if err != nil {
			return nil, fail("stop session "+id, err)
		}
		res.StoppedSessions = append(res.StoppedSessions, kr.Session.ID)
	}
	// Close any leftover held/dead windows for this workspace's sessions.
	if sessions != nil {
		if wins, err := sessions.Tmux.ListWindows(ctx); err == nil {
			for _, s := range sess {
				for _, w := range wins {
					if w.DmuxSession == s.ID {
						_ = sessions.Tmux.KillWindow(ctx, w.WindowID)
					}
				}
			}
		}
	}

	// --- 5 & 6: worktrees and branches -----------------------------------------
	for i, repo := range ws.Repos {
		r := insp.Repos[i]
		if !r.Missing {
			if err := m.Git.WorktreeRemove(ctx, repo.SourcePath, repo.WorktreePath, opts.Discard); err != nil {
				return nil, fail("remove worktree "+repo.Name, err)
			}
		}
		res.RemovedRepos = append(res.RemovedRepos, repo.Name)
		switch {
		case !repo.CreatedBranch:
			res.KeptBranches = append(res.KeptBranches, repo.Name+"@"+repo.Branch+" (pre-existing)")
		case !opts.DeleteBranches:
			res.KeptBranches = append(res.KeptBranches, repo.Name+"@"+repo.Branch)
		default:
			if err := m.Git.BranchDeleteSafe(ctx, repo.SourcePath, repo.Branch); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("kept branch %s in %s: %v", repo.Branch, repo.SourcePath, err))
				res.KeptBranches = append(res.KeptBranches, repo.Name+"@"+repo.Branch+" (not fully merged)")
			} else {
				res.DeletedBranches = append(res.DeletedBranches, repo.Name+"@"+repo.Branch)
			}
		}
	}

	// --- 7: prune ----------------------------------------------------------------
	for _, repo := range ws.Repos {
		if err := m.Git.WorktreePrune(ctx, repo.SourcePath); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("prune %s: %v", repo.SourcePath, err))
		}
	}

	// --- 8: root dir, then records (last) --------------------------------------
	_ = os.Remove(filepath.Join(ws.Root, "AGENTS.md"))
	if err := os.Remove(ws.Root); err != nil && !errors.Is(err, os.ErrNotExist) {
		res.Warnings = append(res.Warnings, fmt.Sprintf("workspace root %s not removed (not empty?): %v", ws.Root, err))
	}
	if err := m.Store.Update(ctx, func(st *State) error {
		kept := st.Sessions[:0]
		for _, s := range st.Sessions {
			if s.WorkspaceID != ws.ID {
				kept = append(kept, s)
			}
		}
		st.Sessions = kept
		for i := range st.Workspaces {
			if st.Workspaces[i].ID == ws.ID {
				st.Workspaces = append(st.Workspaces[:i], st.Workspaces[i+1:]...)
				break
			}
		}
		return nil
	}); err != nil {
		return nil, fail("delete records", err)
	}
	return res, nil
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
