package tmux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

// Tier 2 integration tests against a real tmux server on an isolated
// socket (-L dmux-test-<random>), killed at cleanup. They never touch the
// user's tmux server.

func newAdapter(t *testing.T) *Adapter {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	a := &Adapter{Socket: "dmux-test-" + hex.EncodeToString(b)}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// tmux does not unlink its socket on kill-server; ask for the path
		// while the server is up, then remove it so tests leave no residue.
		sockPath, _ := a.run(ctx, "display-message", "-p", "#{socket_path}")
		_, _ = a.run(ctx, "kill-server")
		if sockPath != "" {
			_ = os.Remove(sockPath)
		}
	})
	return a
}

func TestAvailable(t *testing.T) {
	a := newAdapter(t)
	if err := a.Available(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := &Adapter{Bin: "/nonexistent/tmux"}
	if err := bad.Available(context.Background()); err == nil {
		t.Fatal("missing binary must error")
	}
}

func TestListWindows_NoServer(t *testing.T) {
	a := newAdapter(t)
	wins, err := a.ListWindows(context.Background())
	if err != nil || len(wins) != 0 {
		t.Fatalf("no server should yield empty list, got %v %v", wins, err)
	}
}

func TestSpawnWindow_CreatesSessionThenReusesIt(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()
	cwd := t.TempDir()
	marker := filepath.Join(cwd, "env.txt")

	// First spawn: creates the session; the command records its env + cwd.
	t1, err := a.SpawnWindow(ctx, core.SpawnWindowOpts{
		SessionName: "dmux-feature.x", WorkspaceID: "ws_abc", WindowName: "auth", SessionID: "s_1",
		Cwd: cwd, Env: map[string]string{"DMUX_SESSION_ID": "s_1"},
		Argv: []string{"sh", "-c", "echo \"$DMUX_SESSION_ID $(pwd)\" > " + shellQuote(marker) + "; sleep 60"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if t1.SessionName != "dmux-feature_x" || !strings.HasPrefix(t1.SessionID, "$") || !strings.HasPrefix(t1.WindowID, "@") {
		t.Fatalf("unexpected target: %+v", t1)
	}

	// Second spawn in the same workspace: reuses the session, new window.
	t2, err := a.SpawnWindow(ctx, core.SpawnWindowOpts{
		SessionName: "dmux-feature.x", WorkspaceID: "ws_abc", WindowName: "review", SessionID: "s_2",
		Cwd: cwd, Argv: []string{"sleep", "60"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if t2.SessionID != t1.SessionID || t2.WindowID == t1.WindowID {
		t.Fatalf("second spawn should share session and get a new window: %+v vs %+v", t1, t2)
	}

	// Env and cwd reached the command.
	deadline := time.Now().Add(3 * time.Second)
	var content []byte
	for time.Now().Before(deadline) {
		if content, err = os.ReadFile(marker); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// tmux -c passes the path as given; sh's pwd may or may not resolve
	// symlinks (macOS /var -> /private/var), so compare resolved forms.
	got := strings.TrimSpace(string(content))
	gotID, gotCwd, _ := strings.Cut(got, " ")
	wantCwd, _ := filepath.EvalSymlinks(cwd)
	resolvedGot, _ := filepath.EvalSymlinks(gotCwd)
	if gotID != "s_1" || resolvedGot != wantCwd {
		t.Fatalf("env/cwd not propagated: %q (want id s_1, cwd %s)", got, wantCwd)
	}

	// Tags resolved in a single ListWindows call.
	wins, err := a.ListWindows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]core.TmuxWindow{}
	for _, w := range wins {
		byID[w.WindowID] = w
	}
	w1, w2 := byID[t1.WindowID], byID[t2.WindowID]
	if w1.DmuxSession != "s_1" || w2.DmuxSession != "s_2" || w1.WorkspaceID != "ws_abc" || w2.WorkspaceID != "ws_abc" {
		t.Fatalf("tags missing: %+v %+v", w1, w2)
	}
	if w1.WindowName != "auth" || w1.PanePID == 0 || w1.PaneDead {
		t.Fatalf("window metadata: %+v", w1)
	}

	// Kill one window; the other survives.
	if err := a.KillWindow(ctx, t2.WindowID); err != nil {
		t.Fatal(err)
	}
	wins, _ = a.ListWindows(ctx)
	if len(wins) != 1 || wins[0].WindowID != t1.WindowID {
		t.Fatalf("after kill: %+v", wins)
	}
}

// remain-on-exit keeps the window (and thus the session) alive after the
// command exits, so the tmux target never vanishes under a Session record.
func TestSpawnWindow_RemainOnExitKeepsDeadPane(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()
	tgt, err := a.SpawnWindow(ctx, core.SpawnWindowOpts{SessionName: "dmux-x", SessionID: "s_dead", Argv: []string{"sh", "-c", "exit 3"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		wins, _ := a.ListWindows(ctx)
		if len(wins) == 1 && wins[0].WindowID == tgt.WindowID && wins[0].PaneDead {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	wins, _ := a.ListWindows(ctx)
	t.Fatalf("expected dead pane to remain, got %+v", wins)
}

func TestSpawnWindow_Validation(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()
	if _, err := a.SpawnWindow(ctx, core.SpawnWindowOpts{SessionName: "x", SessionID: "s"}); err == nil {
		t.Fatal("empty argv must be rejected")
	}
	if _, err := a.SpawnWindow(ctx, core.SpawnWindowOpts{SessionName: "x", Argv: []string{"true"}}); err == nil {
		t.Fatal("missing SessionID must be rejected")
	}
	if err := a.KillWindow(ctx, "not-an-id"); err == nil {
		t.Fatal("KillWindow must require an @N id")
	}
}

func TestInsideTmux(t *testing.T) {
	a := &Adapter{LookupEnv: func(k string) (string, bool) { return "/tmp/tmux-1/default,123,0", k == "TMUX" }}
	if !a.InsideTmux() {
		t.Fatal("TMUX set -> inside")
	}
	a.LookupEnv = func(string) (string, bool) { return "", false }
	if a.InsideTmux() {
		t.Fatal("TMUX unset -> outside")
	}
}

func TestSwitchClient_NoClientIsAnError(t *testing.T) {
	a := newAdapter(t)
	ctx := context.Background()
	tgt, err := a.SpawnWindow(ctx, core.SpawnWindowOpts{SessionName: "dmux-x", SessionID: "s_1", Argv: []string{"sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	// No client is attached to this isolated server, so tmux must refuse.
	err = a.SwitchClient(ctx, tgt)
	var te *Error
	if err == nil || !strings.Contains(err.Error(), "client") {
		t.Fatalf("expected 'no current client' style error, got %v (%T %v)", err, err, te)
	}
}

func TestShellJoin(t *testing.T) {
	cases := map[string][]string{
		"devin --respect-workspace-trust false -- 'fix the auth bug'": {"devin", "--respect-workspace-trust", "false", "--", "fix the auth bug"},
		`echo 'it'\''s'`:              {"echo", "it's"},
		"a ''":                        {"a", ""},
		"/usr/bin/x -e K=v":           {"/usr/bin/x", "-e", "K=v"},
		`sh -c 'echo $HOME; ls | wc'`: {"sh", "-c", "echo $HOME; ls | wc"},
	}
	for want, argv := range cases {
		if got := ShellJoin(argv); got != want {
			t.Errorf("ShellJoin(%q) = %q, want %q", argv, got, want)
		}
	}
}

func TestSanitizeSessionName(t *testing.T) {
	if got := SanitizeSessionName("dmux-v1.2:x"); got != "dmux-v1_2_x" {
		t.Fatalf("got %q", got)
	}
}

func TestParseListWindows(t *testing.T) {
	out := "$0\tdmux-a\t@0\tauth\t123\t0\tws_1\ts_1\n$0\tdmux-a\t@1\treview\t124\t1\tws_1\t\n$1\tuser\t@2\tzsh\t9\t0\t\t\n"
	wins := parseListWindows(out)
	if len(wins) != 3 {
		t.Fatalf("got %d windows", len(wins))
	}
	if wins[0].DmuxSession != "s_1" || wins[0].PanePID != 123 || wins[0].PaneDead {
		t.Fatalf("w0: %+v", wins[0])
	}
	if wins[1].DmuxSession != "" || !wins[1].PaneDead {
		t.Fatalf("w1: %+v", wins[1])
	}
	if wins[2].WorkspaceID != "" || wins[2].SessionName != "user" {
		t.Fatalf("w2 (untagged user window): %+v", wins[2])
	}
}
