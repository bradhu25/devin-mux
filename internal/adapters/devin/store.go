package devin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver; keeps dmux a static binary

	"github.com/bradhu25/devin-mux/internal/core"
)

// DefaultStorePath is where Devin CLI keeps its session store.
func DefaultStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "devin", "cli", "sessions.db"), nil
}

// queryTimeout bounds a lookup; the store is WAL-mode SQLite that Devin
// writes concurrently, so reads are normally sub-millisecond.
const queryTimeout = 2 * time.Second

// toolCallUpdate is the subset of Devin's serialized acp::ToolCallUpdate we
// interpret. Schema verified against 3000.11.3 (Spike 3); undocumented
// internal state, so every read is best-effort.
type toolCallUpdate struct {
	Status string `json:"status"` // completed | failed
	Meta   struct {
		Rejected bool `json:"cognition.ai/rejected"`
		Canceled bool `json:"cognition.ai/canceled"`
	} `json:"_meta"`
}

// ToolCallOutcomes reads tool_call_state rows for the given ids. Read-only:
// the connection is opened with mode=ro and immutable=0 so WAL reads stay
// consistent and dmux never creates or modifies journal files.
func (a *Adapter) ToolCallOutcomes(ctx context.Context, devinSessionID string, toolUseIDs []string) ([]core.ToolCallOutcome, error) {
	if devinSessionID == "" || len(toolUseIDs) == 0 {
		return nil, nil
	}
	path := a.StorePath
	if path == "" {
		p, err := DefaultStorePath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("devin store: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	dsn := "file:" + path + "?" + url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "busy_timeout(500)"}}.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(toolUseIDs)), ",")
	args := make([]any, 0, len(toolUseIDs)+1)
	args = append(args, devinSessionID)
	for _, id := range toolUseIDs {
		args = append(args, id)
	}
	rows, err := db.QueryContext(ctx,
		"SELECT tool_call_id, tool_call_update_json FROM tool_call_state WHERE session_id = ? AND tool_call_id IN ("+placeholders+") AND tool_call_update_json IS NOT NULL", args...)
	if err != nil {
		return nil, fmt.Errorf("devin store query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []core.ToolCallOutcome
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if outcome, ok := classifyUpdate(raw); ok {
			out = append(out, core.ToolCallOutcome{ToolUseID: id, Outcome: outcome})
		}
	}
	return out, rows.Err()
}

// classifyUpdate maps a tool_call_update_json blob to an outcome. Unknown
// shapes yield ok=false so the caller treats them as unresolved.
func classifyUpdate(raw string) (core.ApprovalOutcome, bool) {
	var u toolCallUpdate
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return "", false
	}
	switch {
	case u.Meta.Rejected:
		return core.OutcomeDenied, true
	case u.Meta.Canceled:
		return core.OutcomeCanceled, true
	case u.Status == "completed":
		return core.OutcomeApproved, true
	case u.Status == "failed":
		// Ran and failed (e.g. non-zero exit): the permission was granted.
		return core.OutcomeApproved, true
	}
	return "", false
}

// ErrStoreUnavailable can be used by callers to detect the missing-store case.
var ErrStoreUnavailable = errors.New("devin session store unavailable")
