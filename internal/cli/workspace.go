package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/core"
)

func newWorkspaceCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"ws"},
		Short:   "Create and manage workspaces (sets of git worktrees)",
	}
	cmd.AddCommand(newWorkspaceNewCmd(a), newWorkspaceListCmd(a), newWorkspaceStatusCmd(a))
	return cmd
}

func newWorkspaceStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "status <workspace>",
		Aliases: []string{"diff"},
		Short:   "Show uncommitted changes and unmerged commits in each repo of a workspace",
		Long: `Inspect every repo worktree in a workspace: uncommitted changes, commits on the
branch that are not in its base ref, and git worktree locks. Run this before
"dmux workspace rm" to see what would be lost.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			st, err := a.workspaces.Inspect(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return renderWorkspaceStatus(cmd.OutOrStdout(), st)
		},
	}
}

func renderWorkspaceStatus(w io.Writer, st *core.WorkspaceInspection) error {
	fmt.Fprintf(w, "Workspace %s (%s)  root: %s\n", st.Workspace.Name, st.Workspace.Status, st.Workspace.Root)
	for _, r := range st.Repos {
		fmt.Fprintf(w, "\n%s/  branch %s (base %s)\n", r.Repo.Name, r.Repo.Branch, shortRef(r.Repo.BaseRef))
		switch {
		case r.Err != nil:
			fmt.Fprintf(w, "  ! could not inspect: %v\n", r.Err)
		case r.Missing:
			fmt.Fprintf(w, "  ! worktree is no longer registered with git (run `dmux doctor`)\n")
		case r.Clean():
			fmt.Fprintf(w, "  clean — nothing would be lost\n")
		default:
			if r.Locked {
				fmt.Fprintf(w, "  locked by git worktree lock: %s\n", firstNonEmpty(r.LockReason, "(no reason given)"))
			}
			if r.CommitsAhead > 0 {
				fmt.Fprintf(w, "  %d commit(s) not in base %s\n", r.CommitsAhead, shortRef(r.Repo.BaseRef))
			}
			if len(r.Dirty) > 0 {
				fmt.Fprintf(w, "  %d uncommitted change(s):\n", len(r.Dirty))
				for i, line := range r.Dirty {
					if i == 10 {
						fmt.Fprintf(w, "    … %d more\n", len(r.Dirty)-10)
						break
					}
					fmt.Fprintf(w, "    %s\n", line)
				}
			}
		}
	}
	if st.AnyUnsafe() {
		fmt.Fprintf(w, "\n`dmux workspace rm %s` will refuse without --discard (and never removes locked worktrees).\n", st.Workspace.Name)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func newWorkspaceNewCmd(a *app) *cobra.Command {
	var (
		repos   []string
		baseRef string
	)
	cmd := &cobra.Command{
		Use:   "new <name> --repo <path>[@branch] [--repo ...]",
		Short: "Create a workspace with a git worktree for each repo",
		Long: `Create a workspace: a directory under ~/.devin-mux/workspaces/<name>/ containing
one git worktree per --repo. By default each worktree gets a new branch
"dmux/<name>" created from the repo's HEAD (or --base). Use path@branch to check
out an existing branch instead; dmux will never delete branches it did not create.`,
		Example: `  dmux workspace new feature-x --repo ~/code/api
  dmux workspace new feature-x --repo ~/code/api --repo ~/code/web
  dmux workspace new hotfix --repo ~/code/api@release/1.2 --base origin/main`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			specs, err := parseRepoSpecs(repos)
			if err != nil {
				return err
			}
			ws, err := a.workspaces.Create(cmd.Context(), core.CreateWorkspaceInput{Name: args[0], Repos: specs, BaseRef: baseRef})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Created workspace %s (%s)\n  root: %s\n", ws.Name, ws.ID, ws.Root)
			for _, r := range ws.Repos {
				owner := "existing branch"
				if r.CreatedBranch {
					owner = "new branch"
				}
				fmt.Fprintf(out, "  %-12s %s  [%s, %s]\n", r.Name+"/", r.Branch, owner, shortRef(r.BaseRef))
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&repos, "repo", nil, "path to a git checkout, optionally @branch to reuse an existing branch (repeatable)")
	cmd.Flags().StringVar(&baseRef, "base", "", "ref to create new branches from (default: each repo's HEAD)")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func newWorkspaceListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List workspaces",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			wss, err := a.workspaces.List()
			if err != nil {
				return err
			}
			return renderWorkspaces(cmd.OutOrStdout(), wss)
		},
	}
}

func renderWorkspaces(w io.Writer, wss []core.Workspace) error {
	if len(wss) == 0 {
		_, err := fmt.Fprintln(w, "No workspaces. Create one with: dmux workspace new <name> --repo <path>")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tSTATUS\tREPOS\tROOT")
	for _, ws := range wss {
		names := make([]string, 0, len(ws.Repos))
		for _, r := range ws.Repos {
			names = append(names, r.Name+"@"+r.Branch)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", ws.Name, ws.ID, ws.Status, strings.Join(names, ","), ws.Root)
	}
	return tw.Flush()
}

// parseRepoSpecs turns --repo values into RepoSpecs. Paths are made absolute
// here (the CLI knows the cwd; core should not). The @branch separator is
// split on the LAST '@' so paths containing '@' still work when a branch is
// given; a path with '@' and no branch must be passed as path@ (empty).
func parseRepoSpecs(values []string) ([]core.RepoSpec, error) {
	if len(values) == 0 {
		return nil, errors.New("at least one --repo is required")
	}
	specs := make([]core.RepoSpec, 0, len(values))
	for _, v := range values {
		path, branch := v, ""
		if i := strings.LastIndex(v, "@"); i >= 0 {
			path, branch = v[:i], v[i+1:]
		}
		path = expandHome(path)
		if path == "" {
			return nil, fmt.Errorf("--repo %q: empty path", v)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("--repo %q: %w", v, err)
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, fmt.Errorf("--repo %q: %w", v, err)
		}
		specs = append(specs, core.RepoSpec{Path: abs, Branch: branch})
	}
	return specs, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func shortRef(ref string) string {
	if len(ref) == 40 {
		return ref[:12]
	}
	return ref
}
