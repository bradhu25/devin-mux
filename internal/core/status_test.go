package core

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 28, 22, 0, 0, 0, time.UTC)

// ev builds an event n seconds after t0.
func ev(n int, typ EventType, data map[string]any) SessionEvent {
	return SessionEvent{EventID: "e", SessionID: "s", DevinSessionID: "olive-turkey", Type: typ, Timestamp: t0.Add(time.Duration(n) * time.Second), Data: data}
}

func tool(id string) map[string]any {
	return map[string]any{"toolUseId": id, "toolName": "exec", "promptId": "p1"}
}

func expect(t *testing.T, st Status, lc Lifecycle, act Activity, pending int) {
	t.Helper()
	if st.Lifecycle != lc || st.Activity != act || len(st.Pending) != pending {
		t.Fatalf("got %s/%s pending=%d, want %s/%s pending=%d", st.Lifecycle, st.Activity, len(st.Pending), lc, act, pending)
	}
}

func TestReduce_Empty(t *testing.T) {
	expect(t, Reduce(nil), LifecycleStarting, ActivityUnknown, 0)
}

// Spike 3 approve path: Pre -> Permission -> Post -> Stop.
func TestReduce_ApprovePath(t *testing.T) {
	evs := []SessionEvent{
		ev(0, SessionStarted, map[string]any{"source": "startup"}),
		ev(1, PromptSubmitted, map[string]any{"promptId": "p1", "prompt": "touch x"}),
		ev(2, ToolStarted, tool("t1")),
		ev(3, ApprovalRequested, tool("t1")),
	}
	st := Reduce(evs)
	expect(t, st, LifecycleRunning, ActivityAwaitingApproval, 1)
	if st.Pending[0].ToolUseID != "t1" || st.Pending[0].ToolName != "exec" || st.DevinSessionID != "olive-turkey" || st.LastPrompt != "touch x" {
		t.Fatalf("pending/meta: %+v", st)
	}

	evs = append(evs, ev(4, ToolCompleted, tool("t1")))
	expect(t, Reduce(evs), LifecycleRunning, ActivityWorking, 0)

	evs = append(evs, ev(5, TurnCompleted, map[string]any{"lastMessage": "Done"}))
	st = Reduce(evs)
	expect(t, st, LifecycleRunning, ActivityIdle, 0)
	if st.LastMessage != "Done" {
		t.Fatalf("lastMessage: %q", st.LastMessage)
	}
}

// Spike 3 deny path: nothing fires after PermissionRequest. Hooks alone
// leave us awaiting-approval; the reconciler injects approval_resolved.
func TestReduce_DenyResolvedByReconciler(t *testing.T) {
	evs := []SessionEvent{
		ev(0, SessionStarted, nil),
		ev(1, PromptSubmitted, map[string]any{"promptId": "p1"}),
		ev(2, ToolStarted, tool("t1")),
		ev(3, ApprovalRequested, tool("t1")),
	}
	expect(t, Reduce(evs), LifecycleRunning, ActivityAwaitingApproval, 1)

	// Each branch gets its own copy: appending to a shared slice with spare
	// capacity would alias the backing array across cases.
	with := func(outcome string) []SessionEvent {
		return append(append([]SessionEvent(nil), evs...), ev(4, ApprovalResolved, map[string]any{"toolUseId": "t1", "outcome": outcome}))
	}
	expect(t, Reduce(with("denied")), LifecycleRunning, ActivityIdle, 0)
	expect(t, Reduce(with("canceled")), LifecycleRunning, ActivityIdle, 0)
	expect(t, Reduce(with("approved")), LifecycleRunning, ActivityWorking, 0)
}

// Hook-only fallback: the user typing a new prompt implicitly resolves the
// previous prompt's pending approval.
func TestReduce_NewPromptClearsStalePending(t *testing.T) {
	evs := []SessionEvent{
		ev(0, SessionStarted, nil),
		ev(1, PromptSubmitted, map[string]any{"promptId": "p1"}),
		ev(2, ApprovalRequested, tool("t1")),
		ev(3, PromptSubmitted, map[string]any{"promptId": "p2"}),
	}
	expect(t, Reduce(evs), LifecycleRunning, ActivityWorking, 0)
}

func TestReduce_MultiplePendingResolvedIndividually(t *testing.T) {
	evs := []SessionEvent{
		ev(0, SessionStarted, nil),
		ev(1, ApprovalRequested, tool("t1")),
		ev(2, ApprovalRequested, tool("t2")),
		ev(3, ToolCompleted, tool("t1")),
	}
	st := Reduce(evs)
	expect(t, st, LifecycleRunning, ActivityAwaitingApproval, 1)
	if st.Pending[0].ToolUseID != "t2" {
		t.Fatalf("wrong pending left: %+v", st.Pending)
	}
	// Duplicate request for the same id is not double-counted.
	evs = append(evs, ev(4, ApprovalRequested, tool("t2")))
	expect(t, Reduce(evs), LifecycleRunning, ActivityAwaitingApproval, 1)
}

// Parallel hooks can land out of order: completion recorded before the
// request. The request must not create a phantom pending approval.
func TestReduce_OutOfOrder(t *testing.T) {
	evs := []SessionEvent{
		ev(0, SessionStarted, nil),
		ev(3, ToolCompleted, tool("t1")),     // later timestamp...
		ev(2, ApprovalRequested, tool("t1")), // ...but appended first in the file
		ev(1, ToolStarted, tool("t1")),
	}
	expect(t, Reduce(evs), LifecycleRunning, ActivityWorking, 0)

	// Same timestamps (clock granularity): stable order, completion after request.
	same := []SessionEvent{
		ev(0, SessionStarted, nil),
		ev(1, ApprovalRequested, tool("t1")),
		ev(1, ToolCompleted, tool("t1")),
	}
	expect(t, Reduce(same), LifecycleRunning, ActivityWorking, 0)
	// And reversed in the file with equal timestamps: completed map catches it.
	rev := []SessionEvent{
		ev(0, SessionStarted, nil),
		ev(1, ToolCompleted, tool("t1")),
		ev(1, ApprovalRequested, tool("t1")),
	}
	expect(t, Reduce(rev), LifecycleRunning, ActivityWorking, 0)
}

// Stop is a turn boundary, not an exit.
func TestReduce_StopIsNotExit(t *testing.T) {
	evs := []SessionEvent{ev(0, SessionStarted, nil), ev(1, TurnCompleted, nil)}
	expect(t, Reduce(evs), LifecycleRunning, ActivityIdle, 0)
	evs = append(evs, ev(2, SessionEnded, map[string]any{"reason": "prompt_input_exit"}))
	expect(t, Reduce(evs), LifecycleExited, ActivityUnknown, 0)
}

func TestReduce_ProcessExited(t *testing.T) {
	clean := []SessionEvent{ev(0, SessionStarted, nil), ev(1, ProcessExited, map[string]any{"exitCode": 0})}
	st := Reduce(clean)
	expect(t, st, LifecycleExited, ActivityUnknown, 0)
	if st.ExitCode == nil || *st.ExitCode != 0 {
		t.Fatalf("exit code: %v", st.ExitCode)
	}

	// JSON round-trips numbers as float64.
	failed := []SessionEvent{ev(0, SessionStarted, nil), ev(1, ProcessExited, map[string]any{"exitCode": float64(2)})}
	st = Reduce(failed)
	expect(t, st, LifecycleFailed, ActivityUnknown, 0)

	// Signal death with a pending approval: exited, pending cleared, signal kept.
	sig := []SessionEvent{ev(0, SessionStarted, nil), ev(1, ApprovalRequested, tool("t1")), ev(2, ProcessExited, map[string]any{"exitCode": -1, "signal": "hangup"})}
	st = Reduce(sig)
	expect(t, st, LifecycleExited, ActivityUnknown, 0)
	if st.Signal != "hangup" {
		t.Fatalf("signal: %q", st.Signal)
	}
}

// A resolution arriving after exit must not resurrect activity.
func TestReduce_ResolutionAfterExitIgnored(t *testing.T) {
	evs := []SessionEvent{
		ev(0, SessionStarted, nil),
		ev(1, ApprovalRequested, tool("t1")),
		ev(2, SessionEnded, nil),
		ev(3, ApprovalResolved, map[string]any{"toolUseId": "t1", "outcome": "denied"}),
	}
	expect(t, Reduce(evs), LifecycleExited, ActivityUnknown, 0)
}

func TestReduce_DoesNotMutateInput(t *testing.T) {
	evs := []SessionEvent{ev(2, TurnCompleted, nil), ev(1, SessionStarted, nil)}
	_ = Reduce(evs)
	if evs[0].Type != TurnCompleted {
		t.Fatal("Reduce must sort a copy, not the caller's slice")
	}
}
