package devin

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/bradhu25/devin-mux/internal/core"
)

// Blobs are verbatim from Devin 3000.11.3 sessions.db (Spike 3), trimmed.
const (
	blobCompleted = `{"toolCallId":"toolu_A","status":"completed","_meta":{"cognition.ai/cwd":"/tmp/ws","terminal_exit":{"terminal_id":"56bd11","exit_code":0,"signal":null},"cognition.ai/inferenceToolName":"exec"}}`
	blobRejected  = `{"toolCallId":"toolu_B","status":"failed","content":[{"type":"content","content":{"type":"text","text":"Tool execution was rejected by the user"}}],"_meta":{"cognition.ai/rejected":true,"cognition.ai/inferenceToolName":"exec"}}`
	blobCanceled  = `{"toolCallId":"toolu_C","status":"failed","content":[{"type":"content","content":{"type":"text","text":"Canceled due to user interrupt"}}],"_meta":{"cognition.ai/canceled":true,"cognition.ai/inferenceToolName":"exec"}}`
	blobRanFailed = `{"toolCallId":"toolu_D","status":"failed","_meta":{"terminal_exit":{"exit_code":1}}}`
)

// fixtureDB builds a sessions.db with Devin's real tool_call_state schema.
func fixtureDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	stmts := []string{
		`PRAGMA journal_mode=wal`,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, working_directory TEXT NOT NULL)`,
		`CREATE TABLE tool_call_state (session_id TEXT NOT NULL, tool_call_id TEXT NOT NULL, tool_call_json TEXT, tool_call_update_json TEXT, PRIMARY KEY (session_id, tool_call_id))`,
		`INSERT INTO sessions VALUES ('olive-turkey','/tmp/ws'), ('other','/tmp/o')`,
		`INSERT INTO tool_call_state VALUES ('olive-turkey','toolu_A','{}','` + blobCompleted + `')`,
		`INSERT INTO tool_call_state VALUES ('olive-turkey','toolu_B','{}','` + blobRejected + `')`,
		`INSERT INTO tool_call_state VALUES ('olive-turkey','toolu_C','{}','` + blobCanceled + `')`,
		`INSERT INTO tool_call_state VALUES ('olive-turkey','toolu_D','{}','` + blobRanFailed + `')`,
		`INSERT INTO tool_call_state VALUES ('olive-turkey','toolu_P','{}',NULL)`, // still pending: no update yet
		`INSERT INTO tool_call_state VALUES ('olive-turkey','toolu_G','{}','garbage')`,
		`INSERT INTO tool_call_state VALUES ('other','toolu_A','{}','` + blobRejected + `')`, // same id, different session
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return path
}

func TestToolCallOutcomes(t *testing.T) {
	a := &Adapter{StorePath: fixtureDB(t)}
	got, err := a.ToolCallOutcomes(context.Background(), "olive-turkey", []string{"toolu_A", "toolu_B", "toolu_C", "toolu_D", "toolu_P", "toolu_G", "toolu_missing"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]core.ApprovalOutcome{"toolu_A": core.OutcomeApproved, "toolu_B": core.OutcomeDenied, "toolu_C": core.OutcomeCanceled, "toolu_D": core.OutcomeApproved}
	if len(got) != len(want) {
		t.Fatalf("got %d outcomes %+v, want %d", len(got), got, len(want))
	}
	for _, o := range got {
		if want[o.ToolUseID] != o.Outcome {
			t.Errorf("%s: got %s want %s", o.ToolUseID, o.Outcome, want[o.ToolUseID])
		}
	}
}

func TestToolCallOutcomes_ScopedToSession(t *testing.T) {
	a := &Adapter{StorePath: fixtureDB(t)}
	got, err := a.ToolCallOutcomes(context.Background(), "other", []string{"toolu_A", "toolu_B"})
	if err != nil || len(got) != 1 || got[0].Outcome != core.OutcomeDenied {
		t.Fatalf("session scoping failed: %+v %v", got, err)
	}
}

func TestToolCallOutcomes_EdgeCases(t *testing.T) {
	a := &Adapter{StorePath: fixtureDB(t)}
	ctx := context.Background()
	if got, err := a.ToolCallOutcomes(ctx, "", []string{"x"}); err != nil || got != nil {
		t.Fatalf("empty session: %v %v", got, err)
	}
	if got, err := a.ToolCallOutcomes(ctx, "olive-turkey", nil); err != nil || got != nil {
		t.Fatalf("no ids: %v %v", got, err)
	}
	missing := &Adapter{StorePath: filepath.Join(t.TempDir(), "nope.db")}
	if _, err := missing.ToolCallOutcomes(ctx, "olive-turkey", []string{"x"}); err == nil {
		t.Fatal("missing store must error (caller treats as unknown)")
	}
}

// Reading must never modify Devin's database. A WAL-mode database requires
// the standard -wal/-shm side files for any reader (SQLite creates them if
// absent, and a read-only connection cannot remove them on close); Devin
// creates the same files whenever it runs, so they are permitted. Nothing
// else may appear, and the main file's bytes must be unchanged.
func TestToolCallOutcomes_ReadOnlyNoSideEffects(t *testing.T) {
	path := fixtureDB(t)
	a := &Adapter{StorePath: path}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ToolCallOutcomes(context.Background(), "olive-turkey", []string{"toolu_A"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("database file content changed during a read")
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*"))
	for _, m := range matches {
		base := filepath.Base(m)
		if base != "sessions.db" && base != "sessions.db-wal" && base != "sessions.db-shm" {
			t.Fatalf("unexpected file created: %s", base)
		}
	}
}

func TestClassifyUpdate(t *testing.T) {
	cases := map[string]struct {
		out core.ApprovalOutcome
		ok  bool
	}{
		blobCompleted:              {core.OutcomeApproved, true},
		blobRejected:               {core.OutcomeDenied, true},
		blobCanceled:               {core.OutcomeCanceled, true},
		blobRanFailed:              {core.OutcomeApproved, true},
		`{"status":"in_progress"}`: {"", false},
		`not json`:                 {"", false},
	}
	for raw, want := range cases {
		out, ok := classifyUpdate(raw)
		if out != want.out || ok != want.ok {
			t.Errorf("classify(%.40s) = %s,%v want %s,%v", raw, out, ok, want.out, want.ok)
		}
	}
}
