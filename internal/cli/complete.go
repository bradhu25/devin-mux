package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/core"
)

// Completion functions read state only and must stay fast: shells call them
// on every <TAB>. They never write, never spawn Devin, and time out quickly.
const completionTimeout = 2 * time.Second

// completeWorkspaces suggests workspace names (ready ones first).
func completeWorkspaces(a *app) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		if err := a.init(); err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		st, err := a.store.Read()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var out []string
		for _, ws := range st.Workspaces {
			if !strings.HasPrefix(ws.Name, toComplete) {
				continue
			}
			repos := make([]string, len(ws.Repos))
			for i, r := range ws.Repos {
				repos[i] = r.Name
			}
			desc := strings.Join(repos, ",")
			if ws.Status != core.WorkspaceReady {
				desc += " (" + string(ws.Status) + ")"
			}
			out = append(out, ws.Name+"\t"+desc)
		}
		sort.Strings(out)
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// sessionFilter selects which sessions a command can act on.
type sessionFilter int

const (
	sessionsAll    sessionFilter = iota
	sessionsLive                 // jump, kill: window alive
	sessionsExited               // resume: not running and has a conversation id
)

// completeSessions suggests session ids with "<status>  <task>" descriptions.
// Liveness comes from tmux tags only (no event-log reduce): fast and enough
// to separate live from gone.
func completeSessions(a *app, filter sessionFilter) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		if err := a.init(); err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
		defer cancel()
		st, err := a.store.Read()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		live := map[string]bool{}
		if wins, err := a.tmux.ListWindows(ctx); err == nil {
			for _, w := range wins {
				if w.DmuxSession != "" && !w.PaneDead {
					live[w.DmuxSession] = true
				}
			}
		}
		var out []string
		for _, s := range st.Sessions {
			isLive := live[s.ID] && s.Tmux.WindowID != ""
			switch filter {
			case sessionsLive:
				if !isLive {
					continue
				}
			case sessionsExited:
				if isLive || s.DevinSessionID == "" {
					continue
				}
			}
			if !strings.HasPrefix(s.ID, toComplete) {
				continue
			}
			ws := "?"
			if w := st.Workspace(s.WorkspaceID); w != nil {
				ws = w.Name
			}
			state := "exited"
			if isLive {
				state = "live"
			}
			out = append(out, fmt.Sprintf("%s\t%s  %s  %s", s.ID, ws, state, core.WindowName(s.Task, "")))
		}
		sort.Strings(out)
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}
