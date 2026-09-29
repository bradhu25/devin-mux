package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeEvents struct {
	byID map[string][]SessionEvent
	err  error
}

func (f *fakeEvents) Read(id string) ([]SessionEvent, error) { return f.byID[id], f.err }

type outcomeDevin struct {
	fakeDevin
	outcomes map[string]ApprovalOutcome
	err      error
	calls    int
}

func (d *outcomeDevin) ToolCallOutcomes(_ context.Context, _ string, ids []string) ([]ToolCallOutcome, error) {
	d.calls++
	if d.err != nil {
		return nil, d.err
	}
	var out []ToolCallOutcome
	for _, id := range ids {
		if o, ok := d.outcomes[id]; ok {
			out = append(out, ToolCallOutcome{ToolUseID: id, Outcome: o})
		}
	}
	return out, nil
}

func pendingEvents() []SessionEvent {
	return []SessionEvent{
		ev(0, SessionStarted, map[string]any{"source": "startup"}),
		ev(1, PromptSubmitted, map[string]any{"promptId": "p1"}),
		ev(2, ToolStarted, tool("t1")),
		ev(3, ApprovalRequested, tool("t1")),
	}
}

func newReconciler(events []SessionEvent, win *TmuxWindow, dev *outcomeDevin) (*Reconciler, Session, Workspace) {
	ws := Workspace{ID: "ws_1", Name: "feature-x", Status: WorkspaceReady}
	s := Session{ID: "s_1", WorkspaceID: "ws_1", Task: "auth", Tmux: TmuxTarget{WindowID: "@1"}, CreatedAt: t0}
	tm := &listingTmux{}
	if win != nil {
		tm.windows = []TmuxWindow{*win}
	}
	store := &fakeStore{state: State{Workspaces: []Workspace{ws}, Sessions: []Session{s}}}
	r := &Reconciler{Store: store, Events: &fakeEvents{byID: map[string][]SessionEvent{"s_1": events}}, Tmux: tm, Now: func() time.Time { return t0.Add(time.Hour) }}
	if dev != nil {
		r.Devin = dev
	}
	return r, s, ws
}

func aliveWin() *TmuxWindow { return &TmuxWindow{WindowID: "@1", DmuxSession: "s_1", PanePID: 42} }

func TestReconcile_AliveAwaitingApproval_NoStoreResolution(t *testing.T) {
	r, s, ws := newReconciler(pendingEvents(), aliveWin(), &outcomeDevin{})
	v, _ := r.Reconcile(context.Background(), s, ws)
	if v.Liveness != LivenessAlive || v.Status.Activity != ActivityAwaitingApproval || len(v.Status.Pending) != 1 {
		t.Fatalf("%+v", v.Status)
	}
	if Display(v.Status, v.Liveness) != "awaiting-approval" {
		t.Fatalf("display: %s", Display(v.Status, v.Liveness))
	}
}

// The Spike 3 case: user denied, no hook fired, Devin's store knows.
func TestReconcile_DeniedResolvedFromDevinStore(t *testing.T) {
	dev := &outcomeDevin{outcomes: map[string]ApprovalOutcome{"t1": OutcomeDenied}}
	r, s, ws := newReconciler(pendingEvents(), aliveWin(), dev)
	v, _ := r.Reconcile(context.Background(), s, ws)
	if v.Status.Activity != ActivityIdle || len(v.Status.Pending) != 0 {
		t.Fatalf("denied approval should yield idle: %+v", v.Status)
	}
	if dev.calls != 1 || len(v.Evidence) == 0 {
		t.Fatalf("store should be consulted once with evidence noted: calls=%d evidence=%v", dev.calls, v.Evidence)
	}
	if Display(v.Status, v.Liveness) != "idle" {
		t.Fatalf("display: %s", Display(v.Status, v.Liveness))
	}
}

func TestReconcile_ApprovedFromStoreBeforePostToolUseArrives(t *testing.T) {
	dev := &outcomeDevin{outcomes: map[string]ApprovalOutcome{"t1": OutcomeApproved}}
	r, s, ws := newReconciler(pendingEvents(), aliveWin(), dev)
	v, _ := r.Reconcile(context.Background(), s, ws)
	if v.Status.Activity != ActivityWorking {
		t.Fatalf("approved should yield working: %+v", v.Status)
	}
}

// Store failure is evidence, not an error: stay awaiting-approval honestly.
func TestReconcile_StoreUnavailableKeepsPending(t *testing.T) {
	dev := &outcomeDevin{err: errors.New("db locked")}
	r, s, ws := newReconciler(pendingEvents(), aliveWin(), dev)
	v, err := r.Reconcile(context.Background(), s, ws)
	if err != nil || v.Status.Activity != ActivityAwaitingApproval {
		t.Fatalf("%v %+v", err, v.Status)
	}
	found := false
	for _, e := range v.Evidence {
		if e == "devin store unavailable; approval state unverified" {
			found = true
		}
	}
	if !found {
		t.Fatalf("evidence should note the store failure: %v", v.Evidence)
	}
}

// Store is not consulted when nothing is pending or the process is gone.
func TestReconcile_StoreNotConsultedUnnecessarily(t *testing.T) {
	dev := &outcomeDevin{outcomes: map[string]ApprovalOutcome{"t1": OutcomeDenied}}
	idle := []SessionEvent{ev(0, SessionStarted, nil), ev(1, TurnCompleted, nil)}
	r, s, ws := newReconciler(idle, aliveWin(), dev)
	_, _ = r.Reconcile(context.Background(), s, ws)
	r2, s2, ws2 := newReconciler(pendingEvents(), nil, dev) // window missing
	_, _ = r2.Reconcile(context.Background(), s2, ws2)
	if dev.calls != 0 {
		t.Fatalf("store consulted %d times, want 0", dev.calls)
	}
}

// Liveness beats stale events: window gone with no exit event => exited/unknown.
func TestReconcile_MissingWindowOverridesRunningEvents(t *testing.T) {
	r, s, ws := newReconciler(pendingEvents(), nil, nil)
	v, _ := r.Reconcile(context.Background(), s, ws)
	if v.Liveness != LivenessMissing || v.Status.Lifecycle != LifecycleExited || v.Status.Activity != ActivityUnknown || len(v.Status.Pending) != 0 {
		t.Fatalf("%+v liveness=%s", v.Status, v.Liveness)
	}
	if Display(v.Status, v.Liveness) != "exited" {
		t.Fatalf("display: %s", Display(v.Status, v.Liveness))
	}
}

// A window whose tag is a different session (id reused after tmux restart)
// counts as missing, never as alive.
func TestReconcile_TagMismatchIsMissing(t *testing.T) {
	win := &TmuxWindow{WindowID: "@1", DmuxSession: "s_other"}
	r, s, ws := newReconciler(pendingEvents(), win, nil)
	v, _ := r.Reconcile(context.Background(), s, ws)
	if v.Liveness != LivenessMissing {
		t.Fatalf("liveness = %s", v.Liveness)
	}
}

// Dead pane (remain-on-exit) after the wrapper recorded process_exited.
func TestReconcile_DeadPaneWithExitEvent(t *testing.T) {
	evs := append(pendingEvents(), ev(9, ProcessExited, map[string]any{"exitCode": float64(2)}))
	win := aliveWin()
	win.PaneDead = true
	r, s, ws := newReconciler(evs, win, nil)
	v, _ := r.Reconcile(context.Background(), s, ws)
	if v.Liveness != LivenessDead || v.Status.Lifecycle != LifecycleFailed || Display(v.Status, v.Liveness) != "failed" {
		t.Fatalf("%+v liveness=%s display=%s", v.Status, v.Liveness, Display(v.Status, v.Liveness))
	}
}

// Wrapper holding the pane: window alive, events say exited => exited.
func TestReconcile_HeldPaneAfterExit(t *testing.T) {
	evs := []SessionEvent{ev(0, SessionStarted, nil), ev(1, SessionEnded, nil), ev(2, ProcessExited, map[string]any{"exitCode": 0})}
	r, s, ws := newReconciler(evs, aliveWin(), nil)
	v, _ := r.Reconcile(context.Background(), s, ws)
	if v.Status.Lifecycle != LifecycleExited || Display(v.Status, v.Liveness) != "exited" {
		t.Fatalf("%+v", v.Status)
	}
}

// Alive but no events yet: starting/unknown, never a guessed idle.
func TestReconcile_AliveNoEvents(t *testing.T) {
	r, s, ws := newReconciler(nil, aliveWin(), nil)
	v, _ := r.Reconcile(context.Background(), s, ws)
	if v.Status.Lifecycle != LifecycleStarting || v.Status.Activity != ActivityUnknown || Display(v.Status, v.Liveness) != "starting" {
		t.Fatalf("%+v", v.Status)
	}
}

// Ctrl-C mid-turn fires no hook and leaves no store marker. When the agent
// is "thinking" (no tool in flight) and nothing has happened for StaleAfter,
// the honest status is unknown, never working. A tool still running keeps
// working no matter how long it takes.
func TestReconcile_StaleThinkingBecomesUnknown(t *testing.T) {
	thinking := []SessionEvent{
		ev(0, SessionStarted, nil),
		ev(1, PromptSubmitted, map[string]any{"promptId": "p1"}),
		ev(2, ToolStarted, tool("t1")),
		ev(3, ToolCompleted, tool("t1")), // then the user hit Ctrl-C while the model was thinking
	}
	r, s, ws := newReconciler(thinking, aliveWin(), nil)
	r.StaleAfter = 3 * time.Minute

	r.Now = func() time.Time { return t0.Add(3*time.Second + time.Minute) } // 1 min: still plausibly thinking
	v, _ := r.Reconcile(context.Background(), s, ws)
	if v.Status.Activity != ActivityWorking {
		t.Fatalf("recent thinking should be working: %+v", v.Status)
	}

	r.Now = func() time.Time { return t0.Add(3*time.Second + 10*time.Minute) }
	v, _ = r.Reconcile(context.Background(), s, ws)
	if v.Status.Activity != ActivityUnknown || Display(v.Status, v.Liveness) != "unknown" {
		t.Fatalf("stale thinking should be unknown: %+v", v.Status)
	}
	if len(v.Evidence) == 0 || !strings.Contains(v.Evidence[0], "interrupted") {
		t.Fatalf("evidence should explain: %v", v.Evidence)
	}

	// A long-running tool is real work: stays working.
	running := append(append([]SessionEvent(nil), thinking...), ev(4, ToolStarted, tool("t2")))
	r2, s2, ws2 := newReconciler(running, aliveWin(), nil)
	r2.StaleAfter, r2.Now = 3*time.Minute, func() time.Time { return t0.Add(30 * time.Minute) }
	v, _ = r2.Reconcile(context.Background(), s2, ws2)
	if v.Status.Activity != ActivityWorking {
		t.Fatalf("in-flight tool must stay working: %+v", v.Status)
	}

	// Idle and awaiting-approval are unaffected by staleness.
	idle := []SessionEvent{ev(0, SessionStarted, nil), ev(1, TurnCompleted, nil)}
	r3, s3, ws3 := newReconciler(idle, aliveWin(), nil)
	r3.StaleAfter, r3.Now = 3*time.Minute, func() time.Time { return t0.Add(30 * time.Minute) }
	if v, _ = r3.Reconcile(context.Background(), s3, ws3); v.Status.Activity != ActivityIdle {
		t.Fatalf("idle must not go stale: %+v", v.Status)
	}
}

func TestReduce_TracksInFlightTools(t *testing.T) {
	evs := []SessionEvent{ev(0, SessionStarted, nil), ev(1, ToolStarted, tool("t1")), ev(2, ToolStarted, tool("t2"))}
	if st := Reduce(evs); len(st.InFlight) != 2 {
		t.Fatalf("in flight: %v", st.InFlight)
	}
	evs = append(evs, ev(3, ToolCompleted, tool("t1")))
	if st := Reduce(evs); len(st.InFlight) != 1 || st.InFlight[0] != "t2" {
		t.Fatalf("in flight after completion: %v", st.InFlight)
	}
	evs = append(evs, ev(4, ApprovalResolved, map[string]any{"toolUseId": "t2", "outcome": "denied"}))
	if st := Reduce(evs); len(st.InFlight) != 0 {
		t.Fatalf("denied tool is not in flight: %v", st.InFlight)
	}
	evs = append(evs, ev(5, ToolStarted, tool("t3")), ev(6, TurnCompleted, nil))
	if st := Reduce(evs); len(st.InFlight) != 0 {
		t.Fatalf("turn end clears in flight: %v", st.InFlight)
	}
}

func TestReconcile_EventLogErrorIsEvidence(t *testing.T) {
	r, s, ws := newReconciler(nil, aliveWin(), nil)
	r.Events = &fakeEvents{err: errors.New("disk")}
	v, err := r.Reconcile(context.Background(), s, ws)
	if err != nil || len(v.Evidence) == 0 {
		t.Fatalf("log error must be evidence, not failure: %v %v", err, v.Evidence)
	}
}

func TestSnapshot_GroupsAndSorts(t *testing.T) {
	store := &fakeStore{state: State{
		Workspaces: []Workspace{{ID: "ws_b", Name: "payment-fix"}, {ID: "ws_a", Name: "feature-x"}, {ID: "ws_c", Name: "empty"}},
		Sessions: []Session{
			{ID: "s_2", WorkspaceID: "ws_a", CreatedAt: t0.Add(time.Minute), Tmux: TmuxTarget{WindowID: "@2"}},
			{ID: "s_1", WorkspaceID: "ws_a", CreatedAt: t0, Tmux: TmuxTarget{WindowID: "@1"}},
			{ID: "s_3", WorkspaceID: "ws_b", CreatedAt: t0, Tmux: TmuxTarget{WindowID: "@3"}},
			{ID: "s_orphan", WorkspaceID: "ws_gone"},
		},
	}}
	tm := &listingTmux{windows: []TmuxWindow{{WindowID: "@1", DmuxSession: "s_1"}, {WindowID: "@3", DmuxSession: "s_3", PaneDead: true}}}
	r := &Reconciler{Store: store, Events: &fakeEvents{byID: map[string][]SessionEvent{
		"s_1": {ev(0, SessionStarted, nil), ev(1, TurnCompleted, nil)},
	}}, Tmux: tm}
	snap, err := r.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Workspaces) != 3 || snap.Workspaces[0].Workspace.Name != "empty" || snap.Workspaces[1].Workspace.Name != "feature-x" {
		t.Fatalf("workspace order: %+v", snap.Workspaces)
	}
	fx := snap.Workspaces[1].Sessions
	if len(fx) != 2 || fx[0].Session.ID != "s_1" || fx[1].Session.ID != "s_2" {
		t.Fatalf("session order: %+v", fx)
	}
	if Display(fx[0].Status, fx[0].Liveness) != "idle" || fx[1].Liveness != LivenessMissing {
		t.Fatalf("s_1=%s s_2 liveness=%s", Display(fx[0].Status, fx[0].Liveness), fx[1].Liveness)
	}
	if snap.Workspaces[2].Sessions[0].Liveness != LivenessDead {
		t.Fatalf("s_3 should be dead pane: %s", snap.Workspaces[2].Sessions[0].Liveness)
	}
	if len(snap.Workspaces[0].Sessions) != 0 {
		t.Fatal("empty workspace should have no sessions")
	}
}

func TestSnapshot_TmuxUnavailableIsAllMissing(t *testing.T) {
	store := &fakeStore{state: State{Workspaces: []Workspace{{ID: "ws_a", Name: "a"}}, Sessions: []Session{{ID: "s_1", WorkspaceID: "ws_a", Tmux: TmuxTarget{WindowID: "@1"}}}}}
	r := &Reconciler{Store: store, Events: &fakeEvents{}, Tmux: &erroringTmux{}}
	snap, err := r.Snapshot(context.Background())
	if err != nil || snap.Workspaces[0].Sessions[0].Liveness != LivenessMissing {
		t.Fatalf("%v %+v", err, snap)
	}
}

type erroringTmux struct{ fakeTmux }

func (e *erroringTmux) ListWindows(context.Context) ([]TmuxWindow, error) {
	return nil, errors.New("no tmux")
}
