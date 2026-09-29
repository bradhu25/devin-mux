package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Severity of a doctor finding.
type Severity string

const (
	SevInfo  Severity = "info"
	SevWarn  Severity = "warn"
	SevError Severity = "error"
)

// Finding is one problem doctor detected. Fix, when non-nil, is a repair
// that is safe by construction (never deletes user work); `--fix` runs it.
type Finding struct {
	Check    string
	Severity Severity
	Subject  string // workspace/session id or path
	Message  string
	Advice   string // what the user can do when there is no safe Fix
	Fix      func(ctx context.Context) error
	FixDesc  string
}

// Doctor inspects state, git, tmux, and the filesystem for inconsistencies.
type Doctor struct {
	Store          Store
	Git            Git
	Tmux           Tmux
	Devin          Devin
	Events         EventLog
	WorkspacesRoot string
	// HooksInstalled reports whether Devin's config has dmux's hook; nil skips the check.
	HooksInstalled func() (bool, error)
	// BinaryOnPath, if set, reports whether `dmux` resolves on PATH to the
	// running binary, plus the fix to suggest when it does not.
	BinaryOnPath func() (ok bool, advice string)
	// KnownRepos are extra source repos to scan for orphan dmux branches.
	KnownRepos []string
}

// Run executes every check and returns findings sorted by severity.
func (d *Doctor) Run(ctx context.Context) ([]Finding, error) {
	st, err := d.Store.Read()
	if err != nil {
		return nil, err
	}
	var out []Finding
	out = append(out, d.checkTools(ctx)...)
	if d.BinaryOnPath != nil {
		if ok, advice := d.BinaryOnPath(); !ok {
			out = append(out, Finding{Check: "path", Severity: SevWarn, Subject: "dmux", Message: "`dmux` does not resolve on PATH to this binary", Advice: advice})
		}
	}
	out = append(out, d.checkHooks()...)
	out = append(out, d.checkWorkspaces(st)...)
	out = append(out, d.checkSessionWorktrees(ctx, st)...)
	out = append(out, d.checkOrphanDirs(st)...)
	out = append(out, d.checkOrphanBranches(ctx, st)...)
	out = append(out, d.checkSessions(ctx, st)...)
	rank := map[Severity]int{SevError: 0, SevWarn: 1, SevInfo: 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Severity] < rank[out[j].Severity] })
	return out, nil
}

func (d *Doctor) checkTools(ctx context.Context) []Finding {
	var f []Finding
	if d.Tmux != nil {
		if err := d.Tmux.Available(ctx); err != nil {
			f = append(f, Finding{Check: "tools", Severity: SevError, Subject: "tmux", Message: err.Error(), Advice: "install tmux (brew install tmux)"})
		}
	}
	if d.Devin != nil {
		if err := d.Devin.Available(ctx); err != nil {
			f = append(f, Finding{Check: "tools", Severity: SevError, Subject: "devin", Message: err.Error(), Advice: "install the Devin CLI and ensure it is on PATH"})
		}
	}
	return f
}

func (d *Doctor) checkHooks() []Finding {
	if d.HooksInstalled == nil {
		return nil
	}
	ok, err := d.HooksInstalled()
	switch {
	case err != nil:
		return []Finding{{Check: "hooks", Severity: SevWarn, Subject: "devin config", Message: "could not read Devin config: " + err.Error(), Advice: "run `dmux init`"}}
	case !ok:
		return []Finding{{Check: "hooks", Severity: SevWarn, Subject: "devin config", Message: "dmux hooks are not installed; session status will show as unknown", Advice: "run `dmux init`"}}
	}
	return nil
}

// checkWorkspaces: a workspace stuck in deleting (rm interrupted after its
// sessions were handled) and a missing root dir.
func (d *Doctor) checkWorkspaces(st *State) []Finding {
	var f []Finding
	for _, ws := range st.Workspaces {
		ws := ws
		if ws.Status == WorkspaceDeleting {
			if len(st.SessionsIn(ws.ID)) == 0 {
				f = append(f, Finding{Check: "saga", Severity: SevError, Subject: ws.Name, Message: "workspace removal was interrupted after its sessions were removed",
					FixDesc: "remove the empty workspace dir and drop the record", Fix: func(ctx context.Context) error {
						_ = os.Remove(ws.Root)
						return d.Store.Update(ctx, func(st *State) error { st.removeWorkspace(ws.ID); return nil })
					}})
			} else {
				f = append(f, Finding{Check: "saga", Severity: SevError, Subject: ws.Name, Message: "workspace is stuck in status deleting with sessions remaining",
					Advice: "finish with `dmux workspace rm " + ws.Name + "` (add --stop/--discard as it tells you)"})
			}
			continue
		}
		if _, err := os.Stat(ws.Root); errors.Is(err, os.ErrNotExist) && len(st.SessionsIn(ws.ID)) > 0 {
			f = append(f, Finding{Check: "workspace", Severity: SevError, Subject: ws.Name, Message: "root directory " + ws.Root + " is missing but sessions are recorded",
				Advice: "`dmux workspace rm " + ws.Name + "` to drop the records (git worktree entries will be pruned)"})
		}
	}
	return f
}

// checkSessionWorktrees: sessions stuck mid-saga, and ready sessions whose
// worktrees git no longer registers.
func (d *Doctor) checkSessionWorktrees(ctx context.Context, st *State) []Finding {
	var f []Finding
	for _, s := range st.Sessions {
		s := s
		if !s.OwnsWorktrees() {
			continue
		}
		switch s.Status {
		case WorkspaceCreating, WorkspaceFailed, WorkspaceDeleting:
			f = append(f, d.stuckSession(ctx, st, s))
			continue
		}
		for _, repo := range s.Repos {
			rs := inspectRepo(ctx, d.Git, repo)
			switch {
			case rs.Err != nil:
				f = append(f, Finding{Check: "worktree", Severity: SevWarn, Subject: s.ID + "/" + repo.Name, Message: rs.Err.Error()})
			case rs.Missing:
				f = append(f, Finding{Check: "worktree", Severity: SevError, Subject: s.ID + "/" + repo.Name,
					Message: fmt.Sprintf("worktree %s is no longer registered in %s", repo.WorktreePath, repo.SourcePath),
					Advice:  "if you removed it on purpose, `dmux rm " + s.ID + "`; otherwise re-create with `git -C " + repo.SourcePath + " worktree add " + repo.WorktreePath + " " + repo.Branch + "`"})
			}
		}
	}
	return f
}

// stuckSession handles creating/failed/deleting sessions. The safe fix
// finishes the rollback only when every remaining worktree is clean and no
// other session shares them; otherwise it advises the explicit command.
func (d *Doctor) stuckSession(ctx context.Context, st *State, s Session) Finding {
	fd := Finding{Check: "saga", Severity: SevError, Subject: s.ID, Message: fmt.Sprintf("session is stuck in status %q", s.Status)}
	unsafe := false
	for _, repo := range s.Repos {
		if rs := inspectRepo(ctx, d.Git, repo); !rs.Missing && !rs.Clean() {
			unsafe = true
		}
	}
	if unsafe || len(st.Joiners(s.ID)) > 0 {
		fd.Advice = "worktrees contain work or are shared; inspect with `dmux session status " + s.ID + "`, then `dmux rm " + s.ID + " --discard` to finish removal"
		return fd
	}
	fd.FixDesc = "remove remaining clean worktrees, delete dmux-created branches, drop the record"
	fd.Fix = func(ctx context.Context) error {
		for _, repo := range s.Repos {
			if inspectRepo(ctx, d.Git, repo).Locked {
				return fmt.Errorf("%s is locked; unlock it first", repo.WorktreePath)
			}
		}
		if err := rollbackWorktrees(ctx, d.Git, s.Root, s.Repos); err != nil {
			return err
		}
		for _, repo := range s.Repos {
			_ = d.Git.WorktreePrune(ctx, repo.SourcePath)
		}
		return d.Store.Update(ctx, func(st *State) error { st.removeSession(s.ID); return nil })
	}
	return fd
}

// checkOrphanDirs finds directories no record references: under the
// workspaces root (not a workspace) and under each workspace root (not a
// session). Never deleted automatically: they may hold work.
func (d *Doctor) checkOrphanDirs(st *State) []Finding {
	if d.WorkspacesRoot == "" {
		return nil
	}
	known := map[string]bool{}
	for _, ws := range st.Workspaces {
		known[filepath.Clean(ws.Root)] = true
	}
	for _, s := range st.Sessions {
		known[filepath.Clean(s.Root)] = true
		for _, r := range s.Repos {
			known[filepath.Clean(r.WorktreePath)] = true // v1-migrated sessions keep worktrees directly under the workspace root
		}
	}
	var f []Finding
	report := func(p string) {
		f = append(f, Finding{Check: "orphan", Severity: SevWarn, Subject: p, Message: "directory is not referenced by any workspace or session record",
			Advice: "inspect it; if it holds git worktrees, remove them with `git worktree remove` from their source repos, then delete the directory"})
	}
	entries, err := os.ReadDir(d.WorkspacesRoot)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(d.WorkspacesRoot, e.Name())
		if !known[filepath.Clean(p)] {
			report(p)
			continue
		}
		subs, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, se := range subs {
			if !se.IsDir() {
				continue
			}
			sp := filepath.Join(p, se.Name())
			if !known[filepath.Clean(sp)] {
				report(sp)
			}
		}
	}
	return f
}

// checkOrphanBranches reports dmux/* branches in known source repos that no
// session uses. They are left behind by `rm` without --delete-branches
// (the default, since a branch is often the deliverable). Never deleted
// automatically; the finding carries the exact `git branch -d` command,
// which itself refuses unmerged branches.
func (d *Doctor) checkOrphanBranches(ctx context.Context, st *State) []Finding {
	if d.Git == nil {
		return nil
	}
	inUse := map[string]bool{}
	repos := map[string]bool{}
	for _, ws := range st.Workspaces {
		for _, r := range ws.Repos {
			repos[r.SourcePath] = true
		}
	}
	for _, s := range st.Sessions {
		for _, r := range s.Repos {
			repos[r.SourcePath] = true
			inUse[r.SourcePath+"\x00"+r.Branch] = true
		}
	}
	for _, p := range append(st.KnownRepos, d.KnownRepos...) {
		repos[p] = true
	}
	var f []Finding
	for repo := range repos {
		branches, err := d.Git.ListBranches(ctx, repo, "dmux/")
		if err != nil {
			continue
		}
		var orphans []string
		for _, b := range branches {
			if !inUse[repo+"\x00"+b] {
				orphans = append(orphans, b)
			}
		}
		if len(orphans) == 0 {
			continue
		}
		sort.Strings(orphans)
		advice := fmt.Sprintf("if their work is merged or unwanted: git -C %s branch -d %s   (refuses unmerged branches; they may also be the branches you meant to keep)", repo, strings.Join(orphans, " "))
		for _, b := range orphans {
			if strings.Count(b, "/") == 1 { // v1 naming dmux/<ws>: blocks dmux/<ws>/<slug>
				advice += fmt.Sprintf("; note %s blocks new session branches %s/* until renamed (git -C %s branch -m %s %s-v1) or deleted", b, b, repo, b, b)
			}
		}
		f = append(f, Finding{Check: "branch", Severity: SevInfo, Subject: repo,
			Message: fmt.Sprintf("%d dmux branch(es) not used by any session: %s", len(orphans), strings.Join(orphans, ", ")),
			Advice:  advice})
	}
	sort.Slice(f, func(i, j int) bool { return f[i].Subject < f[j].Subject })
	return f
}

// checkSessions reports sessions whose tmux window is dead or missing, and
// records pointing at nothing.
func (d *Doctor) checkSessions(ctx context.Context, st *State) []Finding {
	if d.Tmux == nil {
		return nil
	}
	wins, err := d.Tmux.ListWindows(ctx)
	if err != nil {
		return nil // tools check already reports tmux problems
	}
	byTag := map[string]TmuxWindow{}
	for _, w := range wins {
		if w.DmuxSession != "" {
			byTag[w.DmuxSession] = w
		}
	}
	var f []Finding
	for _, s := range st.Sessions {
		s := s
		if st.Workspace(s.WorkspaceID) == nil {
			f = append(f, Finding{Check: "session", Severity: SevWarn, Subject: s.ID, Message: "session references unknown workspace " + s.WorkspaceID,
				FixDesc: "drop the orphan session record", Fix: func(ctx context.Context) error {
					return d.Store.Update(ctx, func(st *State) error { st.removeSession(s.ID); return nil })
				}})
			continue
		}
		if !s.OwnsWorktrees() && st.Session(s.SharedWith) == nil {
			f = append(f, Finding{Check: "session", Severity: SevWarn, Subject: s.ID, Message: "session shares worktrees of " + s.SharedWith + ", which no longer exists",
				Advice: "`dmux rm " + s.ID + "` (its worktrees were owned by the removed session)"})
		}
		if s.Status != WorkspaceReady {
			continue // saga check covers it
		}
		w, ok := byTag[s.ID]
		exited := false
		if d.Events != nil {
			if evs, err := d.Events.Read(s.ID); err == nil {
				lc := Reduce(evs).Lifecycle
				exited = lc == LifecycleExited || lc == LifecycleFailed
			}
		}
		switch {
		case ok && w.WindowID == s.Tmux.WindowID && (w.PaneDead || exited):
			wid := w.WindowID
			f = append(f, Finding{Check: "session", Severity: SevInfo, Subject: s.ID, Message: "process exited; tmux window " + wid + " is still open",
				FixDesc: "close the window (record kept for `dmux resume`)", Fix: func(ctx context.Context) error { return d.Tmux.KillWindow(ctx, wid) }})
		case !ok || w.WindowID != s.Tmux.WindowID:
			msg := "no tmux window; session is not running"
			if s.DevinSessionID != "" {
				f = append(f, Finding{Check: "session", Severity: SevInfo, Subject: s.ID, Message: msg, Advice: "`dmux resume " + s.ID + "` to continue it, or `dmux rm " + s.ID + "` to remove its worktrees"})
			} else if s.Tmux.WindowID != "" {
				f = append(f, Finding{Check: "session", Severity: SevWarn, Subject: s.ID, Message: msg + " and no Devin conversation id was recorded", Advice: "it cannot be resumed; `dmux rm " + s.ID + "` when its worktrees are no longer needed"})
			}
		}
	}
	return f
}

// Summarize renders findings compactly for the CLI.
func Summarize(findings []Finding) string {
	if len(findings) == 0 {
		return "No problems found."
	}
	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "[%-5s] %-9s %s: %s\n", f.Severity, f.Check, f.Subject, f.Message)
		if f.Fix != nil {
			fmt.Fprintf(&b, "        fix: %s (run with --fix)\n", f.FixDesc)
		} else if f.Advice != "" {
			fmt.Fprintf(&b, "        %s\n", f.Advice)
		}
	}
	return b.String()
}
