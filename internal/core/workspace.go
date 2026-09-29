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

// RepoSpec is one --repo argument to `dmux workspace new`: a checkout and,
// optionally, the ref that sessions in this workspace branch from in it
// (`path@ref`). Default: the repo's HEAD at spawn time.
type RepoSpec struct {
	Path    string
	BaseRef string
}

// CreateWorkspaceInput is the request for WorkspaceManager.Create.
type CreateWorkspaceInput struct {
	Name  string
	Repos []RepoSpec
	// BaseRef is the default branch point for repos without their own.
	BaseRef string
}

// WorkspaceManager owns workspace records and the workspace-level sagas.
// Worktrees belong to sessions; see SessionManager.
type WorkspaceManager struct {
	Git   Git
	Store Store
	// WorkspacesRoot is the directory workspaces are created under.
	WorkspacesRoot string
	// Now is injectable for tests.
	Now func() time.Time
}

// ErrWorkspaceExists is returned when the name is already taken.
var ErrWorkspaceExists = errors.New("workspace already exists")

// Create records a workspace: a name and the repos it spans. Validation
// happens before the single state write; the only side effect is the root
// directory that session dirs are created under.
func (m *WorkspaceManager) Create(ctx context.Context, in CreateWorkspaceInput) (*Workspace, error) {
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

	var refs []RepoRef
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
			return nil, fmt.Errorf("repo directory name %q cannot be used as a session subdirectory: %w", name, err)
		}
		if seen[name] {
			return nil, fmt.Errorf("two repos would both be named %q", name)
		}
		seen[name] = true
		base := spec.BaseRef
		if base == "" {
			base = in.BaseRef
		}
		if base == "" {
			base = "HEAD"
		}
		if _, err := m.Git.ResolveRef(ctx, top, base); err != nil {
			return nil, fmt.Errorf("base ref %q for %s: %w", base, top, err)
		}
		refs = append(refs, RepoRef{Name: name, SourcePath: top, BaseRef: base})
	}

	ws := &Workspace{Name: in.Name, Root: root, Repos: refs, Status: WorkspaceReady, CreatedAt: m.now()}
	err := m.Store.Update(ctx, func(st *State) error {
		if st.WorkspaceByName(in.Name) != nil {
			return fmt.Errorf("%w: %s", ErrWorkspaceExists, in.Name)
		}
		ws.ID = NewUniqueID(NewWorkspaceID, func(id string) bool { return st.Workspace(id) != nil })
		st.Workspaces = append(st.Workspaces, *ws)
		for _, r := range refs {
			st.RememberRepo(r.SourcePath)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		_ = m.Store.Update(ctx, func(st *State) error { st.removeWorkspace(ws.ID); return nil })
		return nil, fmt.Errorf("mkdir %s: %w", root, err)
	}
	return ws, nil
}

// List returns all workspaces from state.
func (m *WorkspaceManager) List() ([]Workspace, error) {
	st, err := m.Store.Read()
	if err != nil {
		return nil, err
	}
	return st.Workspaces, nil
}

// SessionAgentsMD renders the dmux-authored map placed at <session
// root>/AGENTS.md. The root is not inside any repo, so this is always-on
// context for Devin with zero repo pollution; it compensates for lazy
// per-repo rule loading (Spike 2) and tells the agent which branches are
// its own.
func SessionAgentsMD(ws *Workspace, s *Session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Workspace: %s — session %s\n\n", ws.Name, s.ID)
	if s.Task != "" {
		fmt.Fprintf(&b, "Task: %s\n\n", strings.TrimSpace(s.Task))
	}
	b.WriteString("This directory belongs to one dmux session. Each subdirectory below is a git\n")
	b.WriteString("worktree of a separate repository on a branch created for this session; other\n")
	b.WriteString("sessions work in other directories on other branches. Run git commands inside\n")
	b.WriteString("the repo directory you mean (e.g. `git -C api status`). This root directory\n")
	b.WriteString("itself is not a git repository.\n\n")
	b.WriteString("| Repo | Path | Branch | Base |\n|---|---|---|---|\n")
	for _, r := range s.Repos {
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

// removeWorkspace drops a workspace record and all its session records.
func (s *State) removeWorkspace(id string) {
	kept := s.Sessions[:0]
	for _, sess := range s.Sessions {
		if sess.WorkspaceID != id {
			kept = append(kept, sess)
		}
	}
	s.Sessions = kept
	for i := range s.Workspaces {
		if s.Workspaces[i].ID == id {
			s.Workspaces = append(s.Workspaces[:i], s.Workspaces[i+1:]...)
			return
		}
	}
}

// removeSession drops one session record.
func (s *State) removeSession(id string) {
	for i := range s.Sessions {
		if s.Sessions[i].ID == id {
			s.Sessions = append(s.Sessions[:i], s.Sessions[i+1:]...)
			return
		}
	}
}
