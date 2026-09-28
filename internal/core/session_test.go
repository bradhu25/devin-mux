package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeTmux is an in-memory core.Tmux.
type fakeTmux struct {
	available  error
	spawnErr   error
	spawned    []SpawnWindowOpts
	killed     []string
	nextWindow int
	inside     bool
	switched   []TmuxTarget
}

func (f *fakeTmux) Available(context.Context) error { return f.available }
func (f *fakeTmux) SpawnWindow(_ context.Context, o SpawnWindowOpts) (TmuxTarget, error) {
	if f.spawnErr != nil {
		return TmuxTarget{}, f.spawnErr
	}
	f.spawned = append(f.spawned, o)
	f.nextWindow++
	return TmuxTarget{SessionName: o.SessionName, SessionID: "$1", WindowID: "@" + string(rune('0'+f.nextWindow))}, nil
}
func (f *fakeTmux) ListWindows(context.Context) ([]TmuxWindow, error) { return nil, nil }
func (f *fakeTmux) KillWindow(_ context.Context, id string) error {
	f.killed = append(f.killed, id)
	return nil
}
func (f *fakeTmux) InsideTmux() bool { return f.inside }
func (f *fakeTmux) SwitchClient(_ context.Context, t TmuxTarget) error {
	f.switched = append(f.switched, t)
	return nil
}
func (f *fakeTmux) Attach(TmuxTarget) error { return errors.New("fake attach") }

type fakeDevin struct{ available error }

func (f *fakeDevin) Available(context.Context) error { return f.available }
func (f *fakeDevin) LaunchArgs(s LaunchSpec) []string {
	args := []string{"devin"}
	if s.ResumeID != "" {
		args = append(args, "-r", s.ResumeID)
	}
	if s.Prompt != "" {
		args = append(args, "--", s.Prompt)
	}
	return args
}

func newSessionManager(store *fakeStore, tm *fakeTmux) *SessionManager {
	return &SessionManager{Tmux: tm, Devin: &fakeDevin{}, Store: store, DmuxBin: "/usr/local/bin/dmux",
		Now: func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }}
}

func readyStore() *fakeStore {
	return &fakeStore{state: State{Workspaces: []Workspace{{ID: "ws_1", Name: "feature-x", Root: "/r/feature-x", Status: WorkspaceReady}}}}
}

func TestSpawn_HappyPath(t *testing.T) {
	store, tm := readyStore(), &fakeTmux{}
	m := newSessionManager(store, tm)

	res, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "Fix the auth bug"})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Session
	if !strings.HasPrefix(s.ID, "s_") || s.WorkspaceID != "ws_1" || s.Task != "Fix the auth bug" || s.Tmux.WindowID != "@1" {
		t.Fatalf("session: %+v", s)
	}
	if res.OtherRunning != 0 {
		t.Fatalf("OtherRunning = %d", res.OtherRunning)
	}
	// Window spawned with the right shape.
	if len(tm.spawned) != 1 {
		t.Fatalf("spawned %d windows", len(tm.spawned))
	}
	o := tm.spawned[0]
	if o.SessionName != "dmux-feature-x" || o.WorkspaceID != "ws_1" || o.SessionID != s.ID || o.Cwd != "/r/feature-x" || o.Env["DMUX_SESSION_ID"] != s.ID {
		t.Fatalf("spawn opts: %+v", o)
	}
	wantArgv := []string{"/usr/local/bin/dmux", "run", "--session", s.ID, "--dir", "/r/feature-x", "--", "devin", "--", "Fix the auth bug"}
	if strings.Join(o.Argv, " ") != strings.Join(wantArgv, " ") {
		t.Fatalf("argv:\n got %q\nwant %q", o.Argv, wantArgv)
	}
	if o.WindowName != "Fix the auth bug" {
		t.Fatalf("window name %q", o.WindowName)
	}
	// Persisted with target.
	got := store.state.Session(s.ID)
	if got == nil || got.Tmux != s.Tmux {
		t.Fatalf("persisted: %+v", got)
	}
}

func TestSpawn_ByWorkspaceID_AndCountsOthers(t *testing.T) {
	store, tm := readyStore(), &fakeTmux{}
	store.state.Sessions = []Session{{ID: "s_old", WorkspaceID: "ws_1"}}
	m := newSessionManager(store, tm)
	res, err := m.Spawn(context.Background(), SpawnInput{Workspace: "ws_1", Task: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if res.OtherRunning != 1 {
		t.Fatalf("OtherRunning = %d, want 1", res.OtherRunning)
	}
}

func TestSpawn_Preconditions(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeStore, *fakeTmux, *SessionManager)
		in    SpawnInput
		want  error
		wantS string
	}{
		{"unknown workspace", nil, SpawnInput{Workspace: "nope"}, ErrWorkspaceNotFound, ""},
		{"workspace failed", func(s *fakeStore, _ *fakeTmux, _ *SessionManager) { s.state.Workspaces[0].Status = WorkspaceFailed }, SpawnInput{Workspace: "feature-x"}, ErrWorkspaceNotReady, "dmux doctor"},
		{"tmux missing", func(_ *fakeStore, tm *fakeTmux, _ *SessionManager) { tm.available = errors.New("no tmux") }, SpawnInput{Workspace: "feature-x"}, nil, "no tmux"},
		{"devin missing", func(_ *fakeStore, _ *fakeTmux, m *SessionManager) {
			m.Devin = &fakeDevin{available: errors.New("no devin")}
		}, SpawnInput{Workspace: "feature-x"}, nil, "no devin"},
		{"no dmux bin", func(_ *fakeStore, _ *fakeTmux, m *SessionManager) { m.DmuxBin = "" }, SpawnInput{Workspace: "feature-x"}, nil, "not configured"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, tm := readyStore(), &fakeTmux{}
			m := newSessionManager(store, tm)
			if tc.setup != nil {
				tc.setup(store, tm, m)
			}
			_, err := m.Spawn(context.Background(), tc.in)
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if tc.wantS != "" && !strings.Contains(err.Error(), tc.wantS) {
				t.Fatalf("want %q in %v", tc.wantS, err)
			}
			if len(tm.spawned) != 0 || len(store.state.Sessions) != 0 {
				t.Fatal("precondition failure must have no side effects")
			}
		})
	}
}

func TestSpawn_TmuxFailure_RemovesReservation(t *testing.T) {
	store, tm := readyStore(), &fakeTmux{spawnErr: errors.New("server exploded")}
	m := newSessionManager(store, tm)
	_, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "x"})
	if err == nil || !strings.Contains(err.Error(), "server exploded") {
		t.Fatalf("got %v", err)
	}
	if len(store.state.Sessions) != 0 {
		t.Fatalf("reservation must be removed: %+v", store.state.Sessions)
	}
}

func TestSpawn_PersistFailure_KillsWindow(t *testing.T) {
	store, tm := readyStore(), &fakeTmux{}
	m := newSessionManager(store, tm)
	// First Update (reserve) succeeds; make the second (persist target) fail.
	store.failAfter = 1
	store.failWith = errors.New("disk full")
	_, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "x"})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("got %v", err)
	}
	if len(tm.killed) != 1 || tm.killed[0] != "@1" {
		t.Fatalf("window must be killed when the record cannot be saved: killed=%v", tm.killed)
	}
	if len(store.state.Sessions) != 0 {
		t.Fatalf("reservation must be removed: %+v", store.state.Sessions)
	}
}

func TestWindowName(t *testing.T) {
	cases := map[string]string{
		"Fix the auth bug":           "Fix the auth bug",
		"  spaces\n and\tnewlines  ": "spaces and newlines",
		"":                           "s_abc",
		"a very long task description that goes on": "a very long task descri…",
		"target:with.separators":                    "target with separators",
	}
	for in, want := range cases {
		if got := WindowName(in, "s_abc"); got != want {
			t.Errorf("WindowName(%q) = %q, want %q", in, got, want)
		}
	}
}
