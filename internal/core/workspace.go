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

// RepoSpec is one --repo argument to `dmux workspace new`.
type RepoSpec struct {
	Path   string // path to an existing checkout (any dir inside it)
	Branch string // optional: existing branch to check out instead of creating one
}

// CreateWorkspaceInput is the request for WorkspaceManager.Create.
type CreateWorkspaceInput struct {
	Name  string
	Repos []RepoSpec
	// BaseRef is the ref new branches are created from. Defaults to HEAD of
	// each source repo.
	BaseRef string
}

// WorkspaceManager owns the workspace sagas. It depends only on ports.
type WorkspaceManager struct {
	Git   Git
	Store Store
	// WorkspacesRoot is the directory workspaces are created under.
	WorkspacesRoot string
	// Now is injectable for tests.
	Now func() time.Time
}

// BranchName returns dmux's default branch name for a workspace.
func BranchName(workspaceName string) string { return "dmux/" + workspaceName }

// ErrWorkspaceExists is returned when the name is already taken.
var ErrWorkspaceExists = errors.New("workspace already exists")

// CreateError wraps a failed creation. Rollback reports whether compensation
// succeeded; if false, `dmux doctor` must finish cleanup.
type CreateError struct {
	Step     string
	Cause    error
	Rollback error
}

func (e *CreateError) Error() string {
	msg := fmt.Sprintf("create workspace failed at %s: %v", e.Step, e.Cause)
	if e.Rollback != nil {
		msg += fmt.Sprintf(" (rollback incomplete: %v; run `dmux doctor`)", e.Rollback)
	}
	return msg
}

func (e *CreateError) Unwrap() error { return e.Cause }

// Create runs the workspace creation saga:
//
//  1. Validate everything (no side effects).
//  2. Reserve Workspace{status: creating} in state.
//  3. For each repo: git worktree add; record the repo.
//  4. Write the workspace AGENTS.md map.
//  5. Mark status ready.
//
// On failure after step 2, created worktrees are removed in reverse order,
// dmux-created branches are deleted with `-d`, and the workspace is left in
// state with status failed for `dmux doctor` to inspect.
func (m *WorkspaceManager) Create(ctx context.Context, in CreateWorkspaceInput) (*Workspace, error) {
	now := m.now()

	// --- 1. validate, no side effects ---------------------------------------
	if err := ValidateName(in.Name); err != nil {
		return nil, err
	}
	if len(in.Repos) == 0 {
		return nil, errors.New("at least one --repo is required")
	}
	root := filepath.Join(m.WorkspacesRoot, in.Name)
	if _, err := os.Stat(root); err == nil {
		return nil, fmt.Errorf("directory %s already exists", root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	type plan struct {
		repo WorkspaceRepo
		opts WorktreeAddOpts
	}
	plans := make([]plan, 0, len(in.Repos))
	seen := map[string]bool{}
	for _, spec := range in.Repos {
		top, ok, err := m.Git.IsRepo(ctx, spec.Path)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%s is not a git repository", spec.Path)
		}
		name := filepath.Base(top)
		if err := ValidateName(name); err != nil {
			return nil, fmt.Errorf("repo directory name %q cannot be used as a workspace subdirectory: %w", name, err)
		}
		if seen[name] {
			return nil, fmt.Errorf("two repos would both be named %q in the workspace", name)
		}
		seen[name] = true

		repo := WorkspaceRepo{Name: name, SourcePath: top, WorktreePath: filepath.Join(root, name)}
		var opts WorktreeAddOpts
		if spec.Branch != "" {
			exists, err := m.Git.BranchExists(ctx, top, spec.Branch)
			if err != nil {
				return nil, err
			}
			if !exists {
				return nil, fmt.Errorf("branch %q does not exist in %s", spec.Branch, top)
			}
			// git refuses to check out a branch that another worktree (including
			// the main checkout) already has; fail here, before side effects.
			wts, err := m.Git.WorktreeList(ctx, top)
			if err != nil {
				return nil, err
			}
			for _, wt := range wts {
				if wt.Branch == spec.Branch {
					return nil, fmt.Errorf("branch %q is already checked out at %s; omit @%s to create a new dmux branch from it instead", spec.Branch, wt.Path, spec.Branch)
				}
			}
			repo.Branch, repo.CreatedBranch = spec.Branch, false
			repo.BaseRef = spec.Branch
			opts = WorktreeAddOpts{ExistingBranch: spec.Branch}
		} else {
			branch := BranchName(in.Name)
			exists, err := m.Git.BranchExists(ctx, top, branch)
			if err != nil {
				return nil, err
			}
			if exists {
				return nil, fmt.Errorf("branch %q already exists in %s (left over from an earlier workspace?); reuse it with --repo %s@%s, or delete it first with `git -C %s branch -d %s`", branch, top, spec.Path, branch, top, branch)
			}
			baseRef := in.BaseRef
			if baseRef == "" {
				baseRef = "HEAD"
			}
			sha, err := m.Git.ResolveRef(ctx, top, baseRef)
			if err != nil {
				return nil, err
			}
			repo.Branch, repo.CreatedBranch = branch, true
			repo.BaseRef = sha
			opts = WorktreeAddOpts{NewBranch: branch, BaseRef: sha}
		}
		plans = append(plans, plan{repo: repo, opts: opts})
	}

	// --- 2. reserve in state -----------------------------------------------
	ws := &Workspace{Name: in.Name, Root: root, Status: WorkspaceCreating, CreatedAt: now}
	err := m.Store.Update(ctx, func(st *State) error {
		if st.WorkspaceByName(in.Name) != nil {
			return fmt.Errorf("%w: %s", ErrWorkspaceExists, in.Name)
		}
		ws.ID = NewUniqueID(NewWorkspaceID, func(id string) bool { return st.Workspace(id) != nil })
		st.Workspaces = append(st.Workspaces, *ws)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// --- 3. side effects, with compensation ---------------------------------
	fail := func(step string, cause error) error {
		rb := m.rollbackCreate(ctx, ws)
		_ = m.Store.Update(ctx, func(st *State) error {
			if w := st.Workspace(ws.ID); w != nil {
				w.Repos, w.Status = ws.Repos, WorkspaceFailed
			}
			return nil
		})
		return &CreateError{Step: step, Cause: cause, Rollback: rb}
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fail("mkdir workspace root", err)
	}
	for _, p := range plans {
		if err := m.Git.WorktreeAdd(ctx, p.repo.SourcePath, p.repo.WorktreePath, p.opts); err != nil {
			return nil, fail("worktree add "+p.repo.Name, err)
		}
		ws.Repos = append(ws.Repos, p.repo)
		// Persist progress so a crash mid-loop leaves an accurate record.
		if err := m.Store.Update(ctx, func(st *State) error {
			if w := st.Workspace(ws.ID); w != nil {
				w.Repos = ws.Repos
			}
			return nil
		}); err != nil {
			return nil, fail("record repo "+p.repo.Name, err)
		}
	}

	// --- 4. workspace map ----------------------------------------------------
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(WorkspaceAgentsMD(ws)), 0o644); err != nil {
		return nil, fail("write AGENTS.md", err)
	}

	// --- 5. ready ------------------------------------------------------------
	ws.Status = WorkspaceReady
	err = m.Store.Update(ctx, func(st *State) error {
		if w := st.Workspace(ws.ID); w != nil {
			w.Repos, w.Status = ws.Repos, WorkspaceReady
			for _, r := range ws.Repos {
				st.RememberRepo(r.SourcePath)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fail("mark ready", err)
	}
	return ws, nil
}

// rollbackCreate compensates a partially created workspace: worktrees are
// removed in reverse order, dmux-created branches deleted with -d, and the
// root directory removed if empty. Errors are joined, never fatal.
func (m *WorkspaceManager) rollbackCreate(ctx context.Context, ws *Workspace) error {
	var errs []error
	for i := len(ws.Repos) - 1; i >= 0; i-- {
		r := ws.Repos[i]
		if err := m.Git.WorktreeRemove(ctx, r.SourcePath, r.WorktreePath, true); err != nil {
			errs = append(errs, fmt.Errorf("remove worktree %s: %w", r.WorktreePath, err))
			continue
		}
		if r.CreatedBranch {
			if err := m.Git.BranchDeleteSafe(ctx, r.SourcePath, r.Branch); err != nil {
				errs = append(errs, fmt.Errorf("delete branch %s: %w", r.Branch, err))
			}
		}
	}
	_ = os.Remove(filepath.Join(ws.Root, "AGENTS.md"))
	if err := os.Remove(ws.Root); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove %s: %w", ws.Root, err))
	}
	return errors.Join(errs...)
}

// List returns all workspaces from state.
func (m *WorkspaceManager) List() ([]Workspace, error) {
	st, err := m.Store.Read()
	if err != nil {
		return nil, err
	}
	return st.Workspaces, nil
}

// WorkspaceAgentsMD renders the dmux-authored map placed at <root>/AGENTS.md.
// The root is not inside any repo, so this is always-on context for Devin
// with zero repo pollution. It compensates for lazy per-repo rule loading.
func WorkspaceAgentsMD(ws *Workspace) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Workspace: %s\n\n", ws.Name)
	b.WriteString("This directory is a dmux workspace. Each subdirectory below is a git worktree\n")
	b.WriteString("of a separate repository. Run git commands inside the repo directory you mean\n")
	b.WriteString("(e.g. `git -C api status`). This root directory itself is not a git repository.\n\n")
	b.WriteString("| Repo | Path | Branch | Base |\n|---|---|---|---|\n")
	for _, r := range ws.Repos {
		base := r.BaseRef
		if len(base) == 40 {
			base = base[:12]
		}
		fmt.Fprintf(&b, "| %s | ./%s | %s | %s |\n", r.Name, r.Name, r.Branch, base)
	}
	b.WriteString("\nEach repo may have its own AGENTS.md or .devin/rules; they load when you first\n")
	b.WriteString("access files in that repo. Do not edit or commit this file; dmux regenerates it.\n")
	return b.String()
}

func (m *WorkspaceManager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UTC()
}
