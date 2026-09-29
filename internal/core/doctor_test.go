package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type doctorFixture struct {
	d     *Doctor
	sm    *SessionManager
	wm    *WorkspaceManager
	git   *fakeGit
	store *fakeStore
	tm    *listingTmux
	ws    Workspace
	a     Session // one ready session owning api+web worktrees
}

func newDoctorFixture(t *testing.T) *doctorFixture {
	t.Helper()
	m, store, git, _, ws := spawnFixture(t)
	tm := &listingTmux{}
	tm.nextWindow = 10
	m.Tmux = tm
	a, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	wm := &WorkspaceManager{Git: git, Store: store, WorkspacesRoot: filepath.Dir(ws.Root)}
	d := &Doctor{Store: store, Git: git, Tmux: tm, Devin: &fakeDevin{}, Events: &fakeEvents{}, WorkspacesRoot: wm.WorkspacesRoot,
		HooksInstalled: func() (bool, error) { return true, nil }}
	// The spawned session's window is "live" for the sessions check.
	tm.windows = []TmuxWindow{{WindowID: a.Session.Tmux.WindowID, DmuxSession: a.Session.ID, PanePID: 1}}
	return &doctorFixture{d: d, sm: m, wm: wm, git: git, store: store, tm: tm, ws: ws, a: a.Session}
}

func findings(t *testing.T, d *Doctor) []Finding {
	t.Helper()
	fs, err := d.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func byCheck(fs []Finding, check string) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

func TestDoctor_HealthyIsQuiet(t *testing.T) {
	f := newDoctorFixture(t)
	if fs := findings(t, f.d); len(fs) != 0 {
		t.Fatalf("healthy state should have no findings: %v", Summarize(fs))
	}
	if Summarize(nil) != "No problems found." {
		t.Fatal("summary")
	}
}

func TestDoctor_ToolsAndHooks(t *testing.T) {
	f := newDoctorFixture(t)
	f.tm.available = errors.New("no tmux")
	f.d.Devin = &fakeDevin{available: errors.New("no devin")}
	f.d.HooksInstalled = func() (bool, error) { return false, nil }
	fs := findings(t, f.d)
	if len(byCheck(fs, "tools")) != 2 || len(byCheck(fs, "hooks")) != 1 {
		t.Fatalf("%s", Summarize(fs))
	}
	if fs[0].Severity != SevError || !strings.Contains(byCheck(fs, "hooks")[0].Advice, "dmux init") {
		t.Fatalf("ordering/advice: %s", Summarize(fs))
	}
}

func TestDoctor_StuckSession_CleanFixFinishesRollback(t *testing.T) {
	f := newDoctorFixture(t)
	f.store.state.Session(f.a.ID).Status = WorkspaceFailed
	fs := findings(t, f.d)
	saga := byCheck(fs, "saga")
	if len(saga) != 1 || saga[0].Fix == nil || saga[0].Severity != SevError || saga[0].Subject != f.a.ID {
		t.Fatalf("%s", Summarize(fs))
	}
	if err := saga[0].Fix(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.store.state.Session(f.a.ID) != nil || dirExists(f.a.Root) || f.git.repos["/src/api"][f.a.Repos[0].Branch] {
		t.Fatal("fix should remove worktrees, dmux branches, the dir, and the record")
	}
	if f.store.state.Workspace(f.ws.ID) == nil {
		t.Fatal("workspace record stays")
	}
	f.tm.windows = nil
	if fs := findings(t, f.d); len(fs) != 0 {
		t.Fatalf("after fix, doctor should be quiet: %s", Summarize(fs))
	}
}

func TestDoctor_StuckSession_DirtyOrSharedHasNoAutoFix(t *testing.T) {
	f := newDoctorFixture(t)
	f.store.state.Session(f.a.ID).Status = WorkspaceDeleting
	f.git.dirty[f.a.Repos[0].WorktreePath] = []string{" M x"}
	fs := findings(t, f.d)
	saga := byCheck(fs, "saga")
	if len(saga) != 1 || saga[0].Fix != nil || !strings.Contains(saga[0].Advice, "--discard") {
		t.Fatalf("dirty stuck session must not auto-fix: %s", Summarize(fs))
	}
	// Shared: also no auto-fix even when clean.
	delete(f.git.dirty, f.a.Repos[0].WorktreePath)
	f.store.state.Session(f.a.ID).Status = WorkspaceReady
	if _, err := f.sm.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "review", In: f.a.ID}); err != nil {
		t.Fatal(err)
	}
	f.store.state.Session(f.a.ID).Status = WorkspaceFailed
	fs = findings(t, f.d)
	if saga = byCheck(fs, "saga"); len(saga) != 1 || saga[0].Fix != nil {
		t.Fatalf("shared stuck session must not auto-fix: %s", Summarize(fs))
	}
}

func TestDoctor_StuckWorkspaceDeleting(t *testing.T) {
	f := newDoctorFixture(t)
	f.store.state.Workspace(f.ws.ID).Status = WorkspaceDeleting
	fs := findings(t, f.d)
	saga := byCheck(fs, "saga")
	if len(saga) != 1 || saga[0].Fix != nil || !strings.Contains(saga[0].Advice, "workspace rm feature-x") {
		t.Fatalf("with sessions remaining, advise rm: %s", Summarize(fs))
	}
	// Sessions gone, dir empty: fixable.
	f.store.state.Sessions = nil
	_ = os.RemoveAll(f.a.Root)
	fs = findings(t, f.d)
	saga = byCheck(fs, "saga")
	if len(saga) != 1 || saga[0].Fix == nil {
		t.Fatalf("%s", Summarize(fs))
	}
	if err := saga[0].Fix(context.Background()); err != nil || len(f.store.state.Workspaces) != 0 || dirExists(f.ws.Root) {
		t.Fatalf("%v", err)
	}
}

func TestDoctor_UnregisteredWorktreeAndMissingRoot(t *testing.T) {
	f := newDoctorFixture(t)
	delete(f.git.worktree, f.a.Repos[1].WorktreePath)
	fs := findings(t, f.d)
	wt := byCheck(fs, "worktree")
	if len(wt) != 1 || wt[0].Severity != SevError || !strings.Contains(wt[0].Advice, "worktree add") || !strings.Contains(wt[0].Advice, "dmux rm "+f.a.ID) {
		t.Fatalf("%s", Summarize(fs))
	}
	_ = os.RemoveAll(f.ws.Root)
	fs = findings(t, f.d)
	if len(byCheck(fs, "workspace")) != 1 {
		t.Fatalf("missing root not reported: %s", Summarize(fs))
	}
}

func TestDoctor_OrphanDirsNeverAutoDeleted(t *testing.T) {
	f := newDoctorFixture(t)
	orphanWS := filepath.Join(f.wm.WorkspacesRoot, "leftover")
	orphanSess := filepath.Join(f.ws.Root, "s_gone")
	_ = os.MkdirAll(orphanWS, 0o755)
	_ = os.MkdirAll(orphanSess, 0o755)
	fs := findings(t, f.d)
	o := byCheck(fs, "orphan")
	if len(o) != 2 || o[0].Fix != nil || o[1].Fix != nil {
		t.Fatalf("%s", Summarize(fs))
	}
	// A v1-migrated session keeps worktrees directly under the workspace
	// root; those dirs must not read as orphans.
	legacy := Session{ID: "s_old", WorkspaceID: f.ws.ID, Root: f.ws.Root, Status: WorkspaceReady,
		Repos: []WorkspaceRepo{{Name: "chi", SourcePath: "/src/api", WorktreePath: filepath.Join(f.ws.Root, "chi"), Branch: "dmux/chi", CreatedBranch: true}}}
	_ = os.MkdirAll(legacy.Repos[0].WorktreePath, 0o755)
	f.git.worktree[legacy.Repos[0].WorktreePath] = "/src/api"
	f.git.checked[legacy.Repos[0].WorktreePath] = "dmux/chi"
	f.git.repos["/src/api"]["dmux/chi"] = true
	f.store.state.Sessions = append(f.store.state.Sessions, legacy)
	if o = byCheck(findings(t, f.d), "orphan"); len(o) != 2 {
		t.Fatalf("legacy worktree dir must not be an orphan: %s", Summarize(findings(t, f.d)))
	}
}

func TestDoctor_OrphanBranches(t *testing.T) {
	f := newDoctorFixture(t)
	f.git.repos["/src/api"]["dmux/old-feature"] = true
	fs := findings(t, f.d)
	br := byCheck(fs, "branch")
	if len(br) != 1 || br[0].Fix != nil || !strings.Contains(br[0].Message, "dmux/old-feature") || strings.Contains(br[0].Message, f.a.Repos[0].Branch) {
		t.Fatalf("%s", Summarize(fs))
	}
	if !strings.Contains(br[0].Advice, "git -C /src/api branch -d dmux/old-feature") {
		t.Fatalf("advice should carry the exact safe command: %q", br[0].Advice)
	}
	// Remove the session (branches kept, the default): its branches are
	// now orphans in both repos.
	f.tm.windows = nil
	if _, err := f.wm.RemoveSession(context.Background(), f.sm, f.a.ID, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	br = byCheck(findings(t, f.d), "branch")
	if len(br) != 2 || !strings.Contains(br[0].Message+br[1].Message, f.a.Repos[0].Branch) {
		t.Fatalf("%s", Summarize(findings(t, f.d)))
	}
}

func TestDoctor_Sessions(t *testing.T) {
	f := newDoctorFixture(t)
	mk := func(id, win string) Session {
		return Session{ID: id, WorkspaceID: f.ws.ID, Root: filepath.Join(f.ws.Root, id), Status: WorkspaceReady, Tmux: TmuxTarget{WindowID: win}}
	}
	held, gone, noid, orphan, live, heldAlive := mk("s_held", "@1"), mk("s_gone", "@2"), mk("s_noid", "@3"), mk("s_orphan", ""), mk("s_live", "@5"), mk("s_heldalive", "@6")
	gone.DevinSessionID = "olive-turkey"
	orphan.WorkspaceID = "ws_missing"
	joinerOfGone := Session{ID: "s_joiner", WorkspaceID: f.ws.ID, SharedWith: "s_removed", Status: WorkspaceReady, Root: f.ws.Root}
	f.store.state.Sessions = []Session{held, gone, noid, orphan, live, heldAlive, joinerOfGone}
	f.tm.windows = []TmuxWindow{
		{WindowID: "@1", DmuxSession: "s_held", PaneDead: true},
		{WindowID: "@5", DmuxSession: "s_live"},
		{WindowID: "@6", DmuxSession: "s_heldalive"},
	}
	f.d.Events = &fakeEvents{byID: map[string][]SessionEvent{
		"s_heldalive": {ev(0, SessionStarted, nil), ev(1, ProcessExited, map[string]any{"exitCode": 0})},
		"s_live":      {ev(0, SessionStarted, nil), ev(1, TurnCompleted, nil)},
	}}
	fs := findings(t, f.d)
	sess := map[string]Finding{}
	for _, x := range byCheck(fs, "session") {
		sess[x.Subject] = x
	}
	if len(sess) != 6 {
		t.Fatalf("want 6 session findings (live is fine): %s", Summarize(fs))
	}
	if sess["s_heldalive"].Fix == nil || sess["s_held"].Fix == nil {
		t.Fatalf("held panes should offer a close fix")
	}
	if err := sess["s_held"].Fix(context.Background()); err != nil || len(f.tm.killed) != 1 || f.tm.killed[0] != "@1" {
		t.Fatalf("fix should close the dead window: %v %v", err, f.tm.killed)
	}
	if !strings.Contains(sess["s_gone"].Advice, "dmux resume s_gone") || sess["s_gone"].Fix != nil {
		t.Fatalf("resumable session should advise resume, never auto-drop: %+v", sess["s_gone"])
	}
	if sess["s_noid"].Severity != SevWarn || !strings.Contains(sess["s_noid"].Advice, "dmux rm s_noid") {
		t.Fatalf("%+v", sess["s_noid"])
	}
	if sess["s_orphan"].Fix == nil {
		t.Fatal("orphan session record should be droppable")
	}
	if !strings.Contains(sess["s_joiner"].Message, "no longer exists") {
		t.Fatalf("joiner of a removed owner: %+v", sess["s_joiner"])
	}
	if err := sess["s_orphan"].Fix(context.Background()); err != nil || f.store.state.Session("s_orphan") != nil {
		t.Fatalf("orphan drop failed: %v", err)
	}
	if f.store.state.Session("s_held") == nil || f.store.state.Session("s_gone") == nil {
		t.Fatal("doctor must not drop resumable session records")
	}
}
