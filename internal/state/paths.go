// Package state owns everything under ~/.devin-mux: paths, the atomic
// state.json store, the flock-guarded Update transaction, and the
// append-only per-session event logs.
package state

import (
	"os"
	"path/filepath"
)

// EnvRoot overrides the state root directory (used by tests and spikes).
const EnvRoot = "DMUX_ROOT"

// Root returns the dmux state directory: $DMUX_ROOT or ~/.devin-mux.
func Root() (string, error) {
	if r := os.Getenv(EnvRoot); r != "" {
		return r, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".devin-mux"), nil
}

// EventsDir returns the directory holding per-session JSONL event logs.
func EventsDir() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "events"), nil
}
