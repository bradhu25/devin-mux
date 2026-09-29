package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/core"
)

// newRmCmd removes one session: its worktrees, branches per policy, window,
// and record. Same safety rules as workspace rm.
func newRmCmd(a *app) *cobra.Command {
	var (
		stop, discard, deleteBranches, yes bool
		grace                              time.Duration
	)
	cmd := &cobra.Command{
		Use:     "rm <session>",
		Aliases: []string{"session-rm"},
		Short:   "Remove a session: its worktrees, tmux window, and record",
		Long: `Remove a session. Safe by default: refuses while it is running (--stop ends
it), refuses if its worktrees have uncommitted changes or unmerged commits
(--discard loses them), never removes a locked worktree, and refuses while other
sessions share its worktrees (remove those first). Branches are kept unless
--delete-branches, and even then only branches dmux created, only if merged.

Devin's conversation history is not touched. "dmux kill" stops a session
without removing anything.`,
		Example: `  dmux session status auth     # see what would be lost first
  dmux rm auth
  dmux rm auth --stop --discard --delete-branches`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions(a, sessionsAll),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			if !yes && (stop || discard || deleteBranches) {
				ok, err := confirm(cmd.InOrStdin(), cmd.OutOrStdout(), fmt.Sprintf("Remove session %q%s? [y/N] ", args[0], describeFlags(stop, discard, deleteBranches)))
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
					return nil
				}
			}
			res, err := a.workspaces.RemoveSession(cmd.Context(), a.sessions, args[0], core.RemoveOptions{Stop: stop, Discard: discard, DeleteBranches: deleteBranches, KillGrace: grace})
			var refusal *core.RemoveRefusal
			if errors.As(err, &refusal) {
				return fmt.Errorf("%w\n  inspect with: dmux session status %s", err, args[0])
			}
			if err != nil {
				return err
			}
			printRemoveResult(cmd.OutOrStdout(), res)
			return nil
		},
	}
	cmd.Flags().BoolVar(&stop, "stop", false, "stop the session first if it is running")
	cmd.Flags().BoolVar(&discard, "discard", false, "remove worktrees even with uncommitted changes or unmerged commits (data loss)")
	cmd.Flags().BoolVar(&deleteBranches, "delete-branches", false, "delete branches dmux created (git branch -d; unmerged branches are kept)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	cmd.Flags().DurationVar(&grace, "grace", 5*time.Second, "time to wait for the session to exit before SIGKILL (with --stop)")
	return cmd
}

func printRemoveResult(out io.Writer, res *core.RemoveResult) {
	if res.Workspace != nil {
		fmt.Fprintf(out, "Removed workspace %s\n", res.Workspace.Name)
	} else {
		fmt.Fprintf(out, "Removed session %s\n", strings.Join(res.RemovedSessions, ", "))
	}
	if res.Workspace != nil && len(res.RemovedSessions) > 0 {
		fmt.Fprintf(out, "  removed sessions:  %s\n", strings.Join(res.RemovedSessions, ", "))
	}
	if len(res.StoppedSessions) > 0 {
		fmt.Fprintf(out, "  stopped sessions:  %s\n", strings.Join(res.StoppedSessions, ", "))
	}
	if len(res.RemovedWorktrees) > 0 {
		fmt.Fprintf(out, "  removed worktrees: %s\n", strings.Join(res.RemovedWorktrees, ", "))
	}
	if len(res.DeletedBranches) > 0 {
		fmt.Fprintf(out, "  deleted branches:  %s\n", strings.Join(res.DeletedBranches, ", "))
	}
	if len(res.KeptBranches) > 0 {
		fmt.Fprintf(out, "  kept branches:     %s\n", strings.Join(res.KeptBranches, ", "))
	}
	if len(res.DevinSessionIDs) > 0 {
		fmt.Fprintf(out, "  Devin conversations kept (resume by hand): %s\n", strings.Join(res.DevinSessionIDs, ", "))
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(out, "  warning: %s\n", w)
	}
}
