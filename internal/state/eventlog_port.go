package state

import "github.com/bradhu25/devin-mux/internal/core"

// EventLog adapts the JSONL event files to core.EventLog.
type EventLog struct{}

var _ core.EventLog = EventLog{}

// Read implements core.EventLog.
func (EventLog) Read(sessionID string) ([]core.SessionEvent, error) {
	return ReadSessionEvents(sessionID)
}
