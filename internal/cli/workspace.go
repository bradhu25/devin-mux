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
		Short:   "Create and manage workspaces (named sets of repos that sessions work in)",
	}
	cmd.AddCommand(newWorkspaceNewCmd(a), newWorkspaceListCmd(a), newWorkspaceStatusCmd(a), newWorkspaceRmCmd(a), newWorkspaceRenameCmd(a))
	return cmd
}

func newWorkspaceStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "status <workspace>",
		Aliases: []string{"diff"},
		Short:   "Show uncommitted changes and unmerged commits in every session of a workspace",
		Long: `Inspect every session's worktrees in a workspace: uncommitted changes, commits
on the branch that are not in its base, and git worktree locks. Run this before
"dmux workspace rm" to see what would be lost. For one session: "dmux session
status <session>".`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaces(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			st, err := a.workspaces.Inspect(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Workspace %s (%s)  root: %s\n", st.Workspace.Name, st.Workspace.Status, st.Workspace.Root)
			if len(st.Sessions) == 0 {
				fmt.Fprintln(out, "  no sessions")
				return nil
			}
			for _, s := range st.Sessions {
				fmt.Fprintln(out)
				renderSessionInspection(out, &s)
			}
			if st.AnyUnsafe() {
				fmt.Fprintf(out, "\n`dmux workspace rm %s` will refuse without --discard (and never removes locked worktrees).\n", st.Workspace.Name)
			}
			return nil
		},
	}
}

// renderSessionInspection prints one session's worktrees.
func renderSessionInspection(w io.Writer, si *core.SessionInspection) {
	s := si.Session
	fmt.Fprintf(w, "%s  %s  (%s)\n", s.ID, core.WindowName(s.Task, ""), s.Status)
	if !s.OwnsWorktrees() {
		fmt.Fprintf(w, "  shares the worktrees of %s\n", s.SharedWith)
		return
	}
	for _, r := range si.Repos {
		fmt.Fprintf(w, "  %s/  branch %s (base %s)\n", r.Repo.Name, r.Repo.Branch, shortRef(r.Repo.BaseRef))
		switch {
		case r.Err != nil:
			fmt.Fprintf(w, "    ! could not inspect: %v\n", r.Err)
		case r.Missing:
			fmt.Fprintf(w, "    ! worktree is no longer registered with git (run `dmux doctor`)\n")
		case r.Clean():
			fmt.Fprintf(w, "    clean — nothing would be lost\n")
		default:
			if r.Locked {
				fmt.Fprintf(w, "    locked by git worktree lock: %s\n", firstNonEmpty(r.LockReason, "(no reason given)"))
			}
			if r.CommitsAhead > 0 {
				fmt.Fprintf(w, "    %d commit(s) not in base %s\n", r.CommitsAhead, shortRef(r.Repo.BaseRef))
			}
			if len(r.Dirty) > 0 {
				fmt.Fprintf(w, "    %d uncommitted change(s):\n", len(r.Dirty))
				for i, line := range r.Dirty {
					if i == 10 {
						fmt.Fprintf(w, "      … %d more\n", len(r.Dirty)-10)
						break
					}
					fmt.Fprintf(w, "      %s\n", line)
				}
			}
		}
	}
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
		Use:   "new <name> --repo <path>[@ref] [--repo ...]",
		Short: "Create a workspace: a named set of repos that sessions work in",
		Long: `Create a workspace: your unit of work (a project, workstream, feature) spanning
one or more repos. It owns no worktrees itself. Every session you spawn in it
gets its own git worktree and branch per repo, under
~/.devin-mux/workspaces/<name>/<session>/<repo>/, so sessions never share files.

path@ref sets where sessions branch from in that repo (default: the repo's HEAD
at spawn time); --base sets it for every repo.`,
		Example: `  dmux workspace new feature-x --repo ~/code/api
  dmux workspace new feature-x --repo ~/code/api --repo ~/code/web
  dmux workspace new hotfix --repo ~/code/api@release/1.2`,
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
			printWorkspaceCreated(cmd.OutOrStdout(), ws)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&repos, "repo", nil, "path to a git checkout, optionally @ref for where sessions branch from (repeatable)")
	cmd.Flags().StringVar(&baseRef, "base", "", "ref sessions branch from in every repo (default: each repo's HEAD)")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func printWorkspaceCreated(out io.Writer, ws *core.Workspace) {
	fmt.Fprintf(out, "Created workspace %s (%s)\n  root: %s\n", ws.Name, ws.ID, ws.Root)
	for _, r := range ws.Repos {
		fmt.Fprintf(out, "  %-12s %s  (sessions branch from %s)\n", r.Name+"/", r.SourcePath, r.BaseRef)
	}
	fmt.Fprintf(out, "Next: dmux spawn %s -t \"<task>\"\n", ws.Name)
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
			st, err := a.store.Read()
			if err != nil {
				return err
			}
			return renderWorkspaces(cmd.OutOrStdout(), st)
		},
	}
}

func renderWorkspaces(w io.Writer, st *core.State) error {
	if len(st.Workspaces) == 0 {
		_, err := fmt.Fprintln(w, "No workspaces. Create one with: dmux workspace new <name> --repo <path>")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tSTATUS\tREPOS\tSESSIONS\tROOT")
	for _, ws := range st.Workspaces {
		names := make([]string, 0, len(ws.Repos))
		for _, r := range ws.Repos {
			n := r.Name
			if r.BaseRef != "HEAD" && r.BaseRef != "" {
				n += "@" + r.BaseRef
			}
			names = append(names, n)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", ws.Name, ws.ID, ws.Status, strings.Join(names, ","), len(st.SessionsIn(ws.ID)), ws.Root)
	}
	return tw.Flush()
}

// parseRepoSpecs turns --repo values into RepoSpecs. Paths are made absolute
// here (the CLI knows the cwd; core should not). The @ref separator is split
// on the LAST '@' so paths containing '@' still work when a ref is given; a
// path with '@' and no ref must be passed as path@ (empty).
func parseRepoSpecs(values []string) ([]core.RepoSpec, error) {
	if len(values) == 0 {
		return nil, errors.New("at least one --repo is required")
	}
	specs := make([]core.RepoSpec, 0, len(values))
	for _, v := range values {
		path, ref := v, ""
		if i := strings.LastIndex(v, "@"); i >= 0 {
			path, ref = v[:i], v[i+1:]
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
		specs = append(specs, core.RepoSpec{Path: abs, BaseRef: ref})
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

func newWorkspaceRenameCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <workspace> <new-name>",
		Short: "Rename a workspace (records and tmux session; directories and branches keep their names)",
		Long: `Change a workspace's name. Sessions, worktrees, and branches are unaffected
because they reference the workspace by id. The tmux session is renamed to
dmux-<new-name> and each session's AGENTS.md map is regenerated. Directories
under ~/.devin-mux/workspaces/ and dmux/<old-name>/... branches keep their
original names.`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeWorkspaces(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			res, err := a.workspaces.Rename(cmd.Context(), a.tmux, args[0], args[1])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if res.OldName == res.Workspace.Name {
				fmt.Fprintf(out, "Workspace is already named %s.\n", res.Workspace.Name)
				return nil
			}
			fmt.Fprintf(out, "Renamed workspace %s -> %s (%s)\n", res.OldName, res.Workspace.Name, res.Workspace.ID)
			if res.TmuxRenamed {
				fmt.Fprintf(out, "  tmux session renamed to dmux-%s\n", res.Workspace.Name)
			}
			fmt.Fprintf(out, "  directory unchanged: %s\n", res.Workspace.Root)
			for _, w := range res.Warnings {
				fmt.Fprintf(out, "  warning: %s\n", w)
			}
			return nil
		},
	}
}
