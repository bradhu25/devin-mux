package core

import (
	"encoding/json"
	"testing"
	"time"
)

const v1Doc = `{
  "version": 1,
  "workspaces": [
    {"id":"ws_1","name":"chi","root":"/r/chi","status":"ready","createdAt":"2026-09-28T10:00:00Z",
     "repos":[{"name":"chi","sourcePath":"/src/chi","worktreePath":"/r/chi/chi","branch":"dmux/chi","baseRef":"3d1777a","createdBranch":true}]},
    {"id":"ws_2","name":"empty","root":"/r/empty","status":"ready","createdAt":"2026-09-28T11:00:00Z",
     "repos":[{"name":"api","sourcePath":"/src/api","worktreePath":"/r/empty/api","branch":"dmux/empty","baseRef":"abc","createdBranch":true}]},
    {"id":"ws_3","name":"stuck","root":"/r/stuck","status":"failed","createdAt":"2026-09-28T12:00:00Z","repos":[]}
  ],
  "sessions": [
    {"id":"s_b","workspaceId":"ws_1","task":"second","tmux":{"sessionName":"dmux-chi","sessionId":"$1","windowId":"@2"},"createdAt":"2026-09-28T10:20:00Z"},
    {"id":"s_a","workspaceId":"ws_1","task":"first","tmux":{"sessionName":"dmux-chi","sessionId":"$1","windowId":"@1"},"devinSessionId":"olive-turkey","createdAt":"2026-09-28T10:10:00Z"}
  ],
  "knownRepos": ["/src/chi"]
}`

func TestMigrateV1(t *testing.T) {
	st, err := MigrateV1([]byte(v1Doc))
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != StateVersion || len(st.Workspaces) != 3 {
		t.Fatalf("%+v", st)
	}
	chi := st.WorkspaceByName("chi")
	if len(chi.Repos) != 1 || chi.Repos[0].SourcePath != "/src/chi" || chi.Repos[0].BaseRef != "HEAD" || chi.Root != "/r/chi" {
		t.Fatalf("workspace repos should become refs: %+v", chi.Repos)
	}
	// Oldest session owns the old worktrees; the other joins it.
	a, b := st.Session("s_a"), st.Session("s_b")
	if a.Root != "/r/chi" || len(a.Repos) != 1 || a.Repos[0].Branch != "dmux/chi" || !a.OwnsWorktrees() || a.Status != WorkspaceReady {
		t.Fatalf("owner: %+v", a)
	}
	if b.SharedWith != "s_a" || b.Root != "/r/chi" || len(b.Repos) != 1 || b.OwnsWorktrees() {
		t.Fatalf("joiner: %+v", b)
	}
	if a.DevinSessionID != "olive-turkey" || b.Tmux.WindowID != "@2" {
		t.Fatal("session fields must survive")
	}
	// Workspace with worktrees but no sessions gets a placeholder owner.
	empties := st.SessionsIn("ws_2")
	if len(empties) != 1 || empties[0].Root != "/r/empty" || len(empties[0].Repos) != 1 || empties[0].Task == "" {
		t.Fatalf("placeholder: %+v", empties)
	}
	// Failed workspace with nothing: ready, no sessions.
	if stuck := st.WorkspaceByName("stuck"); stuck.Status != WorkspaceReady || len(st.SessionsIn("ws_3")) != 0 {
		t.Fatalf("%+v", stuck)
	}
	if len(st.KnownRepos) != 1 {
		t.Fatal("knownRepos must survive")
	}
	// Round-trips as v2.
	out, _ := json.Marshal(st)
	var again State
	if err := json.Unmarshal(out, &again); err != nil || again.Version != 2 || len(again.Sessions) != 3 {
		t.Fatalf("%v %+v", err, again)
	}
	_ = time.Now
}
