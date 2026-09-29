package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/core"
)

func newSpawnCmd(a *app) *cobra.Command {
	var (
		task, permissionMode, model, branch, in string
	)
	cmd := &cobra.Command{
		Use:   "spawn <workspace> [-t \"task\"] [--branch name] [--in session]",
		Short: "Start a new Devin session in a workspace, in its own worktrees and branch",
		Long: `Start a Devin agent in the given workspace. dmux creates a git worktree of every
repo in the workspace on a new branch dmux/<workspace>/<task-slug>, under
~/.devin-mux/workspaces/<workspace>/<session>/, and runs Devin there in a
detached tmux window. Sessions never share files or branches unless you ask:
--in <session> joins an existing session's worktrees instead (for a reviewer or
a helper working on the same branch).

-t is Devin's first prompt and the session's label; without it Devin opens at an
empty prompt and the first prompt you type becomes the task. --branch overrides
the branch name (an existing branch is checked out, a new one created).`,
		Example: `  dmux spawn feature-x -t "Fix the authentication bug in api/"
  dmux spawn feature-x -t "Add tests for web/" --branch feature-x-tests
  dmux spawn feature-x -t "Review the auth changes" --in auth`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaces(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			res, err := a.sessions.Spawn(cmd.Context(), core.SpawnInput{Workspace: args[0], Task: task, PermissionMode: permissionMode, Model: model, Branch: branch, In: in})
			if err != nil {
				return err
			}
			printSpawned(cmd.OutOrStdout(), res)
			return nil
		},
	}
	cmd.Flags().StringVarP(&task, "task", "t", "", "initial prompt for Devin; also the session label and branch slug")
	cmd.Flags().StringVar(&permissionMode, "permission-mode", "", "Devin permission mode (e.g. accept-edits)")
	cmd.Flags().StringVar(&model, "model", "", "Devin model override")
	cmd.Flags().StringVar(&branch, "branch", "", "branch name for every repo (default dmux/<workspace>/<task-slug>)")
	cmd.Flags().StringVar(&in, "in", "", "join this session's worktrees instead of creating new ones (shared mode)")
	_ = cmd.RegisterFlagCompletionFunc("in", completeSessions(a, sessionsAll))
	return cmd
}

func printSpawned(out io.Writer, res *core.SpawnResult) {
	s := res.Session
	fmt.Fprintf(out, "Spawned session %s in workspace %s\n", s.ID, res.Workspace.Name)
	if res.JoinedSession != nil {
		fmt.Fprintf(out, "  shares worktrees with %s (%s): %s\n", res.JoinedSession.ID, core.WindowName(res.JoinedSession.Task, ""), s.Root)
	} else {
		fmt.Fprintf(out, "  dir:    %s\n", s.Root)
		for _, r := range s.Repos {
			fmt.Fprintf(out, "  %-8s %s  (from %s)\n", r.Name+"/", r.Branch, shortRef(r.BaseRef))
		}
	}
	fmt.Fprintf(out, "  tmux:   %s (window %s)\n", s.Tmux.SessionName, s.Tmux.WindowID)
	fmt.Fprintf(out, "Run `dmux jump %s` to attach, `dmux ls` for status.\n", s.ID)
}
