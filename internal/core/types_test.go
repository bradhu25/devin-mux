package core

import (
	"encoding/json"
	"regexp"
	"testing"
)

func TestNewIDs_Format(t *testing.T) {
	short := regexp.MustCompile(`^(ws|s)_[0-9a-f]{6}$`)
	long := regexp.MustCompile(`^e_[0-9a-f]{16}$`)
	for i := 0; i < 100; i++ {
		for _, id := range []string{NewWorkspaceID(), NewSessionID()} {
			if !short.MatchString(id) {
				t.Fatalf("bad short id: %q", id)
			}
		}
		if id := NewEventID(); !long.MatchString(id) {
			t.Fatalf("bad event id: %q", id)
		}
	}
}

// Event ids are generated per hook event, so they must not collide over a
// realistic session lifetime. 100k draws from a 2^64 space: collision
// probability ~3e-10, so a duplicate here means the generator is broken.
func TestNewEventID_NoCollisions(t *testing.T) {
	seen := make(map[string]bool, 100_000)
	for i := 0; i < 100_000; i++ {
		id := NewEventID()
		if seen[id] {
			t.Fatalf("duplicate event id: %q", id)
		}
		seen[id] = true
	}
}

func TestNewUniqueID_RetriesOnCollision(t *testing.T) {
	seq := []string{"s_taken", "s_taken", "s_free"}
	gen := func() string { id := seq[0]; seq = seq[1:]; return id }
	got := NewUniqueID(gen, func(id string) bool { return id == "s_taken" })
	if got != "s_free" {
		t.Fatalf("got %q", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic when every id is taken")
		}
	}()
	NewUniqueID(func() string { return "x" }, func(string) bool { return true })
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
		Repos: []RepoRef{{Name: "api", SourcePath: "/src/api", BaseRef: "main"}},
	}}, Sessions: []Session{{ID: "s_1", WorkspaceID: "ws_1", Task: "fix auth", Root: "/r/feature-x/s_1", Status: WorkspaceReady,
		Repos: []WorkspaceRepo{{Name: "api", SourcePath: "/src/api", WorktreePath: "/r/feature-x/s_1/api", Branch: "dmux/feature-x/fix-auth", BaseRef: "abc", CreatedBranch: true}},
		Tmux:  TmuxTarget{SessionName: "dmux-feature-x", SessionID: "$3", WindowID: "@12"}}}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Sessions[0].Repos[0].CreatedBranch != true || out.Sessions[0].Tmux.WindowID != "@12" || out.Workspaces[0].Repos[0].BaseRef != "main" || out.Sessions[0].Root != "/r/feature-x/s_1" {
		t.Fatalf("round trip lost data: %+v", out)
	}
	// Derived fields must not be part of the persisted model.
	for _, forbidden := range []string{`"lifecycle"`, `"activity"`} {
		if regexp.MustCompile(forbidden).Match(b) {
			t.Fatalf("persisted state must not contain derived field %s", forbidden)
		}
	}
}
