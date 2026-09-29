package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTmux is an in-memory core.Tmux.
type fakeTmux struct {
	available  error
	spawnErr   error
	spawned    []SpawnWindowOpts
	killed     []string
	nextWindow int
	inside     bool
	switched   []TmuxTarget
}

func (f *fakeTmux) Available(context.Context) error { return f.available }
func (f *fakeTmux) SpawnWindow(_ context.Context, o SpawnWindowOpts) (TmuxTarget, error) {
	if f.spawnErr != nil {
		return TmuxTarget{}, f.spawnErr
	}
	f.spawned = append(f.spawned, o)
	f.nextWindow++
	return TmuxTarget{SessionName: o.SessionName, SessionID: "$1", WindowID: fmt.Sprintf("@%d", f.nextWindow)}, nil
}
func (f *fakeTmux) ListWindows(context.Context) ([]TmuxWindow, error) { return nil, nil }
func (f *fakeTmux) KillWindow(_ context.Context, id string) error {
	f.killed = append(f.killed, id)
	return nil
}
func (f *fakeTmux) RenameSession(context.Context, string, string) error { return nil }
func (f *fakeTmux) RenameWindow(context.Context, string, string) error  { return nil }
func (f *fakeTmux) InsideTmux() bool                                    { return f.inside }
func (f *fakeTmux) SwitchClient(_ context.Context, t TmuxTarget) error {
	f.switched = append(f.switched, t)
	return nil
}
func (f *fakeTmux) Attach(TmuxTarget) error { return errors.New("fake attach") }

type fakeDevin struct{ available error }

func (f *fakeDevin) Available(context.Context) error { return f.available }
func (f *fakeDevin) ToolCallOutcomes(context.Context, string, []string) ([]ToolCallOutcome, error) {
	return nil, nil
}
func (f *fakeDevin) LaunchArgs(s LaunchSpec) []string {
	args := []string{"devin"}
	if s.ResumeID != "" {
		args = append(args, "-r", s.ResumeID)
	}
	if s.Prompt != "" {
		args = append(args, "--", s.Prompt)
	}
	return args
}

func newSessionManager(store *fakeStore, tm *fakeTmux) *SessionManager {
	return &SessionManager{Tmux: tm, Devin: &fakeDevin{}, Store: store, DmuxBin: "/usr/local/bin/dmux",
		Now: func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }}
}

// spawnFixture: a ready workspace over two repos, with a real temp dir as
// the workspaces root so worktree dirs are actually created by the fake.
func spawnFixture(t *testing.T) (*SessionManager, *fakeStore, *fakeGit, *fakeTmux, Workspace) {
	t.Helper()
	git := newFakeGit("/src/api", "/src/web")
	wm, store := newManager(t, git)
	ws, err := wm.Create(context.Background(), CreateWorkspaceInput{Name: "feature-x", Repos: []RepoSpec{{Path: "/src/api"}, {Path: "/src/web"}}})
	if err != nil {
		t.Fatal(err)
	}
	tm := &fakeTmux{}
	m := newSessionManager(store, tm)
	m.Git = git
	return m, store, git, tm, *ws
}

func TestSpawn_CreatesWorktreesPerRepoOnTaskBranch(t *testing.T) {
	m, store, git, tm, ws := spawnFixture(t)
	res, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "Fix the auth bug in api/", PermissionMode: "accept-edits"})
	if err != nil {
		t.Fatal(err)
	}
	s := res.Session
	if s.Status != WorkspaceReady || s.Root != filepath.Join(ws.Root, s.ID) || len(s.Repos) != 2 || !s.OwnsWorktrees() {
		t.Fatalf("session: %+v", s)
	}
	for i, name := range []string{"api", "web"} {
		r := s.Repos[i]
		if r.Name != name || r.WorktreePath != filepath.Join(s.Root, name) || r.Branch != "dmux/feature-x/fix-the-auth-bug-in-api" || !r.CreatedBranch || len(r.BaseRef) != 40 {
			t.Fatalf("repo %d: %+v", i, r)
		}
		if !dirExists(r.WorktreePath) || git.checked[r.WorktreePath] != r.Branch {
			t.Fatalf("worktree %s not created on its branch", r.WorktreePath)
		}
	}
	if !fileExists(filepath.Join(s.Root, "AGENTS.md")) {
		t.Fatal("session AGENTS.md map missing")
	}
	// tmux window: cwd = session root, tagged, wrapper argv.
	o := tm.spawned[0]
	if o.Cwd != s.Root || o.SessionID != s.ID || o.WorkspaceID != ws.ID || o.SessionName != "dmux-feature-x" || o.Env["DMUX_SESSION_ID"] != s.ID {
		t.Fatalf("spawn opts: %+v", o)
	}
	want := "/usr/local/bin/dmux run --session " + s.ID + " --dir " + s.Root + " -- devin -- Fix the auth bug in api/"
	if strings.Join(o.Argv, " ") != want {
		t.Fatalf("argv:\n got %q\nwant %q", strings.Join(o.Argv, " "), want)
	}
	// Persisted.
	if got := store.state.Session(s.ID); got == nil || got.Tmux.WindowID == "" || got.Status != WorkspaceReady || len(got.Repos) != 2 {
		t.Fatalf("record: %+v", got)
	}
}

// Two sessions in one workspace get distinct dirs and branches: that is
// the point of the model.
func TestSpawn_TwoSessionsAreIsolated(t *testing.T) {
	m, _, git, _, _ := spawnFixture(t)
	a, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "tests"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Session.Root == b.Session.Root || a.Session.Repos[0].Branch == b.Session.Repos[0].Branch || a.Session.Repos[0].WorktreePath == b.Session.Repos[0].WorktreePath {
		t.Fatalf("sessions must not share dirs or branches:\n%+v\n%+v", a.Session, b.Session)
	}
	if len(git.repos["/src/api"]) != 3 { // main + two dmux branches
		t.Fatalf("branches: %v", git.repos["/src/api"])
	}
}

// Same task twice: the second branch gets an id suffix instead of failing.
func TestSpawn_BranchCollisionGetsSuffix(t *testing.T) {
	m, _, _, _, _ := spawnFixture(t)
	a, _ := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "auth"})
	b, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Session.Repos[0].Branch != "dmux/feature-x/auth" || b.Session.Repos[0].Branch != "dmux/feature-x/auth-"+strings.TrimPrefix(b.Session.ID, "s_") {
		t.Fatalf("%q %q", a.Session.Repos[0].Branch, b.Session.Repos[0].Branch)
	}
}

func TestSpawn_ExplicitBranch(t *testing.T) {
	m, _, git, _, _ := spawnFixture(t)
	git.repos["/src/api"]["hotfix"] = true
	git.repos["/src/web"]["hotfix"] = true
	res, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "t", Branch: "hotfix"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Session.Repos {
		if r.Branch != "hotfix" || r.CreatedBranch {
			t.Fatalf("existing branch must be checked out, not owned: %+v", r)
		}
	}
	// Checked out elsewhere: refused before side effects.
	m2, store2, git2, _, _ := spawnFixture(t)
	git2.repos["/src/api"]["main"] = true // main is checked out in the source repo
	_, err = m2.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "t", Branch: "main"})
	if err == nil || !strings.Contains(err.Error(), "already checked out") || len(store2.state.Sessions) != 0 || git2.hasCall("worktreeadd") {
		t.Fatalf("%v sessions=%d", err, len(store2.state.Sessions))
	}
}

// A v1-style branch dmux/<ws> makes dmux/<ws>/<slug> impossible in git.
// Caught at plan time with the fix, before any side effect.
func TestSpawn_V1BranchBlocksHierarchy(t *testing.T) {
	m, store, git, tm, _ := spawnFixture(t)
	git.repos["/src/api"]["dmux/feature-x"] = true
	_, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "auth"})
	if err == nil || !strings.Contains(err.Error(), "branch -m dmux/feature-x dmux/feature-x-v1") {
		t.Fatalf("want a clear fix, got %v", err)
	}
	if len(store.state.Sessions) != 0 || git.hasCall("worktreeadd") || len(tm.spawned) != 0 {
		t.Fatal("must fail before side effects")
	}
	// An explicit --branch sidesteps it.
	if _, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "auth", Branch: "auth-work"}); err != nil {
		t.Fatal(err)
	}
}

func TestSpawn_NoTaskUsesIDForBranch(t *testing.T) {
	m, _, _, tm, _ := spawnFixture(t)
	res, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Session.Repos[0].Branch != "dmux/feature-x/"+res.Session.ID || tm.spawned[0].WindowName != res.Session.ID {
		t.Fatalf("%+v", res.Session.Repos[0])
	}
}

func TestSpawn_SecondWorktreeFails_RollsBackFirst(t *testing.T) {
	m, store, git, tm, ws := spawnFixture(t)
	git.failOn["worktreeadd:"+filepath.Join(ws.Root, "s_", "web")] = nil // placeholder; real key computed below
	// Fail on any web worktree add.
	git.failOn = map[string]error{}
	origAdd := git.repos
	_ = origAdd
	m.Store = store
	// Inject failure via a wrapper: fake git fails on paths ending in /web.
	git.failPathSuffix = "/web"
	git.failPathErr = errors.New("disk error")
	_, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "auth"})
	var se *SpawnError
	if !errors.As(err, &se) || se.Step != "worktree add web" || se.Rollback != nil {
		t.Fatalf("want SpawnError at web with clean rollback, got %v", err)
	}
	if len(store.state.Sessions) != 0 {
		t.Fatalf("clean rollback should drop the record: %+v", store.state.Sessions)
	}
	if git.repos["/src/api"]["dmux/feature-x/auth"] || len(git.worktree) != 2 {
		t.Fatalf("api worktree/branch must be rolled back: %v %v", git.repos["/src/api"], git.worktree)
	}
	if len(tm.spawned) != 0 {
		t.Fatal("no window must be created")
	}
	if entries, _ := os.ReadDir(ws.Root); len(entries) != 0 {
		t.Fatalf("session dir must be removed: %v", entries)
	}
}

func TestSpawn_RollbackFailureLeavesFailedRecord(t *testing.T) {
	m, store, git, _, _ := spawnFixture(t)
	git.failPathSuffix, git.failPathErr = "/web", errors.New("disk error")
	git.failOn["worktreeremove:*"] = errors.New("cannot remove")
	_, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "auth"})
	var se *SpawnError
	if !errors.As(err, &se) || se.Rollback == nil || !strings.Contains(err.Error(), "dmux doctor") {
		t.Fatalf("want incomplete rollback with doctor hint, got %v", err)
	}
	if len(store.state.Sessions) != 1 || store.state.Sessions[0].Status != WorkspaceFailed || len(store.state.Sessions[0].Repos) != 1 {
		t.Fatalf("failed record for doctor: %+v", store.state.Sessions)
	}
}

func TestSpawn_TmuxFailureRollsBackWorktrees(t *testing.T) {
	m, store, git, tm, _ := spawnFixture(t)
	tm.spawnErr = errors.New("no tmux")
	_, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "auth"})
	if err == nil || !strings.Contains(err.Error(), "spawn tmux window") {
		t.Fatalf("%v", err)
	}
	if len(store.state.Sessions) != 0 || len(git.worktree) != 2 || git.repos["/src/api"]["dmux/feature-x/auth"] {
		t.Fatal("worktrees and branches must be rolled back when the window cannot be created")
	}
}

func TestSpawn_JoinSharesWorktrees(t *testing.T) {
	m, store, git, tm, _ := spawnFixture(t)
	owner, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	before := len(git.worktree)
	joined, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "review", In: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	j := joined.Session
	if j.SharedWith != owner.Session.ID || j.Root != owner.Session.Root || len(j.Repos) != 2 || j.OwnsWorktrees() || joined.JoinedSession.ID != owner.Session.ID {
		t.Fatalf("joiner: %+v", j)
	}
	if len(git.worktree) != before || tm.spawned[1].Cwd != owner.Session.Root {
		t.Fatal("join must create no worktrees and run in the owner's dir")
	}
	// Joining a joiner resolves to the owner.
	again, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "third", In: "review"})
	if err != nil || again.Session.SharedWith != owner.Session.ID {
		t.Fatalf("%v %+v", err, again)
	}
	if len(store.state.Joiners(owner.Session.ID)) != 2 {
		t.Fatal("joiners")
	}
	// Cannot join a session in another workspace.
	wm := &WorkspaceManager{Git: git, Store: store, WorkspacesRoot: filepath.Dir(store.state.Workspaces[0].Root)}
	if _, err := wm.Create(context.Background(), CreateWorkspaceInput{Name: "other", Repos: []RepoSpec{{Path: "/src/api"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Spawn(context.Background(), SpawnInput{Workspace: "other", Task: "x", In: owner.Session.ID}); err == nil {
		t.Fatal("must not join across workspaces")
	}
}

func TestSpawn_Preconditions(t *testing.T) {
	m, store, _, tm, _ := spawnFixture(t)
	if _, err := m.Spawn(context.Background(), SpawnInput{Workspace: "nope", Task: "t"}); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("%v", err)
	}
	store.state.Workspaces[0].Status = WorkspaceDeleting
	if _, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "t"}); !errors.Is(err, ErrWorkspaceNotReady) {
		t.Fatalf("%v", err)
	}
	store.state.Workspaces[0].Status = WorkspaceReady
	tm.available = errors.New("no tmux")
	if _, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "t"}); err == nil {
		t.Fatal("tmux unavailable must fail")
	}
	tm.available = nil
	m.Devin = &fakeDevin{available: errors.New("no devin")}
	if _, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "t"}); err == nil {
		t.Fatal("devin unavailable must fail")
	}
	if len(store.state.Sessions) != 0 || len(tm.spawned) != 0 {
		t.Fatal("preconditions must have no side effects")
	}
}

func TestWindowName(t *testing.T) {
	cases := map[string]string{
		"Fix the auth bug":           "Fix the auth bug",
		"  spaces\n and\tnewlines  ": "spaces and newlines",
		"":                           "s_abc",
		"a very long task description that goes on": "a very long task descri…",
		"target:with.separators":                    "target with separators",
	}
	for in, want := range cases {
		if got := WindowName(in, "s_abc"); got != want {
			t.Errorf("WindowName(%q) = %q, want %q", in, got, want)
		}
	}
}
