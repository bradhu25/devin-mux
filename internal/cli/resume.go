package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/core"
)

func newResumeCmd(a *app) *cobra.Command {
	var (
		prompt         string
		permissionMode string
		model          string
	)
	cmd := &cobra.Command{
		Use:   "resume <session> [-t \"prompt\"]",
		Short: "Continue an exited session's Devin conversation in a new tmux window",
		Long: `Relaunch Devin with the session's recorded conversation id (devin -r <id>) from
the workspace root, in a fresh tmux window. History, context, and the workspace
files are exactly where the conversation left off.

dmux always resumes the specific conversation it recorded; it never guesses by
directory. A session that is still running cannot be resumed — jump to it.`,
		Example: `  dmux resume s_0a7ae6
  dmux resume auth -t "Pick up where you left off and run the tests"`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions(a, sessionsExited),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			res, err := a.sessions.Resume(cmd.Context(), core.ResumeInput{Query: args[0], Prompt: prompt, PermissionMode: permissionMode, Model: model})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Resumed session %s (devin %s) in workspace %s\n  tmux: %s (window %s)\n", res.Session.ID, res.DevinSessionID, res.Workspace.Name, res.Session.Tmux.SessionName, res.Session.Tmux.WindowID)
			if res.OldWindow != "" {
				fmt.Fprintf(out, "  closed previous window %s\n", res.OldWindow)
			}
			fmt.Fprintf(out, "Run `dmux jump %s` to switch to it.\n", res.Session.ID)
			return nil
		},
	}
	cmd.Flags().StringVarP(&prompt, "task", "t", "", "prompt to submit as soon as the conversation resumes")
	cmd.Flags().StringVar(&permissionMode, "permission-mode", "", "Devin permission mode override")
	cmd.Flags().StringVar(&model, "model", "", "Devin model override (switches the resumed conversation's model)")
	return cmd
}
