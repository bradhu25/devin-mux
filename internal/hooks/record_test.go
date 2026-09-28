package hooks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bradhu25/devin-mux/internal/state"
)

func TestRecord_WrapsPayloadAndExtractsMetadata(t *testing.T) {
	t.Setenv(state.EnvRoot, t.TempDir())

	payload := `{"hook_event_name":"SessionStart","source":"startup","session_id":"olive-turkey"}`
	err := Record(Input{DmuxSessionID: "s_abc", ProjectDir: "/tmp/ws", Payload: []byte(payload)})
	if err != nil {
		t.Fatal(err)
	}

	dir, _ := state.EventsDir()
	data, err := os.ReadFile(filepath.Join(dir, "s_abc.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var got rawEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &got); err != nil {
		t.Fatal(err)
	}
	if got.DmuxSessionID != "s_abc" || got.HookEvent != "SessionStart" || got.DevinSession != "olive-turkey" || got.ProjectDir != "/tmp/ws" {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	if string(got.Payload) != payload {
		t.Fatalf("payload not preserved verbatim: %s", got.Payload)
	}
}

func TestRecord_IgnoresUnmanagedSessions(t *testing.T) {
	t.Setenv(state.EnvRoot, t.TempDir())
	err := Record(Input{Payload: []byte(`{}`)})
	if !errors.Is(err, ErrNotManaged) {
		t.Fatalf("want ErrNotManaged, got %v", err)
	}
}

func TestRecord_NonJSONPayloadStillRecorded(t *testing.T) {
	t.Setenv(state.EnvRoot, t.TempDir())
	if err := Record(Input{DmuxSessionID: "s_x", Payload: []byte("not json")}); err != nil {
		t.Fatal(err)
	}
}
