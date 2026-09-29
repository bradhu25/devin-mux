package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeProc models a process group: Terminate makes it exit after a delay
// (or never, if stubborn), Kill always ends it.
type fakeProc struct {
	mu          sync.Mutex
	alive       map[int]bool
	stubborn    bool // ignore SIGTERM
	terminated  []int
	killed      []int
	onExit      func(pid int) // e.g. append process_exited to the fake log
	descendants []ProcInfo
}

func (p *fakeProc) Descendants(int) ([]ProcInfo, error) { return p.descendants, nil }

func (p *fakeProc) Terminate(pid int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.terminated = append(p.terminated, pid)
	if !p.stubborn {
		p.alive[pid] = false
		if p.onExit != nil {
			p.onExit(pid)
		}
	}
	return nil
}

func (p *fakeProc) Kill(pid int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.killed = append(p.killed, pid)
	p.alive[pid] = false
	return nil
}

func (p *fakeProc) Alive(pid int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.alive[pid]
}

type mutableEvents struct {
	mu   sync.Mutex
	byID map[string][]SessionEvent
}

func (m *mutableEvents) Read(id string) ([]SessionEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]SessionEvent(nil), m.byID[id]...), nil
}

func (m *mutableEvents) add(id string, ev SessionEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[id] = append(m.byID[id], ev)
}

func killFixture(t *testing.T, stubborn bool) (*SessionManager, *fakeProc, *listingTmux, *mutableEvents) {
	t.Helper()
	store := &fakeStore{state: State{
		Workspaces: []Workspace{{ID: "ws_1", Name: "feature-x", Status: WorkspaceReady}},
		Sessions: []Session{
			{ID: "s_1", WorkspaceID: "ws_1", Task: "auth work", Tmux: TmuxTarget{WindowID: "@1"}, CreatedAt: t0},
			{ID: "s_2", WorkspaceID: "ws_1", Task: "gone already", Tmux: TmuxTarget{WindowID: "@9"}, CreatedAt: t0},
		},
	}}
	tm := &listingTmux{windows: []TmuxWindow{{WindowID: "@1", DmuxSession: "s_1", PanePID: 4242}}}
	evs := &mutableEvents{byID: map[string][]SessionEvent{"s_1": {ev(0, SessionStarted, nil)}}}
	proc := &fakeProc{alive: map[int]bool{4242: true}, stubborn: stubborn}
	proc.onExit = func(int) {
		evs.add("s_1", ev(5, ProcessExited, map[string]any{"exitCode": -1, "signal": "terminated"}))
	}
	m := newSessionManager(store, &tm.fakeTmux)
	m.Tmux, m.Proc, m.Events = tm, proc, evs
	return m, proc, tm, evs
}

func TestKill_GracefulTerminate(t *testing.T) {
	m, proc, tm, _ := killFixture(t, false)
	res, err := m.Kill(context.Background(), "s_1", KillOptions{Grace: time.Second, Poll: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if res.Method != "terminated" || !res.WindowGone || len(proc.terminated) != 1 || len(proc.killed) != 0 {
		t.Fatalf("res=%+v proc=%+v", res, proc)
	}
	if len(tm.killed) != 1 || tm.killed[0] != "@1" {
		t.Fatalf("window not removed: %v", tm.killed)
	}
	// Record is kept.
	st, _ := m.Store.Read()
	if st.Session("s_1") == nil {
		t.Fatal("kill must never delete the session record")
	}
}

func TestKill_EscalatesToSIGKILL(t *testing.T) {
	m, proc, _, _ := killFixture(t, true)
	start := time.Now()
	res, err := m.Kill(context.Background(), "s_1", KillOptions{Grace: 200 * time.Millisecond, Poll: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if res.Method != "killed" || len(proc.killed) != 1 || !res.WindowGone {
		t.Fatalf("res=%+v proc=%+v", res, proc)
	}
	if el := time.Since(start); el < 200*time.Millisecond || el > 3*time.Second {
		t.Fatalf("grace not honored: %v", el)
	}
}

func TestKill_AlreadyExited_NoWindow(t *testing.T) {
	m, proc, tm, _ := killFixture(t, false)
	res, err := m.Kill(context.Background(), "s_2", KillOptions{})
	if err != nil || res.Method != "already-exited" || res.WindowGone {
		t.Fatalf("%v %+v", err, res)
	}
	if len(proc.terminated)+len(proc.killed)+len(tm.killed) != 0 {
		t.Fatal("nothing should be signalled or removed")
	}
}

func TestKill_DeadPaneJustRemovesWindow(t *testing.T) {
	m, proc, tm, _ := killFixture(t, false)
	tm.windows[0].PaneDead = true
	res, err := m.Kill(context.Background(), "s_1", KillOptions{})
	if err != nil || res.Method != "already-exited" || !res.WindowGone {
		t.Fatalf("%v %+v", err, res)
	}
	if len(proc.terminated) != 0 || len(tm.killed) != 1 {
		t.Fatalf("dead pane: should only remove window: %+v %v", proc, tm.killed)
	}
}

// Devin exited on its own; the wrapper is alive holding the pane. Kill must
// not signal anything, just close the window.
func TestKill_HeldPaneAfterExit(t *testing.T) {
	m, proc, tm, evs := killFixture(t, false)
	evs.add("s_1", ev(3, ProcessExited, map[string]any{"exitCode": 0}))
	res, err := m.Kill(context.Background(), "s_1", KillOptions{})
	if err != nil || res.Method != "already-exited" || !res.WindowGone {
		t.Fatalf("%v %+v", err, res)
	}
	if len(proc.terminated) != 0 || len(tm.killed) != 1 {
		t.Fatalf("held pane: only the window should be removed: %+v %v", proc, tm.killed)
	}
}

func TestKillAll(t *testing.T) {
	store := &fakeStore{state: State{
		Workspaces: []Workspace{{ID: "ws_1", Name: "a", Status: WorkspaceReady}, {ID: "ws_2", Name: "b", Status: WorkspaceReady}},
		Sessions: []Session{
			{ID: "s_a1", WorkspaceID: "ws_1", Tmux: TmuxTarget{WindowID: "@1"}},
			{ID: "s_a2", WorkspaceID: "ws_1", Tmux: TmuxTarget{WindowID: "@2"}},
			{ID: "s_b1", WorkspaceID: "ws_2", Tmux: TmuxTarget{WindowID: "@3"}},
			{ID: "s_gone", WorkspaceID: "ws_2", Tmux: TmuxTarget{WindowID: "@9"}}, // no window: skipped
		},
	}}
	tm := &listingTmux{windows: []TmuxWindow{
		{WindowID: "@1", DmuxSession: "s_a1", PanePID: 11}, {WindowID: "@2", DmuxSession: "s_a2", PanePID: 12}, {WindowID: "@3", DmuxSession: "s_b1", PanePID: 13},
	}}
	proc := &fakeProc{alive: map[int]bool{11: true, 12: true, 13: true}}
	m := newSessionManager(store, &tm.fakeTmux)
	m.Tmux, m.Proc, m.Events = tm, proc, &mutableEvents{byID: map[string][]SessionEvent{}}
	opts := KillOptions{Grace: time.Second, Poll: 10 * time.Millisecond}

	// One workspace only.
	res, err := m.KillAll(context.Background(), "a", opts)
	if err != nil || len(res) != 2 {
		t.Fatalf("%v %+v", err, res)
	}
	for _, r := range res {
		if r.Err != nil || r.Result.Method != "terminated" || r.Session.WorkspaceID != "ws_1" {
			t.Fatalf("%+v", r)
		}
	}
	if proc.Alive(13) != true {
		t.Fatal("workspace b must be untouched")
	}
	// Everything remaining, concurrently (stubborn: each needs the full grace).
	proc.stubborn = true
	start := time.Now()
	res, err = m.KillAll(context.Background(), "", opts)
	if err != nil || len(res) != 1 || res[0].Session.ID != "s_b1" || res[0].Result.Method != "killed" {
		t.Fatalf("%v %+v", err, res)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("kills must run concurrently")
	}
	if len(store.state.Sessions) != 4 {
		t.Fatal("records must be kept")
	}
	if _, err := m.KillAll(context.Background(), "nope", opts); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatal(err)
	}
}

func TestKill_QueryResolution(t *testing.T) {
	m, _, _, _ := killFixture(t, false)
	if _, err := m.Kill(context.Background(), "auth", KillOptions{Grace: time.Second, Poll: 10 * time.Millisecond}); err != nil {
		t.Fatalf("task substring: %v", err)
	}
	m2, _, _, _ := killFixture(t, false)
	if _, err := m2.Kill(context.Background(), "s_", KillOptions{}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("want ErrAmbiguous, got %v", err)
	}
	if _, err := m2.Kill(context.Background(), "nope", KillOptions{}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("want ErrSessionNotFound, got %v", err)
	}
}

func TestKill_TagMismatchTreatedAsGone(t *testing.T) {
	m, proc, tm, _ := killFixture(t, false)
	tm.windows[0].DmuxSession = "s_other" // window id reused by someone else
	res, err := m.Kill(context.Background(), "s_1", KillOptions{})
	if err != nil || res.Method != "already-exited" {
		t.Fatalf("%v %+v", err, res)
	}
	if len(proc.terminated) != 0 || len(tm.killed) != 0 {
		t.Fatal("must never signal or kill a window with a foreign tag")
	}
}
