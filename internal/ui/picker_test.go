package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bradhu25/devin-mux/internal/core"
)

func TestSessionLabels_AlignedColumnsEveryRowCarriesWorkspace(t *testing.T) {
	cands := []core.JumpCandidate{
		{Workspace: core.Workspace{Name: "feature-x"}, Session: core.Session{ID: "s_000001", Task: "Reply with exactly ALPHA. No tools."}},
		{Workspace: core.Workspace{Name: "feature-x"}, Session: core.Session{ID: "s_000002", Task: "BRAVO"}},
		{Workspace: core.Workspace{Name: "payment-fix"}, Session: core.Session{ID: "s_000003"}},
	}
	labels := SessionLabels(cands)
	if len(labels) != 3 {
		t.Fatalf("got %d labels", len(labels))
	}
	// Each row starts with its own workspace name (no header-style first row).
	for i, want := range []string{"feature-x", "feature-x", "payment-fix"} {
		if !strings.HasPrefix(labels[i], want) {
			t.Errorf("row %d = %q, want prefix %q", i, labels[i], want)
		}
	}
	// The session id column starts at the same rune offset on every row,
	// even though row 0's task was truncated with a multi-byte ellipsis.
	col := -1
	for i, l := range labels {
		idx := strings.Index(l, "s_00000")
		if idx < 0 {
			t.Fatalf("row %d lacks id: %q", i, l)
		}
		off := utf8.RuneCountInString(l[:idx])
		if col == -1 {
			col = off
		} else if off != col {
			t.Errorf("row %d id at rune %d, row 0 at %d:\n%s", i, off, col, strings.Join(labels, "\n"))
		}
	}
	if !strings.Contains(labels[0], "…") || !strings.Contains(labels[2], "(no task)") {
		t.Fatalf("truncation / empty-task rendering wrong:\n%s", strings.Join(labels, "\n"))
	}
}
