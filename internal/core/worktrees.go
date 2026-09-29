package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// BranchName returns dmux's default branch for a session: dmux/<ws>/<slug>
// where slug derives from the task (PR-friendly), falling back to the
// session id. Collisions are handled by the caller.
func BranchName(workspaceName, task, sessionID string) string {
	slug := Slug(task)
	if slug == "" {
		slug = sessionID
	}
	return "dmux/" + workspaceName + "/" + slug
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug lowercases, keeps [a-z0-9] runs joined by '-', and caps length at a
// word boundary. Empty input yields "".
func Slug(s string) string {
	const maxLen = 40
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) <= maxLen {
		return s
	}
	cut := s[:maxLen]
	if i := strings.LastIndexByte(cut, '-'); i > maxLen/2 {
		cut = cut[:i]
	}
	return strings.Trim(cut, "-")
}

// worktreePlan is one repo's worktree to create for a session.
type worktreePlan struct {
	repo WorkspaceRepo
	opts WorktreeAddOpts
}

// planWorktrees validates and decides, without side effects, the worktree
// and branch for each repo of ws under root. branch, if set, is used for
// every repo: an existing branch is checked out (must not be checked out
// elsewhere), a new one is created from the repo's base ref. Otherwise the
// default name is used, with an id suffix if taken.
func planWorktrees(ctx context.Context, g Git, ws *Workspace, root, task, sessionID, branch string) ([]worktreePlan, error) {
	plans := make([]worktreePlan, 0, len(ws.Repos))
	for _, ref := range ws.Repos {
		repo := WorkspaceRepo{Name: ref.Name, SourcePath: ref.SourcePath, WorktreePath: filepath.Join(root, ref.Name)}
		name := branch
		explicit := branch != ""
		if !explicit {
			name = BranchName(ws.Name, task, sessionID)
			// Git refs are files: an existing branch "dmux/<ws>" (the v1
			// naming) makes "dmux/<ws>/<slug>" impossible. Say so up front.
			if parent := "dmux/" + ws.Name; parent != name {
				if exists, err := g.BranchExists(ctx, ref.SourcePath, parent); err != nil {
					return nil, err
				} else if exists {
					return nil, fmt.Errorf("branch %q in %s blocks creating %q (git cannot have both a ref and refs under it); rename it with `git -C %s branch -m %s %s-v1` or, if merged, delete it with `git -C %s branch -d %s`",
						parent, ref.SourcePath, name, ref.SourcePath, parent, parent, ref.SourcePath, parent)
				}
			}
		}
		exists, err := g.BranchExists(ctx, ref.SourcePath, name)
		if err != nil {
			return nil, err
		}
		switch {
		case exists && explicit:
			wts, err := g.WorktreeList(ctx, ref.SourcePath)
			if err != nil {
				return nil, err
			}
			for _, wt := range wts {
				if wt.Branch == name {
					return nil, fmt.Errorf("branch %q is already checked out at %s", name, wt.Path)
				}
			}
			repo.Branch, repo.BaseRef, repo.CreatedBranch = name, name, false
			plans = append(plans, worktreePlan{repo: repo, opts: WorktreeAddOpts{ExistingBranch: name}})
			continue
		case exists:
			name = name + "-" + strings.TrimPrefix(sessionID, "s_")
			if again, err := g.BranchExists(ctx, ref.SourcePath, name); err != nil {
				return nil, err
			} else if again {
				return nil, fmt.Errorf("branch %q already exists in %s", name, ref.SourcePath)
			}
		}
		base := ref.BaseRef
		if base == "" {
			base = "HEAD"
		}
		sha, err := g.ResolveRef(ctx, ref.SourcePath, base)
		if err != nil {
			return nil, fmt.Errorf("base ref %q in %s: %w", base, ref.SourcePath, err)
		}
		repo.Branch, repo.BaseRef, repo.CreatedBranch = name, sha, true
		plans = append(plans, worktreePlan{repo: repo, opts: WorktreeAddOpts{NewBranch: name, BaseRef: sha}})
	}
	return plans, nil
}

// rollbackWorktrees compensates a partially created worktree set: worktrees
// are removed in reverse order, dmux-created branches deleted with -d, and
// the root directory removed if empty. Errors are joined, never fatal.
func rollbackWorktrees(ctx context.Context, g Git, root string, repos []WorkspaceRepo) error {
	var errs []error
	for i := len(repos) - 1; i >= 0; i-- {
		r := repos[i]
		if err := g.WorktreeRemove(ctx, r.SourcePath, r.WorktreePath, true); err != nil {
			errs = append(errs, fmt.Errorf("remove worktree %s: %w", r.WorktreePath, err))
			continue
		}
		if r.CreatedBranch {
			if err := g.BranchDeleteSafe(ctx, r.SourcePath, r.Branch); err != nil {
				errs = append(errs, fmt.Errorf("delete branch %s: %w", r.Branch, err))
			}
		}
	}
	_ = os.Remove(filepath.Join(root, "AGENTS.md"))
	if err := os.Remove(root); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove %s: %w", root, err))
	}
	return errors.Join(errs...)
}
