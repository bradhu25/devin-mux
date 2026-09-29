package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/core"
)

func newKillCmd(a *app) *cobra.Command {
	var grace time.Duration
	cmd := &cobra.Command{
		Use:   "kill <session>",
		Short: "Stop a session's Devin process and close its tmux window",
		Long: `Stop a running session: SIGTERM is sent to the session's process group, dmux
waits up to --grace for a clean exit, then SIGKILLs. The tmux window is removed.

The session record and Devin's conversation history are kept, so the
conversation can be continued later with "dmux resume <session>". Killing a
session never deletes files or branches; use "dmux workspace rm" for that.`,
		Example: `  dmux kill s_0a7ae6
  dmux kill auth        # by task text (must be unambiguous)`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSessions(a, sessionsLive),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			res, err := a.sessions.Kill(cmd.Context(), args[0], core.KillOptions{Grace: grace})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch res.Method {
			case "already-exited":
				if res.WindowGone {
					fmt.Fprintf(out, "Session %s had already exited; removed its tmux window.\n", res.Session.ID)
				} else {
					fmt.Fprintf(out, "Session %s has no running process or window; nothing to stop.\n", res.Session.ID)
				}
			case "terminated":
				fmt.Fprintf(out, "Stopped session %s (clean exit).\n", res.Session.ID)
			case "killed":
				fmt.Fprintf(out, "Stopped session %s (forced after %s).\n", res.Session.ID, grace)
			}
			if res.Session.DevinSessionID != "" {
				fmt.Fprintf(out, "Continue later with: dmux resume %s\n", res.Session.ID)
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&grace, "grace", 5*time.Second, "time to wait for a clean exit before SIGKILL")
	return cmd
}
