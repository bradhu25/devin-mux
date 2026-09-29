package state

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

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

// ReadSessionEvents loads a session's normalized events. Lines that are not
// normalized events (type "raw", malformed, or a torn final line) are
// skipped: the log is append-only and readers must tolerate a writer that
// is mid-append. A missing file yields no events and no error.
func ReadSessionEvents(sessionID string) ([]core.SessionEvent, error) {
	if !validSessionID(sessionID) {
		return nil, errors.New("invalid session id")
	}
	dir, err := EventsDir()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(dir, sessionID+".jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []core.SessionEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev core.SessionEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Type == "" || ev.Type == "raw" {
			continue
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}
