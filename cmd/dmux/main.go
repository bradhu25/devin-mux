// Command dmux is a worktree-session orchestrator for running multiple Devin
// CLI sessions concurrently. See PLAN.md for the architecture.
package main

import (
	"fmt"
	"os"

	"github.com/bradhu25/devin-mux/internal/cli"
)

// Set via -ldflags "-X main.version=..." by the Makefile / goreleaser.
var version = "dev"

func main() {
	if err := cli.Execute(version); err != nil {
		fmt.Fprintln(os.Stderr, "dmux:", err)
		os.Exit(1)
	}
}
