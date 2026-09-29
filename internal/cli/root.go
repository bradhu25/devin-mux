// Package cli wires cobra commands to core services. Commands are thin:
// parse flags, call core, render the result.
package cli

import (
	"github.com/spf13/cobra"
)

// Execute runs the root command with the given build version.
func Execute(version string) error {
	return newRootCmd(version).Execute()
}

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "dmux",
		Short:         "Run multiple Devin CLI sessions across isolated git worktrees",
		Long:          "dmux organizes Devin CLI sessions into workspaces (sets of git worktrees) and lets you spawn, observe, and jump between them.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	a := &app{}
	root.AddCommand(newHookEventCmd(), newRunCmd(), newInitCmd(a), newWorkspaceCmd(a), newSpawnCmd(a), newJumpCmd(a), newLsCmd(a), newKillCmd(a))
	return root
}
