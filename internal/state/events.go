package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AppendEvent appends one JSONL line to events/<sessionID>.jsonl.
//
// Concurrency contract (see PLAN.md "Persistence"): many short-lived writers
// (each hook invocation, the launch wrapper) append to the same file. This is
// safe without locking because the file is opened O_APPEND and each line is
// emitted in exactly one write() call, which POSIX guarantees will not
// interleave with other appenders. Never split a line across writes.
func AppendEvent(sessionID string, line []byte) error {
	if !validSessionID(sessionID) {
		return fmt.Errorf("invalid session id %q", sessionID)
	}
	dir, err := EventsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, sessionID+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	buf := make([]byte, 0, len(line)+1)
	buf = append(buf, line...)
	if len(buf) == 0 || buf[len(buf)-1] != '\n' {
		buf = append(buf, '\n')
	}
	n, werr := f.Write(buf)
	cerr := f.Close()
	switch {
	case werr != nil:
		return werr
	case n != len(buf):
		return errors.New("short write to event log")
	default:
		return cerr
	}
}

// validSessionID guards against path traversal: ids are used as file names.
func validSessionID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		ok := r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return !strings.HasPrefix(id, "-")
}
