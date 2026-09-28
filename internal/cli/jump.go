package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/core"
	"github.com/bradhu25/devin-mux/internal/ui"
)

func newJumpCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "jump [query]",
		Short: "Switch to a running Devin session",
		Long: `Switch the terminal to a running session's tmux window. Inside tmux the current
client is switched; outside tmux this attaches to the session.

With no argument an interactive picker lists every live session grouped by
workspace. A query matches a session id, id prefix, or task text.`,
		Example: `  dmux jump
  dmux jump s_0a7a
  dmux jump auth`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			live, gone, err := a.sessions.Candidates(cmd.Context())
			if err != nil {
				return err
			}
			if len(live) == 0 {
				if len(gone) > 0 {
					return fmt.Errorf("no live sessions (%d recorded session(s) have no tmux window; `dmux resume` is coming in M4)", len(gone))
				}
				return errors.New("no sessions yet; start one with: dmux spawn <workspace> -t \"task\"")
			}
			var c core.JumpCandidate
			if len(args) == 1 {
				c, err = core.Resolve(live, gone, args[0])
			} else {
				c, err = ui.PickSession(live)
			}
			if err != nil {
				if errors.Is(err, ui.ErrCancelled) {
					return nil
				}
				return err
			}
			return a.sessions.Jump(cmd.Context(), c)
		},
	}
}
