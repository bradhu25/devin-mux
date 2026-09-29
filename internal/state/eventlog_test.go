package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

func TestReadSessionEvents_RoundTripSkipsRawAndTorn(t *testing.T) {
	t.Setenv(EnvRoot, t.TempDir())
	now := time.Date(2026, 9, 28, 22, 0, 0, 0, time.UTC)
	for _, ev := range []core.SessionEvent{
		{EventID: "e1", SessionID: "s_1", Type: core.SessionStarted, Timestamp: now, Data: map[string]any{"source": "startup"}},
		{EventID: "e2", SessionID: "s_1", Type: core.ProcessExited, Timestamp: now.Add(time.Second), Data: map[string]any{"exitCode": 3}},
	} {
		if err := AppendSessionEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
	// A raw (unmapped hook) line and a torn trailing line, as a real log may have.
	if err := AppendEvent("s_1", []byte(`{"eventId":"e3","sessionId":"s_1","type":"raw","payload":{}}`)); err != nil {
		t.Fatal(err)
	}
	dir, _ := EventsDir()
	f, _ := os.OpenFile(filepath.Join(dir, "s_1.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"eventId":"e4","sessionId":"s_1","type":"turn_comp`) // no newline, mid-write
	_ = f.Close()

	evs, err := ReadSessionEvents("s_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Type != core.SessionStarted || evs[1].Type != core.ProcessExited {
		t.Fatalf("events: %+v", evs)
	}
	if code, ok := evs[1].Data["exitCode"].(float64); !ok || code != 3 {
		t.Fatalf("exitCode round trip: %v", evs[1].Data["exitCode"])
	}
	if !evs[0].Timestamp.Equal(now) {
		t.Fatalf("timestamp: %v", evs[0].Timestamp)
	}
}

func TestReadSessionEvents_MissingAndInvalid(t *testing.T) {
	t.Setenv(EnvRoot, t.TempDir())
	evs, err := ReadSessionEvents("s_none")
	if err != nil || evs != nil {
		t.Fatalf("missing log: %v %v", evs, err)
	}
	if _, err := ReadSessionEvents("../x"); err == nil {
		t.Fatal("invalid id must error")
	}
}
