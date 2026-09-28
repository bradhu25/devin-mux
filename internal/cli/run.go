package cli

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/launcher"
	"github.com/bradhu25/devin-mux/internal/state"
)

func newRunCmd() *cobra.Command {
	var (
		sessionID string
		dir       string
		noHold    bool
	)
	cmd := &cobra.Command{
		Use:    "run --session <id> [--dir <path>] -- <command> [args...]",
		Short:  "Launch wrapper used by dmux spawn (runs the agent, records its exit, holds the pane)",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sessionID == "" {
				return errors.New("--session is required")
			}
			res, err := launcher.Run(cmd.Context(), launcher.Options{
				SessionID: sessionID,
				Argv:      args,
				Dir:       dir,
				Hold:      !noHold,
				Emit:      state.AppendSessionEvent,
			})
			if err != nil {
				return err
			}
			// Mirror the child's exit status so tmux's pane_dead_status is meaningful.
			if res.ExitCode > 0 {
				os.Exit(res.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "dmux session id (exported to the child as DMUX_SESSION_ID)")
	cmd.Flags().StringVar(&dir, "dir", "", "working directory for the command")
	cmd.Flags().BoolVar(&noHold, "no-hold", false, "exit immediately after the command instead of holding the pane open")
	return cmd
}
