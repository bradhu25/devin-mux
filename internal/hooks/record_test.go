package hooks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
	"github.com/bradhu25/devin-mux/internal/state"
)

func readEvents(t *testing.T, id string) []map[string]any {
	t.Helper()
	dir, _ := state.EventsDir()
	f, err := os.Open(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func TestRecord_NormalizedEventWithProjectDir(t *testing.T) {
	t.Setenv(state.EnvRoot, t.TempDir())
	r := &Recorder{}
	err := r.Record(Input{DmuxSessionID: "s_abc", ProjectDir: "/ws/feature-x", Payload: []byte(captured["PreToolUse"])})
	if err != nil {
		t.Fatal(err)
	}
	evs := readEvents(t, "s_abc")
	if len(evs) != 1 || evs[0]["type"] != "tool_started" || evs[0]["devinSessionId"] != "olive-turkey" {
		t.Fatalf("events: %+v", evs)
	}
	data := evs[0]["data"].(map[string]any)
	if data["toolUseId"] != "toolu_01RwpC" || data["projectDir"] != "/ws/feature-x" {
		t.Fatalf("data: %+v", data)
	}
}

func TestRecord_SessionStartCapturesDevinSessionID(t *testing.T) {
	t.Setenv(state.EnvRoot, t.TempDir())
	store, _ := state.OpenDefault()
	_ = store.Update(context.Background(), func(st *core.State) error {
		st.Sessions = append(st.Sessions, core.Session{ID: "s_abc", WorkspaceID: "ws_1"})
		return nil
	})
	r := &Recorder{Store: store}
	if err := r.Record(Input{DmuxSessionID: "s_abc", Payload: []byte(captured["SessionStart"])}); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Read()
	if got := st.Session("s_abc").DevinSessionID; got != "olive-turkey" {
		t.Fatalf("devinSessionId not captured: %q", got)
	}
	// Resume of the same conversation: no change, no error.
	if err := r.Record(Input{DmuxSessionID: "s_abc", Payload: []byte(captured["SessionStartR"])}); err != nil {
		t.Fatal(err)
	}
	// Unknown dmux session: event still written, capture skipped silently.
	if err := r.Record(Input{DmuxSessionID: "s_unknown", Payload: []byte(captured["SessionStart"])}); err != nil {
		t.Fatal(err)
	}
	if len(readEvents(t, "s_unknown")) != 1 {
		t.Fatal("event for unknown session should still be logged")
	}
}

func TestRecord_UnknownHookKeptAsRaw(t *testing.T) {
	t.Setenv(state.EnvRoot, t.TempDir())
	r := &Recorder{}
	if err := r.Record(Input{DmuxSessionID: "s_x", Payload: []byte(`{"hook_event_name":"PostCompaction","session_id":"olive-turkey","summary":"..."}`)}); err != nil {
		t.Fatal(err)
	}
	evs := readEvents(t, "s_x")
	if len(evs) != 1 || evs[0]["type"] != "raw" || evs[0]["hookEventName"] != "PostCompaction" {
		t.Fatalf("raw record: %+v", evs)
	}
}

func TestRecord_IgnoresUnmanagedSessions(t *testing.T) {
	t.Setenv(state.EnvRoot, t.TempDir())
	if err := (&Recorder{}).Record(Input{Payload: []byte(`{}`)}); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("want ErrNotManaged, got %v", err)
	}
}

func TestRecord_MalformedPayloadErrors(t *testing.T) {
	t.Setenv(state.EnvRoot, t.TempDir())
	if err := (&Recorder{}).Record(Input{DmuxSessionID: "s_x", Payload: []byte("not json")}); err == nil {
		t.Fatal("expected error")
	}
}

// The store update is bounded so a held lock cannot stall Devin's hook.
func TestRecord_StoreLockDoesNotStallHook(t *testing.T) {
	t.Setenv(state.EnvRoot, t.TempDir())
	store, _ := state.OpenDefault()
	_ = store.Update(context.Background(), func(st *core.State) error {
		st.Sessions = append(st.Sessions, core.Session{ID: "s_abc"})
		return nil
	})
	holding, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = store.Update(context.Background(), func(*core.State) error { close(holding); <-release; return nil })
	}()
	<-holding
	r := &Recorder{Store: store, StoreTimeout: 100 * time.Millisecond}
	start := time.Now()
	err := r.Record(Input{DmuxSessionID: "s_abc", Payload: []byte(captured["SessionStart"])})
	close(release)
	<-done
	if err != nil {
		t.Fatalf("hook must not fail because of a lock: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("hook stalled on the state lock")
	}
	if len(readEvents(t, "s_abc")) != 1 {
		t.Fatal("event must be written even when capture is skipped")
	}
}
