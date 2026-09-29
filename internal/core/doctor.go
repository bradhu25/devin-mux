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
	out = append(out, d.checkWorkspaces(ctx, st)...)
	out = append(out, d.checkOrphanDirs(st)...)
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

func (d *Doctor) checkWorkspaces(ctx context.Context, st *State) []Finding {
	var f []Finding
	for _, ws := range st.Workspaces {
		ws := ws
		// Saga state.
		switch ws.Status {
		case WorkspaceCreating, WorkspaceFailed, WorkspaceDeleting:
			f = append(f, d.stuckWorkspace(ctx, ws))
		}
		// Worktree registration for ready workspaces.
		if ws.Status != WorkspaceReady {
			continue
		}
		for _, repo := range ws.Repos {
			rs := inspectRepo(ctx, d.Git, repo)
			switch {
			case rs.Err != nil:
				f = append(f, Finding{Check: "worktree", Severity: SevWarn, Subject: ws.Name + "/" + repo.Name, Message: rs.Err.Error()})
			case rs.Missing:
				f = append(f, Finding{Check: "worktree", Severity: SevError, Subject: ws.Name + "/" + repo.Name,
					Message: fmt.Sprintf("worktree %s is no longer registered in %s", repo.WorktreePath, repo.SourcePath),
					Advice:  "if you removed it on purpose, `dmux workspace rm " + ws.Name + "`; otherwise re-create with `git -C " + repo.SourcePath + " worktree add " + repo.WorktreePath + " " + repo.Branch + "`"})
			}
		}
		if _, err := os.Stat(ws.Root); errors.Is(err, os.ErrNotExist) {
			f = append(f, Finding{Check: "workspace", Severity: SevError, Subject: ws.Name, Message: "root directory " + ws.Root + " is missing",
				Advice: "`dmux workspace rm " + ws.Name + "` to drop the record (git worktree entries will be pruned)"})
		}
	}
	return f
}

// stuckWorkspace handles creating/failed/deleting. The safe fix finishes
// the rollback only when every remaining worktree is clean; otherwise it
// advises the explicit command.
func (d *Doctor) stuckWorkspace(ctx context.Context, ws Workspace) Finding {
	fd := Finding{Check: "saga", Severity: SevError, Subject: ws.Name, Message: fmt.Sprintf("workspace is stuck in status %q", ws.Status)}
	unsafe := false
	for _, repo := range ws.Repos {
		rs := inspectRepo(ctx, d.Git, repo)
		if !rs.Missing && !rs.Clean() {
			unsafe = true
		}
	}
	if unsafe {
		fd.Advice = "worktrees contain work; inspect with `dmux workspace status " + ws.Name + "`, then `dmux workspace rm " + ws.Name + " --discard` to finish removal, or fix by hand and edit state"
		return fd
	}
	fd.FixDesc = "remove remaining clean worktrees, delete dmux-created branches, drop the record"
	fd.Fix = func(ctx context.Context) error {
		for i := len(ws.Repos) - 1; i >= 0; i-- {
			repo := ws.Repos[i]
			rs := inspectRepo(ctx, d.Git, repo)
			if rs.Locked {
				return fmt.Errorf("%s is locked; unlock it first", repo.WorktreePath)
			}
			if !rs.Missing {
				if err := d.Git.WorktreeRemove(ctx, repo.SourcePath, repo.WorktreePath, false); err != nil {
					return err
				}
			}
			if repo.CreatedBranch {
				_ = d.Git.BranchDeleteSafe(ctx, repo.SourcePath, repo.Branch) // refuses unmerged; fine
			}
			_ = d.Git.WorktreePrune(ctx, repo.SourcePath)
		}
		_ = os.Remove(filepath.Join(ws.Root, "AGENTS.md"))
		_ = os.Remove(ws.Root)
		return d.Store.Update(ctx, func(st *State) error {
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
		})
	}
	return fd
}

// checkOrphanDirs finds directories under the workspaces root that no
// record references. Never deleted automatically: they may hold work.
func (d *Doctor) checkOrphanDirs(st *State) []Finding {
	if d.WorkspacesRoot == "" {
		return nil
	}
	entries, err := os.ReadDir(d.WorkspacesRoot)
	if err != nil {
		return nil
	}
	known := map[string]bool{}
	for _, ws := range st.Workspaces {
		known[filepath.Clean(ws.Root)] = true
	}
	var f []Finding
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(d.WorkspacesRoot, e.Name())
		if !known[filepath.Clean(p)] {
			f = append(f, Finding{Check: "orphan", Severity: SevWarn, Subject: p, Message: "directory is not referenced by any workspace record",
				Advice: "inspect it; if it holds git worktrees, remove them with `git worktree remove` from their source repos, then delete the directory"})
		}
	}
	return f
}

// checkSessions reports sessions whose tmux window is dead or missing.
// The safe fix closes dead (held) windows only; records are kept because
// they enable `dmux resume`.
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
					return d.Store.Update(ctx, func(st *State) error {
						for i := range st.Sessions {
							if st.Sessions[i].ID == s.ID {
								st.Sessions = append(st.Sessions[:i], st.Sessions[i+1:]...)
								break
							}
						}
						return nil
					})
				}})
			continue
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
				f = append(f, Finding{Check: "session", Severity: SevInfo, Subject: s.ID, Message: msg, Advice: "`dmux resume " + s.ID + "` to continue it"})
			} else {
				f = append(f, Finding{Check: "session", Severity: SevWarn, Subject: s.ID, Message: msg + " and no Devin conversation id was recorded", Advice: "it cannot be resumed; `dmux init` ensures future sessions record it"})
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
