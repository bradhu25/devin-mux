package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/ui"
)

func newLsCmd(a *app) *cobra.Command {
	var (
		watch   bool
		verbose bool
	)
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "Show every workspace and session with live status",
		Long: `List workspaces and their sessions. Status is derived on each run from the
session's hook events, its tmux window, and Devin's own record of tool-call
outcomes:

  ● working            agent is executing
  ! awaiting-approval  a permission prompt needs you
  ○ idle               agent finished its turn and is waiting for input
  ◌ starting           process launched, no events yet
  × exited / ✗ failed  process gone (see dmux resume)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			render := func() error {
				snap, err := a.reconciler.Snapshot(cmd.Context())
				if err != nil {
					return err
				}
				if watch {
					fmt.Fprint(cmd.OutOrStdout(), "\033[H\033[2J") // clear screen
					fmt.Fprintf(cmd.OutOrStdout(), "dmux ls --watch  %s  (Ctrl-C to stop)\n\n", snap.TakenAt.Local().Format("15:04:05"))
				}
				return ui.RenderSnapshot(cmd.OutOrStdout(), snap, verbose)
			}
			if !watch {
				return render()
			}
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				if err := render(); err != nil {
					return err
				}
				select {
				case <-cmd.Context().Done():
					return nil
				case <-ticker.C:
				}
			}
		},
	}
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "refresh every 2 seconds")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show reconciliation evidence and last agent message")
	return cmd
}
