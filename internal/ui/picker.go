// Package ui holds interactive terminal presentation. Phase 1: a huh-based
// picker; Phase 2: the Bubble Tea dashboard. It renders core view models and
// never touches git, tmux, or state directly.
package ui

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/huh"

	"github.com/bradhu25/devin-mux/internal/core"
)

// ErrCancelled is returned when the user aborts the picker.
var ErrCancelled = errors.New("cancelled")

// PickSession shows a select over live sessions and returns the chosen one.
// Candidates arrive sorted by workspace then creation time (core.Candidates).
func PickSession(cands []core.JumpCandidate) (core.JumpCandidate, error) {
	if len(cands) == 0 {
		return core.JumpCandidate{}, errors.New("no live sessions")
	}
	labels := SessionLabels(cands)
	opts := make([]huh.Option[int], len(cands))
	for i := range cands {
		opts[i] = huh.NewOption(labels[i], i)
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

// SessionLabels renders one aligned row per candidate:
//
//	<workspace>  <task…>  <session id>
//
// Every row carries its workspace name so rows are self-describing and
// selectable; a select widget cannot have non-selectable header rows.
func SessionLabels(cands []core.JumpCandidate) []string {
	wsWidth, taskWidth := 0, 0
	tasks := make([]string, len(cands))
	for i, c := range cands {
		tasks[i] = labelTask(c)
		wsWidth = max(wsWidth, utf8.RuneCountInString(c.Workspace.Name))
		taskWidth = max(taskWidth, utf8.RuneCountInString(tasks[i]))
	}
	labels := make([]string, len(cands))
	for i, c := range cands {
		labels[i] = fmt.Sprintf("%s  %s  %s", pad(c.Workspace.Name, wsWidth), pad(tasks[i], taskWidth), c.Session.ID)
	}
	return labels
}

// pad right-pads s with spaces to width runes (fmt's %-*s counts bytes,
// which misaligns the "…" ellipsis).
func pad(s string, width int) string {
	if n := width - utf8.RuneCountInString(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func labelTask(c core.JumpCandidate) string {
	if c.Session.Task == "" {
		return "(no task)"
	}
	return core.WindowName(c.Session.Task, c.Session.ID)
}
