package core

import (
	"encoding/json"
	"regexp"
	"testing"
)

func TestNewIDs_FormatAndUniqueness(t *testing.T) {
	re := regexp.MustCompile(`^(ws|s|e)_[0-9a-f]{6}$`)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		for _, id := range []string{NewWorkspaceID(), NewSessionID(), NewEventID()} {
			if !re.MatchString(id) {
				t.Fatalf("bad id format: %q", id)
			}
			if seen[id] {
				t.Fatalf("duplicate id: %q", id)
			}
			seen[id] = true
		}
	}
}

func TestValidateName(t *testing.T) {
	ok := []string{"feature-x", "api", "web_2", "v1.2", "A", "payment-fix-2026"}
	bad := []string{"", ".", "..", "-x", ".hidden", "has space", "a/b", "a\\b", "semi;colon", "$(x)", "tab\tname", string(make([]byte, 65))}
	for _, n := range ok {
		if err := ValidateName(n); err != nil {
			t.Errorf("expected %q valid: %v", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("expected %q invalid", n)
		}
	}
}

func TestState_Lookups(t *testing.T) {
	s := State{
		Workspaces: []Workspace{{ID: "ws_1", Name: "feature-x"}, {ID: "ws_2", Name: "payment-fix"}},
		Sessions:   []Session{{ID: "s_1", WorkspaceID: "ws_1"}, {ID: "s_2", WorkspaceID: "ws_1"}, {ID: "s_3", WorkspaceID: "ws_2"}},
	}
	if w := s.Workspace("ws_2"); w == nil || w.Name != "payment-fix" {
		t.Fatalf("Workspace lookup failed: %+v", w)
	}
	if w := s.WorkspaceByName("feature-x"); w == nil || w.ID != "ws_1" {
		t.Fatalf("WorkspaceByName lookup failed: %+v", w)
	}
	if s.Workspace("nope") != nil || s.WorkspaceByName("nope") != nil || s.Session("nope") != nil {
		t.Fatal("missing lookups must return nil")
	}
	if got := len(s.SessionsIn("ws_1")); got != 2 {
		t.Fatalf("SessionsIn(ws_1) = %d, want 2", got)
	}
	// Lookups return pointers into the slice so callers can mutate in place.
	s.Workspace("ws_1").Status = WorkspaceReady
	if s.Workspaces[0].Status != WorkspaceReady {
		t.Fatal("Workspace() must return a pointer into State")
	}
}

func TestState_JSONRoundTrip(t *testing.T) {
	in := State{Version: StateVersion, Workspaces: []Workspace{{
		ID: "ws_1", Name: "feature-x", Root: "/r/feature-x", Status: WorkspaceReady,
		Repos: []WorkspaceRepo{{Name: "api", SourcePath: "/src/api", WorktreePath: "/r/feature-x/api", Branch: "dmux/feature-x", BaseRef: "main", CreatedBranch: true}},
	}}, Sessions: []Session{{ID: "s_1", WorkspaceID: "ws_1", Task: "fix auth", Tmux: TmuxTarget{SessionName: "dmux-feature-x", SessionID: "$3", WindowID: "@12"}}}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Workspaces[0].Repos[0].CreatedBranch != true || out.Sessions[0].Tmux.WindowID != "@12" || out.Workspaces[0].Status != WorkspaceReady {
		t.Fatalf("round trip lost data: %+v", out)
	}
	// Derived fields must not be part of the persisted model.
	for _, forbidden := range []string{`"lifecycle"`, `"activity"`} {
		if regexp.MustCompile(forbidden).Match(b) {
			t.Fatalf("persisted state must not contain derived field %s", forbidden)
		}
	}
}
