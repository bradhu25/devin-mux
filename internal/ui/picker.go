// Package ui holds interactive terminal presentation. Phase 1: a huh-based
// picker; Phase 2: the Bubble Tea dashboard. It renders core view models and
// never touches git, tmux, or state directly.
package ui

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"

	"github.com/bradhu25/devin-mux/internal/core"
)

// ErrCancelled is returned when the user aborts the picker.
var ErrCancelled = errors.New("cancelled")

// PickSession shows a grouped select over live sessions and returns the
// chosen one. Grouping is by workspace name (candidates arrive sorted).
func PickSession(cands []core.JumpCandidate) (core.JumpCandidate, error) {
	if len(cands) == 0 {
		return core.JumpCandidate{}, errors.New("no live sessions")
	}
	opts := make([]huh.Option[int], 0, len(cands))
	lastWS := ""
	for i, c := range cands {
		prefix := "  "
		if c.Workspace.Name != lastWS {
			prefix = c.Workspace.Name + " › "
			lastWS = c.Workspace.Name
		}
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s%s  %s", prefix, labelTask(c), c.Session.ID), i))
	}
	var idx int
	sel := huh.NewSelect[int]().Title("Jump to session").Options(opts...).Value(&idx)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return core.JumpCandidate{}, ErrCancelled
		}
		return core.JumpCandidate{}, err
	}
	return cands[idx], nil
}

func labelTask(c core.JumpCandidate) string {
	if c.Session.Task == "" {
		return "(no task)"
	}
	return core.WindowName(c.Session.Task, c.Session.ID)
}
