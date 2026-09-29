package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/core"
)

// newNewCmd is the one-step path: create the workspace if needed, then spawn.
func newNewCmd(a *app) *cobra.Command {
	var (
		repos                                   []string
		baseRef                                 string
		task, permissionMode, model, branchName string
	)
	cmd := &cobra.Command{
		Use:   "new <workspace> [--repo <path>[@ref] ...] -t \"task\"",
		Short: "Create a workspace (if needed) and spawn a session in it, in one step",
		Long: `Equivalent to "dmux workspace new" followed by "dmux spawn". If the workspace
already exists, --repo must be omitted and the session is spawned in it; if it
does not, --repo is required and it is created first.`,
		Example: `  dmux new payment-fix --repo ~/code/api -t "Fix the refund rounding bug"
  dmux new payment-fix -t "Add a regression test for refunds"   # workspace exists`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaces(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			st, err := a.store.Read()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if ws := st.WorkspaceByName(args[0]); ws == nil {
				if len(repos) == 0 {
					return errors.New("workspace " + args[0] + " does not exist; pass --repo <path> to create it")
				}
				specs, err := parseRepoSpecs(repos)
				if err != nil {
					return err
				}
				created, err := a.workspaces.Create(cmd.Context(), core.CreateWorkspaceInput{Name: args[0], Repos: specs, BaseRef: baseRef})
				if err != nil {
					return err
				}
				printWorkspaceCreated(out, created)
				fmt.Fprintln(out)
			} else if len(repos) > 0 {
				return fmt.Errorf("workspace %s already exists; omit --repo to spawn in it", ws.Name)
			}
			res, err := a.sessions.Spawn(cmd.Context(), core.SpawnInput{Workspace: args[0], Task: task, PermissionMode: permissionMode, Model: model, Branch: branchName})
			if err != nil {
				return err
			}
			printSpawned(out, res)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&repos, "repo", nil, "path to a git checkout, optionally @ref (repeatable; required when the workspace is new)")
	cmd.Flags().StringVar(&baseRef, "base", "", "ref sessions branch from in every repo (default: each repo's HEAD)")
	cmd.Flags().StringVarP(&task, "task", "t", "", "initial prompt for Devin; also the session label and branch slug")
	cmd.Flags().StringVar(&permissionMode, "permission-mode", "", "Devin permission mode")
	cmd.Flags().StringVar(&model, "model", "", "Devin model override")
	cmd.Flags().StringVar(&branchName, "branch", "", "branch name for every repo (default dmux/<workspace>/<task-slug>)")
	return cmd
}
