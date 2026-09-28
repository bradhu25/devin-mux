// Package hooks receives raw Devin lifecycle hook payloads and appends them
// to the per-session event log. Normalization into core.SessionEvent lives
// here too (normalize.go, Spike 3 onward).
package hooks

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/bradhu25/devin-mux/internal/state"
)

// Input is everything a hook invocation knows: the correlation id injected by
// `dmux spawn`, Devin's project dir, and the raw JSON payload from stdin.
type Input struct {
	DmuxSessionID string
	ProjectDir    string
	Payload       []byte
}

// rawEvent is the Spike 3 wire format: the untouched Devin payload wrapped
// with correlation metadata. Kept verbatim so we can study real payloads
// before committing to the normalized SessionEvent contract.
type rawEvent struct {
	ReceivedAt    time.Time       `json:"receivedAt"`
	DmuxSessionID string          `json:"dmuxSessionId"`
	ProjectDir    string          `json:"projectDir,omitempty"`
	HookEvent     string          `json:"hookEvent,omitempty"`
	DevinSession  string          `json:"devinSessionId,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

// ErrNotManaged is returned for hook invocations from Devin sessions that
// dmux did not spawn (no DMUX_SESSION_ID). They are ignored by design.
var ErrNotManaged = errors.New("hook event from a session not managed by dmux")

// Record appends the payload to ~/.devin-mux/events/<dmuxSessionId>.jsonl.
// Events without a dmux session id are dropped: dmux only observes sessions
// it spawned.
func Record(in Input) error {
	if in.DmuxSessionID == "" {
		return ErrNotManaged
	}
	var meta struct {
		HookEventName string `json:"hook_event_name"`
		SessionID     string `json:"session_id"`
	}
	_ = json.Unmarshal(in.Payload, &meta) // best effort; payload kept raw regardless

	payload := json.RawMessage(in.Payload)
	if !json.Valid(payload) {
		payload, _ = json.Marshal(string(in.Payload))
	}
	line, err := json.Marshal(rawEvent{
		ReceivedAt:    time.Now().UTC(),
		DmuxSessionID: in.DmuxSessionID,
		ProjectDir:    in.ProjectDir,
		HookEvent:     meta.HookEventName,
		DevinSession:  meta.SessionID,
		Payload:       payload,
	})
	if err != nil {
		return err
	}
	return state.AppendEvent(in.DmuxSessionID, line)
}
