package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bradhu25/devin-mux/internal/core"
	"github.com/bradhu25/devin-mux/internal/state"
)

// completionTmux is a core.Tmux that only answers ListWindows.
type completionTmux struct {
	core.Tmux
	windows []core.TmuxWindow
}

func (c completionTmux) ListWindows(context.Context) ([]core.TmuxWindow, error) {
	return c.windows, nil
}

func completionApp(t *testing.T) *app {
	t.Helper()
	t.Setenv(state.EnvRoot, t.TempDir())
	st, err := state.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	err = st.Update(context.Background(), func(s *core.State) error {
		s.Workspaces = []core.Workspace{
			{ID: "ws_1", Name: "feature-x", Status: core.WorkspaceReady, Repos: []core.WorkspaceRepo{{Name: "api"}, {Name: "web"}}},
			{ID: "ws_2", Name: "payment-fix", Status: core.WorkspaceFailed, Repos: []core.WorkspaceRepo{{Name: "api"}}},
		}
		s.Sessions = []core.Session{
			{ID: "s_live01", WorkspaceID: "ws_1", Task: "auth implementation", Tmux: core.TmuxTarget{WindowID: "@1"}},
			{ID: "s_gone01", WorkspaceID: "ws_1", Task: "finished work", DevinSessionID: "olive-turkey", Tmux: core.TmuxTarget{WindowID: "@2"}},
			{ID: "s_noid01", WorkspaceID: "ws_2", Task: "never hooked", Tmux: core.TmuxTarget{WindowID: "@3"}},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &app{}
	a.once.Do(func() {}) // mark initialized; inject fakes
	a.store = st
	a.tmux = completionTmux{windows: []core.TmuxWindow{{WindowID: "@1", DmuxSession: "s_live01"}}}
	return a
}

// complete runs cobra's __complete protocol exactly as a shell would.
func complete(t *testing.T, a *app, args ...string) []string {
	t.Helper()
	root := newRootCmd("test")
	// Rebuild commands against the injected app.
	root.ResetCommands()
	root.AddCommand(newWorkspaceCmd(a), newSpawnCmd(a), newJumpCmd(a), newKillCmd(a), newResumeCmd(a))
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut) // cobra prints the directive trailer to stderr
	root.SetArgs(append([]string{cobraCompleteCmd}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("__complete %v: %v\n%s%s", args, err, out.String(), errOut.String())
	}
	var lines []string
	for _, l := range strings.Split(out.String(), "\n") {
		if l != "" && !strings.HasPrefix(l, ":") {
			lines = append(lines, l)
		}
	}
	return lines
}

const cobraCompleteCmd = "__complete"

func values(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i], _, _ = strings.Cut(l, "\t")
	}
	return out
}

func TestCompletion_Workspaces(t *testing.T) {
	a := completionApp(t)
	got := complete(t, a, "spawn", "")
	if strings.Join(values(got), ",") != "feature-x,payment-fix" {
		t.Fatalf("spawn: %v", got)
	}
	if !strings.Contains(got[0], "api,web") || !strings.Contains(got[1], "(failed)") {
		t.Fatalf("descriptions: %v", got)
	}
	if got := values(complete(t, a, "spawn", "pay")); len(got) != 1 || got[0] != "payment-fix" {
		t.Fatalf("prefix filter: %v", got)
	}
	if got := complete(t, a, "workspace", "rm", ""); len(got) != 2 {
		t.Fatalf("workspace rm: %v", got)
	}
	// Second positional: nothing.
	if got := complete(t, a, "spawn", "feature-x", ""); len(got) != 0 {
		t.Fatalf("no second arg completion expected: %v", got)
	}
}

func TestCompletion_SessionsByCommand(t *testing.T) {
	a := completionApp(t)
	if got := values(complete(t, a, "jump", "")); strings.Join(got, ",") != "s_live01" {
		t.Fatalf("jump should offer live only: %v", got)
	}
	if got := values(complete(t, a, "kill", "")); strings.Join(got, ",") != "s_live01" {
		t.Fatalf("kill should offer live only: %v", got)
	}
	got := complete(t, a, "resume", "")
	if strings.Join(values(got), ",") != "s_gone01" {
		t.Fatalf("resume should offer exited sessions with a devin id only: %v", got)
	}
	if !strings.Contains(got[0], "exited") || !strings.Contains(got[0], "finished work") || !strings.Contains(got[0], "feature-x") {
		t.Fatalf("resume description: %v", got)
	}
}
