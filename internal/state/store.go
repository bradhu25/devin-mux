package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

	"github.com/bradhu25/devin-mux/internal/core"
)

const (
	stateFile = "state.json"
	lockFile  = "state.lock"

	// LockTimeout bounds how long Update waits for a concurrent writer.
	LockTimeout   = 5 * time.Second
	lockRetryWait = 20 * time.Millisecond
)

// ErrLockTimeout is returned when another dmux process holds the state lock
// for longer than LockTimeout.
var ErrLockTimeout = errors.New("timed out waiting for state lock (another dmux command running?)")

// Store persists core.State under a root directory.
//
// Two guarantees, provided by two different mechanisms (see PLAN.md
// "Persistence"):
//
//   - Atomicity: writes go to a temp file in the same directory, fsync,
//     rename over state.json, fsync the directory. Readers see the previous
//     complete document or the new one, never a torn write.
//   - Isolation: Update serializes read-modify-write across processes with
//     an exclusive flock on state.lock. flock is released by the kernel when
//     the holder dies, so stale locks cannot occur.
//
// Read never takes the lock: it sees an atomic snapshot and can never be
// blocked by a stuck writer.
type Store struct {
	root string
}

// Open returns a Store rooted at dir, creating the directory if needed.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	return &Store{root: dir}, nil
}

// OpenDefault opens the store at Root() ($DMUX_ROOT or ~/.devin-mux).
func OpenDefault() (*Store, error) {
	dir, err := Root()
	if err != nil {
		return nil, err
	}
	return Open(dir)
}

// Dir returns the store's root directory.
func (s *Store) Dir() string { return s.root }

// Read returns the current state. A missing file yields an empty state at
// the current schema version; this is the normal first-run path.
func (s *Store) Read() (*core.State, error) {
	data, err := os.ReadFile(filepath.Join(s.root, stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return &core.State{Version: core.StateVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var st core.State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", stateFile, err)
	}
	if st.Version > core.StateVersion {
		return nil, fmt.Errorf("%s is schema version %d but this dmux understands %d; upgrade dmux", stateFile, st.Version, core.StateVersion)
	}
	return &st, nil
}

// Update runs fn inside a read-modify-write transaction. fn receives the
// current state and mutates it in place; if fn returns nil the result is
// written atomically. If fn returns an error nothing is written.
func (s *Store) Update(ctx context.Context, fn func(*core.State) error) error {
	lock := flock.New(filepath.Join(s.root, lockFile))
	lockCtx, cancel := context.WithTimeout(ctx, LockTimeout)
	defer cancel()
	ok, err := lock.TryLockContext(lockCtx, lockRetryWait)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("acquire state lock: %w", err)
	}
	if !ok {
		return ErrLockTimeout
	}
	defer func() { _ = lock.Unlock() }()

	st, err := s.Read()
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	st.Version = core.StateVersion
	return s.writeAtomic(st)
}

func (s *Store) writeAtomic(st *core.State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(s.root, stateFile+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("fsync temp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp state: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp state: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(s.root, stateFile)); err != nil {
		cleanup()
		return fmt.Errorf("replace state: %w", err)
	}
	return syncDir(s.root)
}

// syncDir fsyncs a directory so the rename is durable. Some platforms
// reject fsync on directories; that is not fatal for a local tool.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	_ = d.Sync()
	return d.Close()
}
