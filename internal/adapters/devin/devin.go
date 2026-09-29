// Package devin implements core.Devin for the Devin CLI. It is the single
// place that encodes the CLI's argument contract, which is version-specific
// (verified against 3000.11.3 — see PLAN.md Spikes 1/2/4). Re-verify with
// `devin --help` after upgrading.
package devin

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/bradhu25/devin-mux/internal/core"
)

// Adapter builds Devin invocations and reads Devin's session store. Zero
// value uses "devin" on PATH and the default store path.
type Adapter struct {
	Bin string
	// StorePath overrides the sessions.db location (tests).
	StorePath string
}

var _ core.Devin = (*Adapter)(nil)

func (a *Adapter) bin() string {
	if a.Bin != "" {
		return a.Bin
	}
	return "devin"
}

func (a *Adapter) Available(_ context.Context) error {
	if _, err := exec.LookPath(a.bin()); err != nil {
		return fmt.Errorf("devin CLI not found on PATH: %w", err)
	}
	return nil
}

// LaunchArgs builds:
//
//	devin --respect-workspace-trust false [--permission-mode M] [--model M] [-r ID] [-- PROMPT]
//
// Workspace trust is skipped because dmux creates the workspace directory
// itself moments earlier; the trust prompt would otherwise block every
// spawn behind an interactive question the user cannot see (Spike 1).
// `-r ID -- PROMPT` resumes and submits the prompt in one go (Spike 1).
func (a *Adapter) LaunchArgs(spec core.LaunchSpec) []string {
	args := []string{a.bin(), "--respect-workspace-trust", "false"}
	if spec.PermissionMode != "" {
		args = append(args, "--permission-mode", spec.PermissionMode)
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.ResumeID != "" {
		args = append(args, "-r", spec.ResumeID)
	}
	if spec.Prompt != "" {
		args = append(args, "--", spec.Prompt)
	}
	return args
}
