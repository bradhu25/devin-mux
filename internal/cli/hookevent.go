package cli

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/hooks"
	"github.com/bradhu25/devin-mux/internal/state"
)

func newHookEventCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "hook-event",
		Short:  "Receive a Devin lifecycle hook payload on stdin (installed by dmux init)",
		Hidden: true,
		// Passive observer: this command must never block or alter the agent.
		// Every failure path is swallowed and we always exit 0 with no stdout.
		RunE: func(cmd *cobra.Command, args []string) error {
			payload, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			if err != nil {
				return nil
			}
			rec := &hooks.Recorder{}
			if store, err := state.OpenDefault(); err == nil {
				rec.Store = store
			}
			_ = rec.Record(hooks.Input{
				DmuxSessionID: os.Getenv("DMUX_SESSION_ID"),
				ProjectDir:    os.Getenv("DEVIN_PROJECT_DIR"),
				Payload:       payload,
			})
			return nil
		},
	}
}
