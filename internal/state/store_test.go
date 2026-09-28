package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStore_ReadMissingIsEmptyState(t *testing.T) {
	s := newStore(t)
	st, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != core.StateVersion || len(st.Workspaces) != 0 || len(st.Sessions) != 0 {
		t.Fatalf("unexpected empty state: %+v", st)
	}
}

func TestStore_UpdateThenRead(t *testing.T) {
	s := newStore(t)
	err := s.Update(context.Background(), func(st *core.State) error {
		st.Workspaces = append(st.Workspaces, core.Workspace{ID: "ws_1", Name: "feature-x", Status: core.WorkspaceReady})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Workspaces) != 1 || st.Workspaces[0].Name != "feature-x" || st.Version != core.StateVersion {
		t.Fatalf("round trip failed: %+v", st)
	}
	// No temp files left behind; state file has restrictive perms.
	entries, _ := os.ReadDir(s.Dir())
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
	info, _ := os.Stat(filepath.Join(s.Dir(), stateFile))
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state.json perm = %o, want 600", perm)
	}
}

func TestStore_UpdateErrorWritesNothing(t *testing.T) {
	s := newStore(t)
	_ = s.Update(context.Background(), func(st *core.State) error {
		st.Workspaces = append(st.Workspaces, core.Workspace{ID: "ws_keep"})
		return nil
	})
	sentinel := errors.New("boom")
	err := s.Update(context.Background(), func(st *core.State) error {
		st.Workspaces = nil // would wipe everything if written
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
	st, _ := s.Read()
	if len(st.Workspaces) != 1 || st.Workspaces[0].ID != "ws_keep" {
		t.Fatalf("failed Update must not persist: %+v", st)
	}
}

func TestStore_RejectsNewerSchema(t *testing.T) {
	s := newStore(t)
	future, _ := json.Marshal(map[string]any{"version": core.StateVersion + 1})
	if err := os.WriteFile(filepath.Join(s.Dir(), stateFile), future, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(); err == nil || !strings.Contains(err.Error(), "upgrade dmux") {
		t.Fatalf("want schema error, got %v", err)
	}
}

func TestStore_RejectsCorruptFile(t *testing.T) {
	s := newStore(t)
	if err := os.WriteFile(filepath.Join(s.Dir(), stateFile), []byte(`{"version": 1, "workspaces": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(); err == nil {
		t.Fatal("want parse error for truncated file")
	}
}

// Many goroutines each append one session through Update. Without the lock,
// read-modify-write races would lose records. All N must survive.
func TestStore_ConcurrentUpdatesLoseNothing(t *testing.T) {
	s := newStore(t)
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- s.Update(context.Background(), func(st *core.State) error {
				st.Sessions = append(st.Sessions, core.Session{ID: fmt.Sprintf("s_%02d", i)})
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, _ := s.Read()
	if len(st.Sessions) != n {
		t.Fatalf("lost updates: got %d sessions, want %d", len(st.Sessions), n)
	}
}

// The lock must be exclusive across *processes*, not just goroutines: a
// child process holds the lock while the parent tries to Update.
func TestStore_LockIsCrossProcess(t *testing.T) {
	if os.Getenv("DMUX_TEST_LOCK_HOLDER") != "" {
		// Child: hold the lock for a while, then exit.
		s, _ := Open(os.Getenv("DMUX_TEST_LOCK_DIR"))
		_ = s.Update(context.Background(), func(*core.State) error {
			fmt.Println("locked")
			time.Sleep(600 * time.Millisecond)
			return nil
		})
		os.Exit(0)
	}

	s := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStore_LockIsCrossProcess$")
	cmd.Env = append(os.Environ(), "DMUX_TEST_LOCK_HOLDER=1", "DMUX_TEST_LOCK_DIR="+s.Dir())
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })

	// Wait until the child reports it holds the lock.
	buf := make([]byte, 16)
	if _, err := stdout.Read(buf); err != nil || !strings.HasPrefix(string(buf), "locked") {
		t.Fatalf("child did not acquire lock: %q %v", buf, err)
	}

	start := time.Now()
	err = s.Update(context.Background(), func(st *core.State) error {
		st.Workspaces = append(st.Workspaces, core.Workspace{ID: "ws_parent"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 300*time.Millisecond {
		t.Fatalf("parent Update returned after %v; expected to block on child's lock", waited)
	}
}

func TestStore_LockTimeout(t *testing.T) {
	s := newStore(t)
	// Hold the lock in-process via a second Store instance's Update that never returns
	// until we say so, and shrink the deadline via context.
	release := make(chan struct{})
	holding := make(chan struct{})
	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_ = s.Update(context.Background(), func(*core.State) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := s.Update(ctx, func(*core.State) error { return nil })
	close(release)
	<-holderDone // let the holder finish writing before TempDir cleanup
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("want ErrLockTimeout, got %v", err)
	}
}
