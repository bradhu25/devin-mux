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
	m     *WorkspaceManager
	git   *fakeGit
	store *fakeStore
	tm    *listingTmux
	ws    Workspace
}

func newDoctorFixture(t *testing.T) *doctorFixture {
	t.Helper()
	git := newFakeGit("/src/api", "/src/web")
	m, store := newManager(t, git)
	ws, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "w", Repos: []RepoSpec{{Path: "/src/api"}, {Path: "/src/web"}}})
	if err != nil {
		t.Fatal(err)
	}
	tm := &listingTmux{}
	d := &Doctor{Store: store, Git: git, Tmux: tm, Devin: &fakeDevin{}, Events: &fakeEvents{}, WorkspacesRoot: m.WorkspacesRoot,
		HooksInstalled: func() (bool, error) { return true, nil }}
	return &doctorFixture{d: d, m: m, git: git, store: store, tm: tm, ws: *ws}
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

func TestDoctor_StuckWorkspace_CleanFixFinishesRollback(t *testing.T) {
	f := newDoctorFixture(t)
	f.store.state.Workspaces[0].Status = WorkspaceFailed
	f.store.state.Sessions = append(f.store.state.Sessions, Session{ID: "s_1", WorkspaceID: f.ws.ID})
	fs := findings(t, f.d)
	saga := byCheck(fs, "saga")
	if len(saga) != 1 || saga[0].Fix == nil || saga[0].Severity != SevError {
		t.Fatalf("%s", Summarize(fs))
	}
	if err := saga[0].Fix(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.store.state.Workspaces) != 0 || len(f.store.state.Sessions) != 0 {
		t.Fatalf("fix should drop records: %+v", f.store.state)
	}
	if f.git.repos["/src/api"]["dmux/w"] || dirExists(f.ws.Root) {
		t.Fatal("fix should remove worktrees, dmux branches, and the root")
	}
	if len(findings(t, f.d)) != 0 {
		t.Fatal("after fix, doctor should be quiet")
	}
}

func TestDoctor_StuckWorkspace_DirtyHasNoAutoFix(t *testing.T) {
	f := newDoctorFixture(t)
	f.store.state.Workspaces[0].Status = WorkspaceDeleting
	f.git.dirty[filepath.Join(f.ws.Root, "api")] = []string{" M x"}
	fs := findings(t, f.d)
	saga := byCheck(fs, "saga")
	if len(saga) != 1 || saga[0].Fix != nil || !strings.Contains(saga[0].Advice, "--discard") {
		t.Fatalf("dirty stuck workspace must not auto-fix: %s", Summarize(fs))
	}
}

func TestDoctor_StuckWorkspace_LockedFixRefuses(t *testing.T) {
	f := newDoctorFixture(t)
	f.store.state.Workspaces[0].Status = WorkspaceCreating
	f.git.locked[filepath.Join(f.ws.Root, "web")] = true
	fs := findings(t, f.d)
	saga := byCheck(fs, "saga")
	// Locked counts as not clean -> no auto fix offered.
	if len(saga) != 1 || saga[0].Fix != nil {
		t.Fatalf("%s", Summarize(fs))
	}
}

func TestDoctor_UnregisteredWorktreeAndMissingRoot(t *testing.T) {
	f := newDoctorFixture(t)
	delete(f.git.worktree, filepath.Join(f.ws.Root, "web"))
	fs := findings(t, f.d)
	wt := byCheck(fs, "worktree")
	if len(wt) != 1 || wt[0].Severity != SevError || !strings.Contains(wt[0].Advice, "worktree add") {
		t.Fatalf("%s", Summarize(fs))
	}
	_ = os.RemoveAll(f.ws.Root)
	fs = findings(t, f.d)
	if len(byCheck(fs, "workspace")) != 1 {
		t.Fatalf("missing root not reported: %s", Summarize(fs))
	}
}

func TestDoctor_OrphanDirNeverAutoDeleted(t *testing.T) {
	f := newDoctorFixture(t)
	orphan := filepath.Join(f.m.WorkspacesRoot, "leftover")
	_ = os.MkdirAll(orphan, 0o755)
	fs := findings(t, f.d)
	o := byCheck(fs, "orphan")
	if len(o) != 1 || o[0].Fix != nil || o[0].Subject != orphan {
		t.Fatalf("%s", Summarize(fs))
	}
}

func TestDoctor_Sessions(t *testing.T) {
	f := newDoctorFixture(t)
	f.store.state.Sessions = []Session{
		{ID: "s_held", WorkspaceID: f.ws.ID, Tmux: TmuxTarget{WindowID: "@1"}},
		{ID: "s_gone", WorkspaceID: f.ws.ID, DevinSessionID: "olive-turkey", Tmux: TmuxTarget{WindowID: "@2"}},
		{ID: "s_noid", WorkspaceID: f.ws.ID, Tmux: TmuxTarget{WindowID: "@3"}},
		{ID: "s_orphan", WorkspaceID: "ws_missing"},
		{ID: "s_live", WorkspaceID: f.ws.ID, Tmux: TmuxTarget{WindowID: "@5"}},
		{ID: "s_heldalive", WorkspaceID: f.ws.ID, Tmux: TmuxTarget{WindowID: "@6"}},
	}
	f.tm.windows = []TmuxWindow{
		{WindowID: "@1", DmuxSession: "s_held", PaneDead: true},
		{WindowID: "@5", DmuxSession: "s_live"},
		{WindowID: "@6", DmuxSession: "s_heldalive"}, // wrapper holding the pane after Devin exited
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
	if len(sess) != 5 {
		t.Fatalf("want 5 session findings (live is fine): %s", Summarize(fs))
	}
	if sess["s_heldalive"].Fix == nil || sess["s_heldalive"].Severity != SevInfo {
		t.Fatalf("held-open pane after exit should offer a close fix: %+v", sess["s_heldalive"])
	}
	if sess["s_held"].Fix == nil || sess["s_held"].Severity != SevInfo {
		t.Fatalf("held pane should offer a fix: %+v", sess["s_held"])
	}
	if err := sess["s_held"].Fix(context.Background()); err != nil || len(f.tm.killed) != 1 || f.tm.killed[0] != "@1" {
		t.Fatalf("fix should close the dead window: %v %v", err, f.tm.killed)
	}
	if !strings.Contains(sess["s_gone"].Advice, "dmux resume s_gone") || sess["s_gone"].Fix != nil {
		t.Fatalf("resumable session should advise resume, never auto-drop: %+v", sess["s_gone"])
	}
	if sess["s_noid"].Severity != SevWarn || !strings.Contains(sess["s_noid"].Advice, "dmux init") {
		t.Fatalf("%+v", sess["s_noid"])
	}
	if sess["s_orphan"].Fix == nil {
		t.Fatal("orphan session record should be droppable")
	}
	if err := sess["s_orphan"].Fix(context.Background()); err != nil || f.store.state.Session("s_orphan") != nil {
		t.Fatalf("orphan drop failed: %v", err)
	}
	// Records for held/gone sessions are still present (needed for resume).
	if f.store.state.Session("s_held") == nil || f.store.state.Session("s_gone") == nil {
		t.Fatal("doctor must not drop resumable session records")
	}
}
