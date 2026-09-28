package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/core"
)

func newSpawnCmd(a *app) *cobra.Command {
	var (
		task           string
		permissionMode string
		model          string
	)
	cmd := &cobra.Command{
		Use:   "spawn <workspace> [-t \"task\"]",
		Short: "Start a new Devin session in a workspace (inside tmux)",
		Long: `Start a Devin CLI session in the given workspace. The session runs detached in a
tmux window (session "dmux-<workspace>"), with the workspace root as its working
directory so every repo worktree is in scope. Use "dmux jump" to switch to it.

Isolation is per workspace, not per session: two sessions in the same workspace
share its files and branches.`,
		Example: `  dmux spawn feature-x -t "Fix the authentication bug in api/"
  dmux spawn feature-x --permission-mode accept-edits -t "Add tests for web/"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			res, err := a.sessions.Spawn(cmd.Context(), core.SpawnInput{Workspace: args[0], Task: task, PermissionMode: permissionMode, Model: model})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Spawned session %s in workspace %s\n  tmux: %s (window %s)\n", res.Session.ID, res.Workspace.Name, res.Session.Tmux.SessionName, res.Session.Tmux.WindowID)
			if task != "" {
				fmt.Fprintf(out, "  task: %s\n", task)
			}
			if res.OtherRunning > 0 {
				fmt.Fprintf(out, "  note: %d other session(s) already use this workspace; they share its files and branches\n", res.OtherRunning)
			}
			fmt.Fprintf(out, "Run `dmux jump` to switch to it.\n")
			return nil
		},
	}
	cmd.Flags().StringVarP(&task, "task", "t", "", "initial prompt for Devin (also used as the window name)")
	cmd.Flags().StringVar(&permissionMode, "permission-mode", "", "Devin permission mode (auto, accept-edits, smart, dangerous)")
	cmd.Flags().StringVar(&model, "model", "", "Devin model override")
	return cmd
}
