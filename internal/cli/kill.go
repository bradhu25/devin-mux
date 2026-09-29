package cli

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/core"
)

func newKillCmd(a *app) *cobra.Command {
	var (
		grace     time.Duration
		all       bool
		workspace string
	)
	cmd := &cobra.Command{
		Use:   "kill <session> | --all | --workspace <name>",
		Short: "Stop a session's Devin process and close its tmux window",
		Long: `Stop a running session: SIGTERM is sent to the session's process group, dmux
waits up to --grace for a clean exit, then SIGKILLs. The tmux window is removed.

The session record and Devin's conversation history are kept, so the
conversation can be continued later with "dmux resume <session>". Killing a
session never deletes files or branches; use "dmux workspace rm" for that.

--all stops every running session; --workspace stops those in one workspace.
Sessions are stopped concurrently.`,
		Example: `  dmux kill s_0a7ae6
  dmux kill auth            # by task text (must be unambiguous)
  dmux kill --workspace feature-x
  dmux kill --all`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeSessions(a, sessionsLive),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			switch {
			case len(args) == 1 && (all || workspace != ""):
				return errors.New("give a session, or --all / --workspace, not both")
			case len(args) == 0 && !all && workspace == "":
				return errors.New("session required (or --all / --workspace <name>)")
			case len(args) == 1:
				res, err := a.sessions.Kill(cmd.Context(), args[0], core.KillOptions{Grace: grace})
				if err != nil {
					return err
				}
				printKill(cmd.OutOrStdout(), res, grace)
				return nil
			}
			results, err := a.sessions.KillAll(cmd.Context(), workspace, core.KillOptions{Grace: grace})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(results) == 0 {
				fmt.Fprintln(out, "No running sessions.")
				return nil
			}
			failed := 0
			for _, r := range results {
				if r.Err != nil {
					failed++
					fmt.Fprintf(out, "%s: %v\n", r.Session.ID, r.Err)
					continue
				}
				printKill(out, r.Result, grace)
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d sessions could not be stopped", failed, len(results))
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&grace, "grace", 5*time.Second, "time to wait for a clean exit before SIGKILL")
	cmd.Flags().BoolVar(&all, "all", false, "stop every running session")
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "stop every running session in this workspace")
	_ = cmd.RegisterFlagCompletionFunc("workspace", completeWorkspaces(a))
	return cmd
}

func printKill(out io.Writer, res *core.KillResult, grace time.Duration) {
	label := core.WindowName(res.Session.Task, res.Session.ID)
	switch res.Method {
	case "already-exited":
		if res.WindowGone {
			fmt.Fprintf(out, "%s (%s) had already exited; removed its tmux window.\n", res.Session.ID, label)
		} else {
			fmt.Fprintf(out, "%s (%s) has no running process or window; nothing to stop.\n", res.Session.ID, label)
		}
	case "terminated":
		fmt.Fprintf(out, "Stopped %s (%s): clean exit.\n", res.Session.ID, label)
	case "killed":
		fmt.Fprintf(out, "Stopped %s (%s): forced after %s.\n", res.Session.ID, label, grace)
	}
	if res.Session.DevinSessionID != "" {
		fmt.Fprintf(out, "  resume with: dmux resume %s\n", res.Session.ID)
	}
}
