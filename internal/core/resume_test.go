package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func resumeFixture(t *testing.T) (*SessionManager, *fakeStore, *listingTmux, *mutableEvents, *fakeProc) {
	t.Helper()
	store := &fakeStore{state: State{
		Workspaces: []Workspace{{ID: "ws_1", Name: "feature-x", Root: "/r/feature-x", Status: WorkspaceReady}},
		Sessions: []Session{
			{ID: "s_1", WorkspaceID: "ws_1", Task: "auth", Root: "/r/feature-x/s_1", Status: WorkspaceReady, DevinSessionID: "olive-turkey", Tmux: TmuxTarget{WindowID: "@1"}, CreatedAt: t0},
			{ID: "s_2", WorkspaceID: "ws_1", Task: "no devin id", Root: "/r/feature-x/s_2", Status: WorkspaceReady, Tmux: TmuxTarget{WindowID: "@2"}, CreatedAt: t0},
		},
	}}
	tm := &listingTmux{}
	tm.nextWindow = 10 // real tmux never reuses a live window id; keep fixtures distinct
	evs := &mutableEvents{byID: map[string][]SessionEvent{
		"s_1": {ev(0, SessionStarted, nil), ev(1, SessionEnded, nil), ev(2, ProcessExited, map[string]any{"exitCode": 0})},
	}}
	proc := &fakeProc{alive: map[int]bool{}}
	m := newSessionManager(store, &tm.fakeTmux)
	m.Tmux, m.Events, m.Proc = tm, evs, proc
	return m, store, tm, evs, proc
}

func TestResume_ExitedSession_NoWindow(t *testing.T) {
	m, store, tm, _, _ := resumeFixture(t)
	res, err := m.Resume(context.Background(), ResumeInput{Query: "s_1", Prompt: "continue please"})
	if err != nil {
		t.Fatal(err)
	}
	if res.DevinSessionID != "olive-turkey" || res.OldWindow != "" {
		t.Fatalf("res: %+v", res)
	}
	o := tm.spawned[0]
	want := "/usr/local/bin/dmux run --session s_1 --dir /r/feature-x/s_1 -- devin -r olive-turkey -- continue please"
	if strings.Join(o.Argv, " ") != want {
		t.Fatalf("argv:\n got %q\nwant %q", strings.Join(o.Argv, " "), want)
	}
	if o.Cwd != "/r/feature-x/s_1" || o.SessionID != "s_1" || o.Env["DMUX_SESSION_ID"] != "s_1" || o.SessionName != "dmux-feature-x" {
		t.Fatalf("spawn opts: %+v", o)
	}
	// Record points at the new window.
	if got := store.state.Session("s_1").Tmux.WindowID; got != "@1" && got == "" {
		t.Fatalf("tmux target not updated: %+v", store.state.Session("s_1").Tmux)
	}
	if store.state.Session("s_1").Tmux != res.Session.Tmux {
		t.Fatal("persisted target must match result")
	}
}

func TestResume_ClosesHeldPane(t *testing.T) {
	m, _, tm, _, proc := resumeFixture(t)
	// Wrapper alive, holding the pane after Devin exited.
	tm.windows = []TmuxWindow{{WindowID: "@1", DmuxSession: "s_1", PanePID: 77}}
	proc.alive[77] = true
	res, err := m.Resume(context.Background(), ResumeInput{Query: "s_1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.OldWindow != "@1" || len(tm.killed) != 1 || tm.killed[0] != "@1" {
		t.Fatalf("held pane should be closed: %+v killed=%v", res, tm.killed)
	}
}

func TestResume_RefusesRunningSession(t *testing.T) {
	m, _, tm, evs, proc := resumeFixture(t)
	evs.byID["s_1"] = []SessionEvent{ev(0, SessionStarted, nil), ev(1, TurnCompleted, nil)} // idle, alive
	tm.windows = []TmuxWindow{{WindowID: "@1", DmuxSession: "s_1", PanePID: 77}}
	proc.alive[77] = true
	_, err := m.Resume(context.Background(), ResumeInput{Query: "s_1"})
	if !errors.Is(err, ErrSessionRunning) || !strings.Contains(err.Error(), "dmux jump s_1") {
		t.Fatalf("want ErrSessionRunning with jump hint, got %v", err)
	}
	if len(tm.spawned) != 0 {
		t.Fatal("must not spawn")
	}
}

func TestResume_NoDevinID_NeverFallsBackToContinue(t *testing.T) {
	m, _, tm, _, _ := resumeFixture(t)
	_, err := m.Resume(context.Background(), ResumeInput{Query: "s_2"})
	if !errors.Is(err, ErrNoDevinSession) || !strings.Contains(err.Error(), "dmux init") {
		t.Fatalf("want ErrNoDevinSession with init hint, got %v", err)
	}
	if len(tm.spawned) != 0 {
		t.Fatal("must not spawn anything (and certainly not `devin -c`)")
	}
}

func TestResume_DevinIDFromEventLogFallback(t *testing.T) {
	m, store, tm, evs, _ := resumeFixture(t)
	evs.byID["s_2"] = []SessionEvent{
		{Type: SessionStarted, SessionID: "s_2", DevinSessionID: "glaze-toque", Timestamp: t0},
		{Type: ProcessExited, SessionID: "s_2", Timestamp: t0.Add(1), Data: map[string]any{"exitCode": 0}},
	}
	res, err := m.Resume(context.Background(), ResumeInput{Query: "s_2"})
	if err != nil || res.DevinSessionID != "glaze-toque" {
		t.Fatalf("%v %+v", err, res)
	}
	if !strings.Contains(strings.Join(tm.spawned[0].Argv, " "), "-r glaze-toque") {
		t.Fatalf("argv: %v", tm.spawned[0].Argv)
	}
	if store.state.Session("s_2").DevinSessionID != "glaze-toque" {
		t.Fatal("recovered id should be persisted to the record")
	}
}

func TestResume_Preconditions(t *testing.T) {
	m, store, tm, _, _ := resumeFixture(t)
	if _, err := m.Resume(context.Background(), ResumeInput{Query: "nope"}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("%v", err)
	}
	store.state.Workspaces[0].Status = WorkspaceFailed
	if _, err := m.Resume(context.Background(), ResumeInput{Query: "s_1"}); !errors.Is(err, ErrWorkspaceNotReady) {
		t.Fatalf("%v", err)
	}
	store.state.Workspaces[0].Status = WorkspaceReady
	tm.spawnErr = errors.New("tmux broke")
	if _, err := m.Resume(context.Background(), ResumeInput{Query: "s_1"}); err == nil || !strings.Contains(err.Error(), "tmux broke") {
		t.Fatalf("%v", err)
	}
	if store.state.Session("s_1").Tmux.WindowID != "@1" {
		t.Fatal("failed spawn must not alter the record")
	}
}

func TestResume_PersistFailureKillsNewWindow(t *testing.T) {
	m, store, tm, _, _ := resumeFixture(t)
	store.failNext = errors.New("disk full")
	_, err := m.Resume(context.Background(), ResumeInput{Query: "s_1"})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("%v", err)
	}
	if len(tm.killed) != 1 {
		t.Fatalf("new window must be killed when the record cannot be saved: %v", tm.killed)
	}
}
