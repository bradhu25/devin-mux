package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSessionCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Inspect sessions",
	}
	cmd.AddCommand(&cobra.Command{
		Use:               "status <session>",
		Aliases:           []string{"diff"},
		Short:             "Show uncommitted changes and unmerged commits in a session's worktrees",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions(a, sessionsAll),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			si, err := a.workspaces.InspectSession(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "dir: %s\n", si.Session.Root)
			renderSessionInspection(out, si)
			if si.AnyUnsafe() {
				fmt.Fprintf(out, "\n`dmux rm %s` will refuse without --discard (and never removes locked worktrees).\n", si.Session.ID)
			}
			return nil
		},
	})
	return cmd
}
