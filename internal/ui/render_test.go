package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/bradhu25/devin-mux/internal/core"
)

func TestRenderSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 28, 22, 0, 0, 0, time.UTC)
	snap := &core.Snapshot{TakenAt: now, Workspaces: []core.WorkspaceView{
		{Workspace: core.Workspace{Name: "empty", Status: core.WorkspaceReady}},
		{Workspace: core.Workspace{Name: "feature-x"}, Sessions: []core.SessionView{
			{Session: core.Session{ID: "s_1", Task: "auth implementation", Repos: []core.WorkspaceRepo{{Name: "api", Branch: "dmux/feature-x/auth"}, {Name: "web", Branch: "dmux/feature-x/auth"}}}, Liveness: core.LivenessAlive,
				Status: core.Status{Lifecycle: core.LifecycleRunning, Activity: core.ActivityWorking, LastEventAt: now.Add(-5 * time.Second), LastEventType: core.ToolStarted, DevinSessionID: "olive-turkey"}},
			{Session: core.Session{ID: "s_2", Task: "security review", SharedWith: "s_1"}, Liveness: core.LivenessAlive,
				Status:   core.Status{Lifecycle: core.LifecycleRunning, Activity: core.ActivityAwaitingApproval, Pending: []core.PendingApproval{{ToolUseID: "t", ToolName: "exec"}}, LastEventAt: now.Add(-3 * time.Minute), LastEventType: core.ApprovalRequested},
				Evidence: []string{"devin store unavailable; approval state unverified"}},
			{Session: core.Session{ID: "s_3", Task: ""}, Liveness: core.LivenessMissing,
				Status: core.Status{Lifecycle: core.LifecycleExited, Activity: core.ActivityUnknown, LastMessage: "  Done —\n created  file  "}},
		}},
	}}
	var buf bytes.Buffer
	if err := RenderSnapshot(&buf, snap, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"WORKSPACE", "BRANCH", "empty", "(no sessions)", "● working", "! awaiting-approval (exec)", "× exited", "tool_started 5s ago", "approval_requested 3m ago", "dmux/feature-x/auth", "↳ s_1", "auth implementation"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "devin store unavailable") || strings.Contains(out, "olive-turkey") {
		t.Fatal("evidence and devin id must only render in verbose mode")
	}
	buf.Reset()
	_ = RenderSnapshot(&buf, snap, true)
	if !strings.Contains(buf.String(), "· devin store unavailable") || !strings.Contains(buf.String(), "» Done — created file") || !strings.Contains(buf.String(), "devin olive-turkey") {
		t.Fatalf("verbose output missing evidence/last message:\n%s", buf.String())
	}
	buf.Reset()
	_ = RenderSnapshot(&buf, &core.Snapshot{}, false)
	if !strings.Contains(buf.String(), "No workspaces") {
		t.Fatalf("empty: %s", buf.String())
	}
}

func TestBadgeAndAgo(t *testing.T) {
	for label, glyph := range map[string]string{"working": "●", "awaiting-approval": "!", "idle": "○", "idle?": "◌", "starting": "◌", "exited": "×", "failed": "✗", "unknown": "?"} {
		if Badge(label) != glyph {
			t.Errorf("Badge(%s) = %s", label, Badge(label))
		}
	}
	for d, want := range map[time.Duration]string{-time.Second: "now", 30 * time.Second: "30s ago", 5 * time.Minute: "5m ago", 3 * time.Hour: "3h ago", 49 * time.Hour: "2d ago"} {
		if ago(d) != want {
			t.Errorf("ago(%v) = %s, want %s", d, ago(d), want)
		}
	}
}

func TestBranchSummary(t *testing.T) {
	same := core.Session{Repos: []core.WorkspaceRepo{{Name: "api", Branch: "b"}, {Name: "web", Branch: "b"}}}
	diff := core.Session{Repos: []core.WorkspaceRepo{{Name: "api", Branch: "b1"}, {Name: "web", Branch: "b2"}}}
	joiner := core.Session{SharedWith: "s_9"}
	for got, want := range map[string]string{BranchSummary(same): "b", BranchSummary(diff): "api:b1 web:b2", BranchSummary(joiner): "↳ s_9", BranchSummary(core.Session{}): ""} {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
