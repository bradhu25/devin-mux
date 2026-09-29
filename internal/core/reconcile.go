package core

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Liveness is what tmux says about a session's window.
type Liveness string

const (
	LivenessAlive   Liveness = "alive"   // tagged window exists, pane process running
	LivenessDead    Liveness = "dead"    // tagged window exists, pane process exited (remain-on-exit)
	LivenessMissing Liveness = "missing" // no window carries this session's tag
)

// SessionView is the reconciled, display-ready state of one session.
type SessionView struct {
	Session   Session
	Workspace Workspace
	Status    Status   // reducer output, after approval resolution
	Liveness  Liveness // tmux evidence
	// Evidence notes what the reconciler did, for `ls --verbose`/debugging.
	Evidence []string
}

// WorkspaceView groups sessions under their workspace.
type WorkspaceView struct {
	Workspace Workspace
	Sessions  []SessionView
}

// Snapshot is the whole board at one instant.
type Snapshot struct {
	TakenAt    time.Time
	Workspaces []WorkspaceView
}

// Reconciler derives SessionViews from all evidence sources.
type Reconciler struct {
	Store  Store
	Events EventLog
	Tmux   Tmux
	Devin  Devin // may be nil: approval resolution via Devin's store is skipped
	Proc   Proc  // may be nil: approval inference from the process tree is skipped
	Now    func() time.Time
	// StaleAfter bounds how long a "working" session with no tool in flight
	// may stay silent before it is reported as probably idle. Ctrl-C
	// mid-turn fires no hook and leaves no store marker, so silence while
	// "thinking" is ambiguous; a running tool is not. Default 90s.
	StaleAfter time.Duration
}

// DefaultStaleAfter is the default for Reconciler.StaleAfter. Measured over
// 38 real thinking gaps (tool_completed/prompt_submitted -> next agent event)
// in dogfooding sessions: median 6.6s, p95 34s, max 59s. 90s covers the
// observed maximum with 50% headroom.
const DefaultStaleAfter = 90 * time.Second

// Snapshot reconciles every recorded session. It never writes state.
func (r *Reconciler) Snapshot(ctx context.Context) (*Snapshot, error) {
	st, err := r.Store.Read()
	if err != nil {
		return nil, err
	}
	byTag := r.windowsByTag(ctx)
	snap := &Snapshot{TakenAt: r.now()}
	views := make(map[string][]SessionView)
	for _, s := range st.Sessions {
		ws := st.Workspace(s.WorkspaceID)
		if ws == nil {
			continue
		}
		v := r.reconcileSession(ctx, s, *ws, byTag)
		views[ws.ID] = append(views[ws.ID], v)
	}
	for _, ws := range st.Workspaces {
		sv := views[ws.ID]
		sort.SliceStable(sv, func(i, j int) bool { return sv[i].Session.CreatedAt.Before(sv[j].Session.CreatedAt) })
		snap.Workspaces = append(snap.Workspaces, WorkspaceView{Workspace: ws, Sessions: sv})
	}
	sort.SliceStable(snap.Workspaces, func(i, j int) bool { return snap.Workspaces[i].Workspace.Name < snap.Workspaces[j].Workspace.Name })
	return snap, nil
}

// Reconcile derives the view for one session.
func (r *Reconciler) Reconcile(ctx context.Context, s Session, ws Workspace) (SessionView, error) {
	return r.reconcileSession(ctx, s, ws, r.windowsByTag(ctx)), nil
}

// windowsByTag indexes live tmux windows by their @dmux_session tag. A tmux
// failure is evidence, not an error: every session then reads as missing.
func (r *Reconciler) windowsByTag(ctx context.Context) map[string]TmuxWindow {
	wins, err := r.Tmux.ListWindows(ctx)
	if err != nil {
		return nil
	}
	byTag := make(map[string]TmuxWindow, len(wins))
	for _, w := range wins {
		if w.DmuxSession != "" {
			byTag[w.DmuxSession] = w
		}
	}
	return byTag
}

func (r *Reconciler) reconcileSession(ctx context.Context, s Session, ws Workspace, byTag map[string]TmuxWindow) SessionView {
	v := SessionView{Session: s, Workspace: ws}

	// 1. Events -> reducer.
	events, err := r.Events.Read(s.ID)
	if err != nil {
		v.Evidence = append(v.Evidence, "event log unreadable: "+err.Error())
	}
	v.Status = Reduce(events)

	// 2. Liveness from tmux (tag + id must both match; PLAN.md Navigation).
	w, ok := byTag[s.ID]
	switch {
	case !ok || w.WindowID != s.Tmux.WindowID:
		v.Liveness = LivenessMissing
	case w.PaneDead:
		v.Liveness = LivenessDead
	default:
		v.Liveness = LivenessAlive
	}

	// 3. Resolve pending approvals via Devin's store (Spike 3: deny/cancel
	//    fire no hook). Only meaningful while the process is alive.
	if v.Liveness == LivenessAlive && len(v.Status.Pending) > 0 && r.Devin != nil && v.Status.DevinSessionID != "" {
		ids := make([]string, 0, len(v.Status.Pending))
		for _, p := range v.Status.Pending {
			if p.ToolUseID != "" {
				ids = append(ids, p.ToolUseID)
			}
		}
		outcomes, err := r.Devin.ToolCallOutcomes(ctx, v.Status.DevinSessionID, ids)
		switch {
		case err != nil:
			v.Evidence = append(v.Evidence, "devin store unavailable; approval state unverified")
		case len(outcomes) > 0:
			now := r.now()
			for _, o := range outcomes {
				events = append(events, SessionEvent{
					EventID: NewEventID(), SessionID: s.ID, Type: ApprovalResolved, Timestamp: now,
					Data: map[string]any{"toolUseId": o.ToolUseID, "outcome": string(o.Outcome), "source": "devin-store"},
				})
				v.Evidence = append(v.Evidence, "approval "+o.ToolUseID+" "+string(o.Outcome)+" (devin store)")
			}
			v.Status = Reduce(events)
		}
	}

	// 3b. Process tree. Approving an exec prompt fires no hook and writes
	//     no store row until the command ends, so a long command would read
	//     as awaiting-approval for its whole run. A command started under
	//     the pane's Devin after the request is proof of approval.
	if v.Liveness == LivenessAlive && len(v.Status.Pending) > 0 && r.Proc != nil {
		if resolved := r.approvalsSeenInProcessTree(w.PanePID, v.Status.Pending); len(resolved) > 0 {
			now := r.now()
			for _, id := range resolved {
				events = append(events, SessionEvent{
					EventID: NewEventID(), SessionID: s.ID, Type: ApprovalResolved, Timestamp: now,
					Data: map[string]any{"toolUseId": id, "outcome": string(OutcomeApproved), "source": "process-tree"},
				})
				v.Evidence = append(v.Evidence, "approval "+id+" approved (process tree: command running)")
			}
			v.Status = Reduce(events)
		}
	}

	// 4. Precedence: liveness overrides stale event-derived state.
	switch v.Liveness {
	case LivenessMissing, LivenessDead:
		if v.Status.Lifecycle == LifecycleRunning || v.Status.Lifecycle == LifecycleStarting {
			v.Evidence = append(v.Evidence, "process gone without exit event")
			v.Status.Lifecycle = LifecycleExited
		}
		v.Status.Activity = ActivityUnknown
		v.Status.Pending = nil
	case LivenessAlive:
		if v.Status.Lifecycle == LifecycleExited || v.Status.Lifecycle == LifecycleFailed {
			// Window alive but events say exited: wrapper is holding the
			// pane after Devin exited. Trust the events; the pane is a shell.
			v.Evidence = append(v.Evidence, "pane held open after exit")
		}
		if v.Status.Lifecycle == LifecycleStarting {
			v.Status.Activity = ActivityUnknown // no events yet; never guess
		}
		// Silence while thinking is ambiguous (Ctrl-C leaves no trace);
		// silence while a tool runs is not.
		if v.Status.Activity == ActivityWorking && len(v.Status.InFlight) == 0 && !v.Status.LastEventAt.IsZero() {
			if silent := r.now().Sub(v.Status.LastEventAt); silent > r.staleAfter() {
				v.Status.Activity = ActivityProbablyIdle
				v.Evidence = append(v.Evidence, fmt.Sprintf("no events for %s and no tool running: the turn was probably interrupted (Ctrl-C); Devin does not report that, so this is inferred", silent.Truncate(time.Second)))
			}
		}
	}
	return v
}

// Display collapses lifecycle x activity into one label for tables and
// badges. Storage never uses this.
func Display(st Status, _ Liveness) string {
	switch st.Lifecycle {
	case LifecycleFailed:
		return "failed"
	case LifecycleExited:
		return "exited"
	case LifecycleStarting:
		return "starting"
	}
	switch st.Activity {
	case ActivityWorking:
		return "working"
	case ActivityIdle:
		return "idle"
	case ActivityAwaitingApproval:
		return "awaiting-approval"
	case ActivityProbablyIdle:
		return "idle?"
	}
	return "unknown"
}

func (r *Reconciler) staleAfter() time.Duration {
	if r.StaleAfter > 0 {
		return r.StaleAfter
	}
	return DefaultStaleAfter
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UTC()
}

// approvalsSeenInProcessTree returns pending exec approvals for which a
// non-Devin process exists under panePID that started after the request.
// Processes that predate the request (a backgrounded server from an earlier
// command) prove nothing and are ignored.
func (r *Reconciler) approvalsSeenInProcessTree(panePID int, pending []PendingApproval) []string {
	procs, err := r.Proc.Descendants(panePID)
	if err != nil || len(procs) == 0 {
		return nil
	}
	var out []string
	for _, p := range pending {
		if p.ToolName != "exec" {
			continue
		}
		for _, pr := range procs {
			if pr.Comm == "dmux" || pr.Comm == "devin" {
				continue
			}
			// One second of slack: process start times are second-granular.
			if pr.Started.After(p.RequestedAt.Add(-time.Second)) {
				out = append(out, p.ToolUseID)
				break
			}
		}
	}
	return out
}
