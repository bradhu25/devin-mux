package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

// rawPayload is the union of fields Devin CLI 3000.11.3 sends on stdin
// across hook events (captured in Spike 3; see PLAN.md). Unknown fields are
// ignored so a newer Devin cannot break normalization.
type rawPayload struct {
	HookEventName        string          `json:"hook_event_name"`
	SessionID            string          `json:"session_id"`
	PromptID             string          `json:"prompt_id"`
	Source               string          `json:"source"`      // SessionStart: startup|resume
	Reason               string          `json:"reason"`      // SessionEnd
	Prompt               string          `json:"prompt"`      // UserPromptSubmit
	ToolName             string          `json:"tool_name"`   // Pre/PostToolUse, PermissionRequest
	ToolUseID            string          `json:"tool_use_id"` // same three
	ToolResponse         *toolResponse   `json:"tool_response"`
	LastAssistantMessage string          `json:"last_assistant_message"` // Stop
	StopHookActive       bool            `json:"stop_hook_active"`
	ToolInput            json.RawMessage `json:"tool_input"`
}

type toolResponse struct {
	Success *bool  `json:"success"`
	Error   string `json:"error"`
}

// ErrUnknownHook is returned for hook event names dmux does not map.
var ErrUnknownHook = errors.New("unknown hook event")

// Normalize maps a raw Devin hook payload to a core.SessionEvent for the
// given dmux session. It never fails on missing optional fields.
func Normalize(dmuxSessionID string, payload []byte, now time.Time) (core.SessionEvent, error) {
	var p rawPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return core.SessionEvent{}, fmt.Errorf("parse hook payload: %w", err)
	}
	typ, ok := eventTypeFor(p.HookEventName)
	if !ok {
		return core.SessionEvent{}, fmt.Errorf("%w: %q", ErrUnknownHook, p.HookEventName)
	}
	ev := core.SessionEvent{
		EventID:        core.NewEventID(),
		SessionID:      dmuxSessionID,
		DevinSessionID: p.SessionID,
		Type:           typ,
		Timestamp:      now,
		Data:           map[string]any{},
	}
	put := func(k, v string) {
		if v != "" {
			ev.Data[k] = v
		}
	}
	put("promptId", p.PromptID)
	put("toolUseId", p.ToolUseID)
	put("toolName", p.ToolName)
	switch typ {
	case core.SessionStarted:
		put("source", p.Source)
	case core.SessionEnded:
		put("reason", p.Reason)
	case core.PromptSubmitted:
		put("prompt", truncate(p.Prompt, 200))
	case core.TurnCompleted:
		put("lastMessage", truncate(p.LastAssistantMessage, 200))
	case core.ToolCompleted:
		if p.ToolResponse != nil {
			if p.ToolResponse.Success != nil {
				ev.Data["success"] = *p.ToolResponse.Success
			}
			put("error", truncate(p.ToolResponse.Error, 200))
		}
	}
	if len(ev.Data) == 0 {
		ev.Data = nil
	}
	return ev, nil
}

func eventTypeFor(hook string) (core.EventType, bool) {
	switch hook {
	case "SessionStart":
		return core.SessionStarted, true
	case "UserPromptSubmit":
		return core.PromptSubmitted, true
	case "PreToolUse":
		return core.ToolStarted, true
	case "PostToolUse":
		return core.ToolCompleted, true
	case "PermissionRequest":
		return core.ApprovalRequested, true
	case "Stop":
		return core.TurnCompleted, true
	case "SessionEnd":
		return core.SessionEnded, true
	}
	return "", false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
