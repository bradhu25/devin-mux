package core

import (
	"context"
	"fmt"
)

// RepoStatus is the safety-relevant state of one repo worktree.
type RepoStatus struct {
	Repo         WorkspaceRepo
	Missing      bool // worktree directory is not registered with git anymore
	Locked       bool // `git worktree lock` — never removed by dmux
	LockReason   string
	Dirty        []string // `git status --porcelain` lines
	CommitsAhead int      // commits on the branch not reachable from BaseRef
	Err          error    // inspection failed; treat as unsafe
}

// Clean reports whether removing this worktree is safe: it is registered,
// unlocked, has no uncommitted changes and no commits beyond its base. A
// missing or uninspectable worktree is not clean — it needs `dmux doctor`.
func (r RepoStatus) Clean() bool {
	return r.Err == nil && !r.Missing && !r.Locked && len(r.Dirty) == 0 && r.CommitsAhead == 0
}

// WorkspaceStatus is the inspection of a whole workspace.
type WorkspaceInspection struct {
	Workspace Workspace
	Repos     []RepoStatus
	// Sessions recorded in the workspace, with liveness; filled by callers
	// that have a Reconciler (rm) — inspection itself is git-only.
}

// AnyUnsafe reports whether any repo has work that removal would lose or
// a lock that forbids removal.
func (w WorkspaceInspection) AnyUnsafe() bool {
	for _, r := range w.Repos {
		if !r.Clean() {
			return true
		}
	}
	return false
}

// Inspect reports, per repo, uncommitted changes, commits not in the base
// ref, and worktree locks. It is read-only.
func (m *WorkspaceManager) Inspect(ctx context.Context, query string) (*WorkspaceInspection, error) {
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
	out := &WorkspaceInspection{Workspace: *ws}
	for _, repo := range ws.Repos {
		out.Repos = append(out.Repos, m.inspectRepo(ctx, repo))
	}
	return out, nil
}

func (m *WorkspaceManager) inspectRepo(ctx context.Context, repo WorkspaceRepo) RepoStatus {
	return inspectRepo(ctx, m.Git, repo)
}

// inspectRepo is the shared read-only inspection used by status, rm, and
// doctor.
func inspectRepo(ctx context.Context, g Git, repo WorkspaceRepo) RepoStatus {
	rs := RepoStatus{Repo: repo}
	wts, err := g.WorktreeList(ctx, repo.SourcePath)
	if err != nil {
		rs.Err = fmt.Errorf("list worktrees of %s: %w", repo.SourcePath, err)
		return rs
	}
	registered := false
	for _, wt := range wts {
		if samePath(wt.Path, repo.WorktreePath) {
			registered = true
			rs.Locked, rs.LockReason = wt.Locked, wt.LockMsg
		}
	}
	if !registered {
		rs.Missing = true
		return rs
	}
	if rs.Dirty, err = g.StatusPorcelain(ctx, repo.WorktreePath); err != nil {
		rs.Err = err
		return rs
	}
	if repo.BaseRef != "" {
		if rs.CommitsAhead, err = g.CommitsAhead(ctx, repo.WorktreePath, repo.BaseRef); err != nil {
			rs.Err = fmt.Errorf("compare with base %s: %w", repo.BaseRef, err)
		}
	}
	return rs
}
