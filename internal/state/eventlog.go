package state

import (
	"encoding/json"

	"github.com/bradhu25/devin-mux/internal/core"
)

// AppendSessionEvent serializes a normalized event and appends it to the
// session's JSONL log via AppendEvent (O_APPEND, single write).
func AppendSessionEvent(ev core.SessionEvent) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return AppendEvent(ev.SessionID, line)
}
