package core

import (
	"sort"
	"time"
)

// PendingApproval is a permission request with no observed resolution.
type PendingApproval struct {
	ToolUseID   string
	ToolName    string
	PromptID    string
	RequestedAt time.Time
}

// Status is the state derived from a session's event stream. It is never
// persisted (PLAN.md: derived state is recomputed on read).
type Status struct {
	Lifecycle Lifecycle
	Activity  Activity
	// Pending approvals in request order. Non-empty => Activity is
	// awaiting-approval while the process is alive.
	Pending []PendingApproval
	// InFlight lists tool calls started but not yet completed or resolved.
	// Non-empty means real work is running (a long test, a build), however
	// long it has been silent.
	InFlight []string
	// Observations useful for display.
	DevinSessionID  string
	CurrentPromptID string
	LastPrompt      string
	LastMessage     string
	LastEventAt     time.Time
	LastEventType   EventType
	EventCount      int
	// ExitCode/Signal are set once process_exited is observed.
	ExitCode *int
	Signal   string
}

// Reduce folds events into a Status. Events are sorted by timestamp first
// (stable), because parallel hook invocations may append out of order.
//
// Rules (see PLAN.md "Live status architecture"):
//   - session_started         => running, idle (waiting for the first prompt;
//     clears pending approvals and exit info from a previous run)
//   - prompt_submitted / tool_started => running, working
//   - approval_requested(id)  => pending += id => awaiting-approval
//   - tool_completed(id)      => pending -= id (approved and ran)
//   - approval_resolved(id)   => pending -= id; denied/canceled => idle
//   - turn_completed (Stop)   => idle. NOT exited.
//   - prompt_submitted with a new promptId implicitly resolves pending
//     approvals from earlier prompts (the user is typing again)
//   - session_ended / process_exited => exited (failed on non-zero exit);
//     activity unknown; pending cleared
func Reduce(events []SessionEvent) Status {
	evs := make([]SessionEvent, len(events))
	copy(evs, events)
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].Timestamp.Before(evs[j].Timestamp) })

	st := Status{Lifecycle: LifecycleStarting, Activity: ActivityUnknown}
	completed := map[string]bool{} // toolUseIds already completed (for out-of-order arrivals)
	alive := func() bool { return st.Lifecycle == LifecycleRunning || st.Lifecycle == LifecycleStarting }

	for _, ev := range evs {
		st.EventCount++
		st.LastEventAt, st.LastEventType = ev.Timestamp, ev.Type
		if ev.DevinSessionID != "" {
			st.DevinSessionID = ev.DevinSessionID
		}
		id := str(ev.Data, "toolUseId")

		switch ev.Type {
		case SessionStarted:
			// Fresh start or resume: the agent is at its input box until a
			// prompt arrives. A new start also supersedes the previous run.
			st.Lifecycle, st.Activity = LifecycleRunning, ActivityIdle
			st.Pending, st.InFlight, st.ExitCode, st.Signal = nil, nil, nil, ""

		case PromptSubmitted:
			if pid := str(ev.Data, "promptId"); pid != "" && pid != st.CurrentPromptID {
				st.CurrentPromptID = pid
				st.Pending = nil // earlier prompt's approvals are moot
			}
			st.LastPrompt = str(ev.Data, "prompt")
			st.Lifecycle, st.Activity = LifecycleRunning, ActivityWorking

		case ToolStarted:
			st.Lifecycle = LifecycleRunning
			if id != "" && !completed[id] && !contains(st.InFlight, id) {
				st.InFlight = append(st.InFlight, id)
			}
			if len(st.Pending) == 0 {
				st.Activity = ActivityWorking
			}

		case ApprovalRequested:
			st.Lifecycle = LifecycleRunning
			if id != "" && completed[id] {
				break // resolution already observed; out-of-order arrival
			}
			if id == "" || !hasPending(st.Pending, id) {
				st.Pending = append(st.Pending, PendingApproval{ToolUseID: id, ToolName: str(ev.Data, "toolName"), PromptID: str(ev.Data, "promptId"), RequestedAt: ev.Timestamp})
			}
			st.Activity = ActivityAwaitingApproval

		case ToolCompleted:
			st.Lifecycle = LifecycleRunning
			if id != "" {
				completed[id] = true
				st.Pending = removePending(st.Pending, id)
				st.InFlight = removeString(st.InFlight, id)
			}
			if len(st.Pending) == 0 {
				st.Activity = ActivityWorking
			}

		case ApprovalResolved:
			if id != "" {
				completed[id] = true
				st.Pending = removePending(st.Pending, id)
				if o := ApprovalOutcome(str(ev.Data, "outcome")); o != OutcomeApproved {
					st.InFlight = removeString(st.InFlight, id)
				}
			}
			if alive() && len(st.Pending) == 0 {
				switch ApprovalOutcome(str(ev.Data, "outcome")) {
				case OutcomeDenied, OutcomeCanceled, OutcomeUnknown:
					st.Activity = ActivityIdle
				default:
					st.Activity = ActivityWorking
				}
			}

		case TurnCompleted:
			st.LastMessage = str(ev.Data, "lastMessage")
			st.Lifecycle, st.Activity = LifecycleRunning, ActivityIdle
			st.Pending, st.InFlight = nil, nil

		case SessionEnded:
			st.Lifecycle, st.Activity, st.Pending, st.InFlight = LifecycleExited, ActivityUnknown, nil, nil

		case ProcessExited:
			st.Lifecycle, st.Activity, st.Pending, st.InFlight = LifecycleExited, ActivityUnknown, nil, nil
			if code, ok := num(ev.Data, "exitCode"); ok {
				c := code
				st.ExitCode = &c
				if code > 0 {
					st.Lifecycle = LifecycleFailed
				}
			}
			st.Signal = str(ev.Data, "signal")
		}
	}
	return st
}

func hasPending(p []PendingApproval, id string) bool {
	for _, a := range p {
		if a.ToolUseID == id {
			return true
		}
	}
	return false
}

func removePending(p []PendingApproval, id string) []PendingApproval {
	out := p[:0]
	for _, a := range p {
		if a.ToolUseID != id {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func str(d map[string]any, k string) string {
	if d == nil {
		return ""
	}
	s, _ := d[k].(string)
	return s
}

// num reads an integer that may have round-tripped through JSON as float64.
func num(d map[string]any, k string) (int, bool) {
	if d == nil {
		return 0, false
	}
	switch v := d[k].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func removeString(list []string, s string) []string {
	out := list[:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
