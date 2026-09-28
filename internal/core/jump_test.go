package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type listingTmux struct {
	fakeTmux
	windows []TmuxWindow
}

func (l *listingTmux) ListWindows(context.Context) ([]TmuxWindow, error) { return l.windows, nil }

func jumpFixture() (*fakeStore, *listingTmux) {
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{state: State{
		Workspaces: []Workspace{{ID: "ws_b", Name: "payment-fix", Status: WorkspaceReady}, {ID: "ws_a", Name: "feature-x", Status: WorkspaceReady}},
		Sessions: []Session{
			{ID: "s_1", WorkspaceID: "ws_a", Task: "auth implementation", CreatedAt: t0, Tmux: TmuxTarget{SessionID: "$1", WindowID: "@1"}},
			{ID: "s_2", WorkspaceID: "ws_a", Task: "security review", CreatedAt: t0.Add(time.Minute), Tmux: TmuxTarget{SessionID: "$1", WindowID: "@2"}},
			{ID: "s_3", WorkspaceID: "ws_b", Task: "validation tests", CreatedAt: t0, Tmux: TmuxTarget{SessionID: "$2", WindowID: "@3"}},
			{ID: "s_4", WorkspaceID: "ws_a", Task: "gone window", CreatedAt: t0, Tmux: TmuxTarget{SessionID: "$1", WindowID: "@9"}},
			{ID: "s_5", WorkspaceID: "ws_a", Task: "reused id, wrong tag", CreatedAt: t0, Tmux: TmuxTarget{SessionID: "$1", WindowID: "@5"}},
		},
	}}
	tm := &listingTmux{windows: []TmuxWindow{
		{SessionID: "$1", SessionName: "dmux-feature-x", WindowID: "@1", DmuxSession: "s_1", WorkspaceID: "ws_a"},
		{SessionID: "$1", SessionName: "dmux-feature-x", WindowID: "@2", DmuxSession: "s_2", WorkspaceID: "ws_a"},
		{SessionID: "$2", SessionName: "dmux-payment-fix", WindowID: "@3", DmuxSession: "s_3", WorkspaceID: "ws_b"},
		{SessionID: "$1", SessionName: "dmux-feature-x", WindowID: "@5", DmuxSession: "s_other", WorkspaceID: "ws_a"}, // id reused after server restart
		{SessionID: "$7", SessionName: "user-stuff", WindowID: "@7"},                                                  // untagged user window
	}}
	return store, tm
}

func TestCandidates_ValidatesTagAndId_GroupsByWorkspace(t *testing.T) {
	store, tm := jumpFixture()
	m := newSessionManager(store, &tm.fakeTmux)
	m.Tmux = tm
	live, gone, err := m.Candidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gotLive := []string{}
	for _, c := range live {
		gotLive = append(gotLive, c.Workspace.Name+"/"+c.Session.ID)
	}
	want := []string{"feature-x/s_1", "feature-x/s_2", "payment-fix/s_3"}
	if len(gotLive) != len(want) {
		t.Fatalf("live = %v, want %v", gotLive, want)
	}
	for i := range want {
		if gotLive[i] != want[i] {
			t.Fatalf("live = %v, want %v", gotLive, want)
		}
	}
	if len(gone) != 2 || gone[0].ID != "s_4" || gone[1].ID != "s_5" {
		t.Fatalf("gone = %+v, want s_4 (missing window) and s_5 (tag mismatch)", gone)
	}
}

func TestResolve(t *testing.T) {
	store, tm := jumpFixture()
	m := newSessionManager(store, &tm.fakeTmux)
	m.Tmux = tm
	live, gone, _ := m.Candidates(context.Background())

	if c, err := Resolve(live, gone, "s_3"); err != nil || c.Session.ID != "s_3" {
		t.Fatalf("exact id: %v %v", c.Session.ID, err)
	}
	if c, err := Resolve(live, gone, "SECURITY"); err != nil || c.Session.ID != "s_2" {
		t.Fatalf("task substring: %v %v", c.Session.ID, err)
	}
	if _, err := Resolve(live, gone, "s_"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("prefix matching all: want ErrAmbiguous, got %v", err)
	}
	if _, err := Resolve(live, gone, "nothing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("want ErrSessionNotFound, got %v", err)
	}
	if _, err := Resolve(live, gone, "  "); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("empty: %v", err)
	}
	// Gone sessions are not jumpable, but the error must say why.
	if _, err := Resolve(live, gone, "s_4"); !errors.Is(err, ErrWindowGone) || !strings.Contains(err.Error(), "dmux resume s_4") {
		t.Fatalf("gone session must yield ErrWindowGone with resume hint: %v", err)
	}
	if _, err := Resolve(live, gone, "gone window"); !errors.Is(err, ErrWindowGone) {
		t.Fatalf("gone session by task: %v", err)
	}
}

func TestJump_InsideSwitches_OutsideAttaches(t *testing.T) {
	store, tm := jumpFixture()
	m := newSessionManager(store, &tm.fakeTmux)
	m.Tmux = tm
	live, gone, _ := m.Candidates(context.Background())
	c, _ := Resolve(live, gone, "s_3")

	tm.inside = true
	if err := m.Jump(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(tm.switched) != 1 || tm.switched[0].WindowID != "@3" || tm.switched[0].SessionID != "$2" {
		t.Fatalf("switched = %+v", tm.switched)
	}

	tm.inside = false
	if err := m.Jump(context.Background(), c); err == nil || err.Error() != "fake attach" {
		t.Fatalf("outside tmux must attach: %v", err)
	}
}

func TestJump_RefusesUnvalidatedCandidate(t *testing.T) {
	store, tm := jumpFixture()
	m := newSessionManager(store, &tm.fakeTmux)
	m.Tmux = tm
	tm.inside = true
	bad := JumpCandidate{Session: Session{ID: "s_1", Tmux: TmuxTarget{WindowID: "@1"}}, Window: TmuxWindow{WindowID: "@1", DmuxSession: "s_other"}}
	if err := m.Jump(context.Background(), bad); !errors.Is(err, ErrWindowGone) {
		t.Fatalf("want ErrWindowGone, got %v", err)
	}
	if len(tm.switched) != 0 {
		t.Fatal("must never switch to a window with a mismatched tag")
	}
}
