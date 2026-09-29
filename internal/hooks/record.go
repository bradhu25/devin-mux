// Package hooks receives raw Devin lifecycle hook payloads, normalizes
// them into core.SessionEvents, and appends them to the per-session event
// log. It runs inside `dmux hook-event`, synchronously in Devin's tool
// loop, so everything here must be fast and must never fail loudly.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
	"github.com/bradhu25/devin-mux/internal/state"
)

// Input is everything a hook invocation knows: the correlation id injected by
// `dmux spawn`, Devin's project dir, and the raw JSON payload from stdin.
type Input struct {
	DmuxSessionID string
	ProjectDir    string
	Payload       []byte
}

// ErrNotManaged is returned for hook invocations from Devin sessions that
// dmux did not spawn (no DMUX_SESSION_ID). They are ignored by design.
var ErrNotManaged = errors.New("hook event from a session not managed by dmux")

// Recorder writes normalized events and enriches the Session record from
// them: on SessionStart it captures the Devin session id (for `dmux
// resume`); on the first UserPromptSubmit of a session spawned without -t it
// adopts the prompt as the session's task.
type Recorder struct {
	// Store may be nil (then record enrichment is skipped).
	Store core.Store
	// StoreTimeout bounds the state update; the hook must not stall Devin
	// behind a slow lock. Defaults to 500ms.
	StoreTimeout time.Duration
	// RenameWindow, if set, renames the session's tmux window when a task
	// is adopted. Best effort.
	RenameWindow func(windowID, name string) error
	Now          func() time.Time
}

// Record normalizes and appends the event. Unknown hook names are recorded
// verbatim as a raw line (never dropped) so new Devin events remain
// inspectable. Events without a dmux session id are ignored.
func (r *Recorder) Record(in Input) error {
	if in.DmuxSessionID == "" {
		return ErrNotManaged
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now()
	}
	ev, err := Normalize(in.DmuxSessionID, in.Payload, now)
	if err != nil {
		if errors.Is(err, ErrUnknownHook) {
			return recordRaw(in, now)
		}
		return err
	}
	if in.ProjectDir != "" {
		if ev.Data == nil {
			ev.Data = map[string]any{}
		}
		ev.Data["projectDir"] = in.ProjectDir
	}
	if err := state.AppendSessionEvent(ev); err != nil {
		return err
	}
	if r.Store != nil {
		switch {
		case ev.Type == core.SessionStarted && ev.DevinSessionID != "":
			r.captureDevinSessionID(ev.SessionID, ev.DevinSessionID)
		case ev.Type == core.PromptSubmitted:
			if prompt, _ := ev.Data["prompt"].(string); prompt != "" {
				r.adoptTask(ev.SessionID, prompt)
			}
		}
	}
	return nil
}

// adoptTask fills an empty Session.Task with the first prompt the user
// typed, so sessions spawned without -t become findable by task text. It
// never overwrites a task that was given or already adopted.
func (r *Recorder) adoptTask(sessionID, prompt string) {
	timeout := r.StoreTimeout
	if timeout == 0 {
		timeout = 500 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var windowID string
	err := r.Store.Update(ctx, func(st *core.State) error {
		s := st.Session(sessionID)
		if s == nil {
			return errors.New("unknown session")
		}
		if s.Task != "" {
			return errors.New("task already set") // skip the write
		}
		s.Task = prompt
		windowID = s.Tmux.WindowID
		return nil
	})
	if err == nil && windowID != "" && r.RenameWindow != nil {
		_ = r.RenameWindow(windowID, core.WindowName(prompt, sessionID))
	}
}

// captureDevinSessionID is best-effort: a failure here only means resume
// must fall back to the event log, which also carries the id.
func (r *Recorder) captureDevinSessionID(sessionID, devinID string) {
	timeout := r.StoreTimeout
	if timeout == 0 {
		timeout = 500 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = r.Store.Update(ctx, func(st *core.State) error {
		s := st.Session(sessionID)
		if s == nil {
			return errors.New("unknown session")
		}
		if s.DevinSessionID == devinID {
			return errors.New("unchanged") // skip the write
		}
		s.DevinSessionID = devinID
		return nil
	})
}

// recordRaw keeps unmapped hook payloads as a generic line.
func recordRaw(in Input, now time.Time) error {
	var meta struct {
		HookEventName string `json:"hook_event_name"`
		SessionID     string `json:"session_id"`
	}
	_ = json.Unmarshal(in.Payload, &meta)
	payload := json.RawMessage(in.Payload)
	if !json.Valid(payload) {
		payload, _ = json.Marshal(string(in.Payload))
	}
	line, err := json.Marshal(struct {
		EventID       string          `json:"eventId"`
		SessionID     string          `json:"sessionId"`
		DevinSession  string          `json:"devinSessionId,omitempty"`
		Type          string          `json:"type"`
		Timestamp     time.Time       `json:"timestamp"`
		HookEventName string          `json:"hookEventName,omitempty"`
		Payload       json.RawMessage `json:"payload"`
	}{core.NewEventID(), in.DmuxSessionID, meta.SessionID, "raw", now, meta.HookEventName, payload})
	if err != nil {
		return err
	}
	return state.AppendEvent(in.DmuxSessionID, line)
}
