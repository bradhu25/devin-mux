package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/core"
)

func newWorkspaceRmCmd(a *app) *cobra.Command {
	var (
		stop, discard, deleteBranches, yes bool
		grace                              time.Duration
	)
	cmd := &cobra.Command{
		Use:     "rm <workspace>",
		Aliases: []string{"remove", "delete"},
		Short:   "Remove a workspace and every session in it: worktrees, tmux windows, records",
		Long: `Remove a workspace and all its sessions. Safe by default: it refuses while any
session is running (--stop ends them), refuses if any session's worktrees have
uncommitted changes or unmerged commits (--discard loses them), and never
removes a worktree that is locked with "git worktree lock". Branches are kept
unless --delete-branches, and even then only branches dmux created are deleted,
and only if fully merged. To remove one session: "dmux rm <session>".

Devin's conversation history is not touched; the conversation ids are printed
so you can still "devin -r <id>" them by hand.`,
		Example: `  dmux workspace status feature-x      # see what would be lost first
  dmux workspace rm feature-x
  dmux workspace rm feature-x --stop --discard --delete-branches`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaces(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			if !yes && (stop || discard || deleteBranches) {
				ok, err := confirm(cmd.InOrStdin(), cmd.OutOrStdout(), fmt.Sprintf("Remove workspace %q%s? [y/N] ", args[0], describeFlags(stop, discard, deleteBranches)))
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
					return nil
				}
			}
			res, err := a.workspaces.Remove(cmd.Context(), a.sessions, args[0], core.RemoveOptions{Stop: stop, Discard: discard, DeleteBranches: deleteBranches, KillGrace: grace})
			var refusal *core.RemoveRefusal
			if errors.As(err, &refusal) {
				return fmt.Errorf("%w\n  inspect with: dmux workspace status %s", err, args[0])
			}
			if err != nil {
				return err
			}
			printRemoveResult(cmd.OutOrStdout(), res)
			return nil
		},
	}
	cmd.Flags().BoolVar(&stop, "stop", false, "stop running sessions in the workspace first")
	cmd.Flags().BoolVar(&discard, "discard", false, "remove worktrees even with uncommitted changes or unmerged commits (data loss)")
	cmd.Flags().BoolVar(&deleteBranches, "delete-branches", false, "delete branches dmux created (git branch -d; unmerged branches are kept)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	cmd.Flags().DurationVar(&grace, "grace", 5*time.Second, "time to wait for sessions to exit before SIGKILL (with --stop)")
	return cmd
}

func describeFlags(stop, discard, deleteBranches bool) string {
	var f []string
	if stop {
		f = append(f, "stop running sessions")
	}
	if discard {
		f = append(f, "DISCARD uncommitted work")
	}
	if deleteBranches {
		f = append(f, "delete dmux-created branches")
	}
	if len(f) == 0 {
		return ""
	}
	return " (" + strings.Join(f, ", ") + ")"
}

var errNeedYes = errors.New("destructive flags need confirmation; pass --yes when not running interactively")

// confirm asks a y/N question. Pipes are detected up front; /dev/null is a
// character device and slips through, so an immediate EOF is treated the
// same way rather than as a silent "no".
func confirm(in io.Reader, out io.Writer, prompt string) (bool, error) {
	if f, ok := in.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice == 0 {
			return false, errNeedYes
		}
	}
	fmt.Fprint(out, prompt)
	line, err := bufio.NewReader(in).ReadString('\n')
	if errors.Is(err, io.EOF) && strings.TrimSpace(line) == "" {
		fmt.Fprintln(out)
		return false, errNeedYes
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes", nil
}
