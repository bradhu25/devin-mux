package hooks

import (
	"errors"
	"testing"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

// Payloads are verbatim captures from Devin CLI 3000.11.3 (Spike 3,
// 2026-09-28), with only ids shortened.
var captured = map[string]string{
	"SessionStart":      `{"hook_event_name":"SessionStart","source":"startup","session_id":"olive-turkey"}`,
	"SessionStartR":     `{"hook_event_name":"SessionStart","source":"resume","session_id":"olive-turkey"}`,
	"UserPromptSubmit":  `{"hook_event_name":"UserPromptSubmit","prompt":"touch /tmp/x/approve.txt","session_id":"olive-turkey","prompt_id":"efe9d366-1"}`,
	"PreToolUse":        `{"hook_event_name":"PreToolUse","tool_name":"exec","tool_input":{"command":"touch /tmp/x/approve.txt"},"tool_use_id":"toolu_01RwpC","session_id":"olive-turkey","prompt_id":"efe9d366-1"}`,
	"PermissionRequest": `{"hook_event_name":"PermissionRequest","tool_name":"exec","tool_input":{"command":"touch /tmp/x/approve.txt"},"tool_use_id":"toolu_01RwpC","session_id":"olive-turkey","prompt_id":"efe9d366-1"}`,
	"PostToolUse":       `{"hook_event_name":"PostToolUse","tool_name":"exec","tool_input":{"command":"touch /tmp/x/approve.txt"},"tool_use_id":"toolu_01RwpC","tool_response":{"success":true,"output":"Output from command in shell 56bd11:\n\n\nExit code: 0","error":null},"session_id":"olive-turkey","prompt_id":"efe9d366-1"}`,
	"Stop":              `{"hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"Done — file created.","session_id":"olive-turkey","prompt_id":"efe9d366-1"}`,
	"SessionEnd":        `{"hook_event_name":"SessionEnd","reason":"prompt_input_exit","session_id":"olive-turkey","prompt_id":"3f75bda3-1"}`,
}

func TestNormalize_AllCapturedPayloads(t *testing.T) {
	now := time.Date(2026, 9, 28, 22, 0, 0, 0, time.UTC)
	cases := []struct {
		key  string
		typ  core.EventType
		data map[string]any
	}{
		{"SessionStart", core.SessionStarted, map[string]any{"source": "startup"}},
		{"SessionStartR", core.SessionStarted, map[string]any{"source": "resume"}},
		{"UserPromptSubmit", core.PromptSubmitted, map[string]any{"promptId": "efe9d366-1", "prompt": "touch /tmp/x/approve.txt"}},
		{"PreToolUse", core.ToolStarted, map[string]any{"promptId": "efe9d366-1", "toolUseId": "toolu_01RwpC", "toolName": "exec"}},
		{"PermissionRequest", core.ApprovalRequested, map[string]any{"promptId": "efe9d366-1", "toolUseId": "toolu_01RwpC", "toolName": "exec"}},
		{"PostToolUse", core.ToolCompleted, map[string]any{"promptId": "efe9d366-1", "toolUseId": "toolu_01RwpC", "toolName": "exec", "success": true}},
		{"Stop", core.TurnCompleted, map[string]any{"promptId": "efe9d366-1", "lastMessage": "Done — file created."}},
		{"SessionEnd", core.SessionEnded, map[string]any{"promptId": "3f75bda3-1", "reason": "prompt_input_exit"}},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			ev, err := Normalize("s_abc", []byte(captured[tc.key]), now)
			if err != nil {
				t.Fatal(err)
			}
			if ev.Type != tc.typ || ev.SessionID != "s_abc" || ev.DevinSessionID != "olive-turkey" || !ev.Timestamp.Equal(now) || ev.EventID == "" {
				t.Fatalf("envelope: %+v", ev)
			}
			if len(ev.Data) != len(tc.data) {
				t.Fatalf("data keys: got %v want %v", ev.Data, tc.data)
			}
			for k, v := range tc.data {
				if ev.Data[k] != v {
					t.Errorf("data[%s] = %v, want %v", k, ev.Data[k], v)
				}
			}
		})
	}
}

func TestNormalize_UnknownAndMalformed(t *testing.T) {
	if _, err := Normalize("s", []byte(`{"hook_event_name":"PostCompaction","session_id":"x"}`), time.Now()); !errors.Is(err, ErrUnknownHook) {
		t.Fatalf("want ErrUnknownHook, got %v", err)
	}
	if _, err := Normalize("s", []byte(`not json`), time.Now()); err == nil {
		t.Fatal("malformed payload must error")
	}
	// Unknown extra fields are ignored (forward compatibility).
	ev, err := Normalize("s", []byte(`{"hook_event_name":"Stop","session_id":"x","future_field":{"a":1}}`), time.Now())
	if err != nil || ev.Type != core.TurnCompleted {
		t.Fatalf("extra fields must be tolerated: %v %+v", err, ev)
	}
}

func TestNormalize_TruncatesLongText(t *testing.T) {
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'a'
	}
	ev, err := Normalize("s", []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"x","prompt":"`+string(long)+`"}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := ev.Data["prompt"].(string); len([]rune(got)) != 200 {
		t.Fatalf("prompt not truncated to 200 runes: %d", len([]rune(got)))
	}
}
