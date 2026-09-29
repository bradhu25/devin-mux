package ui

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

// Badge returns a one-glyph marker for a display state. Glyphs only; the
// semantics come from core.Display.
func Badge(label string) string {
	switch label {
	case "working":
		return "●"
	case "awaiting-approval":
		return "!"
	case "idle":
		return "○"
	case "starting":
		return "◌"
	case "exited":
		return "×"
	case "failed":
		return "✗"
	}
	return "?"
}

// RenderSnapshot writes the workspace → session table used by `dmux ls`.
func RenderSnapshot(w io.Writer, snap *core.Snapshot, verbose bool) error {
	if len(snap.Workspaces) == 0 {
		_, err := fmt.Fprintln(w, "No workspaces. Create one with: dmux workspace new <name> --repo <path>")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKSPACE\tSESSION\tSTATUS\tTASK\tLAST EVENT\tDEVIN")
	for _, wv := range snap.Workspaces {
		if len(wv.Sessions) == 0 {
			fmt.Fprintf(tw, "%s\t-\t%s\t(no sessions)\t\t\n", wv.Workspace.Name, string(wv.Workspace.Status))
			continue
		}
		for _, sv := range wv.Sessions {
			label := core.Display(sv.Status, sv.Liveness)
			status := Badge(label) + " " + label
			if label == "awaiting-approval" && len(sv.Status.Pending) > 0 {
				status += " (" + sv.Status.Pending[0].ToolName + ")"
			}
			last := ""
			if !sv.Status.LastEventAt.IsZero() {
				last = fmt.Sprintf("%s %s", sv.Status.LastEventType, ago(snap.TakenAt.Sub(sv.Status.LastEventAt)))
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", wv.Workspace.Name, sv.Session.ID, status, core.WindowName(sv.Session.Task, ""), last, sv.Status.DevinSessionID)
			if verbose {
				for _, e := range sv.Evidence {
					fmt.Fprintf(tw, "\t\t  · %s\t\t\t\n", e)
				}
				if sv.Status.LastMessage != "" {
					fmt.Fprintf(tw, "\t\t  » %s\t\t\t\n", truncate(sv.Status.LastMessage, 60))
				}
			}
		}
	}
	return tw.Flush()
}

func ago(d time.Duration) string {
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
