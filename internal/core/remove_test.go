package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type rmFixture struct {
	wm    *WorkspaceManager
	sm    *SessionManager
	git   *fakeGit
	store *fakeStore
	tm    *listingTmux
	proc  *fakeProc
	ws    Workspace
	a, b  Session // two isolated sessions
}

// newRmFixture: workspace over api (sessions branch from HEAD) and web;
// two sessions with their own worktrees; tmux/proc fakes for liveness.
func newRmFixture(t *testing.T) *rmFixture {
	t.Helper()
	m, store, git, _, ws := spawnFixture(t)
	tm := &listingTmux{}
	tm.nextWindow = 10
	proc := &fakeProc{alive: map[int]bool{}}
	m.Tmux, m.Proc, m.Events = tm, proc, &mutableEvents{byID: map[string][]SessionEvent{}}
	a, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "tests"})
	if err != nil {
		t.Fatal(err)
	}
	wm := &WorkspaceManager{Git: git, Store: store, WorkspacesRoot: filepath.Dir(ws.Root)}
	return &rmFixture{wm: wm, sm: m, git: git, store: store, tm: tm, proc: proc, ws: ws, a: a.Session, b: b.Session}
}

// makeLive gives the session a live window + pid.
func (f *rmFixture) makeLive(s Session) {
	pid := 1000 + len(f.tm.windows)
	f.tm.windows = append(f.tm.windows, TmuxWindow{WindowID: s.Tmux.WindowID, DmuxSession: s.ID, PanePID: pid})
	f.proc.alive[pid] = true
}

func TestRemoveSession_CleanKeepsBranch(t *testing.T) {
	f := newRmFixture(t)
	res, err := f.wm.RemoveSession(context.Background(), f.sm, "auth", RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.RemovedSessions) != 1 || len(res.RemovedWorktrees) != 2 || len(res.DeletedBranches) != 0 || len(res.KeptBranches) != 2 {
		t.Fatalf("%+v", res)
	}
	if !f.git.repos["/src/api"][f.a.Repos[0].Branch] {
		t.Fatal("branch kept by default")
	}
	if dirExists(f.a.Root) || f.store.state.Session(f.a.ID) != nil {
		t.Fatal("session dir and record must be gone")
	}
	// The other session is untouched.
	if !dirExists(f.b.Root) || f.store.state.Session(f.b.ID) == nil || !f.git.repos["/src/api"][f.b.Repos[0].Branch] {
		t.Fatal("other session must be untouched")
	}
	if !dirExists(f.ws.Root) || f.store.state.Workspace(f.ws.ID) == nil {
		t.Fatal("workspace stays")
	}
}

func TestRemoveSession_DeleteBranchesOnlyOwned(t *testing.T) {
	f := newRmFixture(t)
	// Pretend api's branch pre-existed (not owned).
	f.store.state.Session(f.a.ID).Repos[0].CreatedBranch = false
	res, err := f.wm.RemoveSession(context.Background(), f.sm, f.a.ID, RemoveOptions{DeleteBranches: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DeletedBranches) != 1 || res.DeletedBranches[0] != f.a.Repos[1].Branch || !strings.Contains(res.KeptBranches[0], "pre-existing") {
		t.Fatalf("%+v", res)
	}
	if !f.git.repos["/src/api"][f.a.Repos[0].Branch] || f.git.repos["/src/web"][f.a.Repos[1].Branch] {
		t.Fatal("only the dmux-created branch may be deleted")
	}
}

func TestRemoveSession_Refusals(t *testing.T) {
	f := newRmFixture(t)
	f.makeLive(f.a)
	f.git.dirty[f.a.Repos[1].WorktreePath] = []string{" M x"}
	_, err := f.wm.RemoveSession(context.Background(), f.sm, f.a.ID, RemoveOptions{})
	var ref *RemoveRefusal
	if !errors.As(err, &ref) || len(ref.Running) != 1 || len(ref.Unsafe) != 1 || !strings.Contains(err.Error(), "--stop") || !strings.Contains(err.Error(), "--discard") {
		t.Fatalf("%v", err)
	}
	f.assertUntouched(t, f.a)

	f.git.locked[f.a.Repos[0].WorktreePath] = true
	_, err = f.wm.RemoveSession(context.Background(), f.sm, f.a.ID, RemoveOptions{Stop: true, Discard: true, DeleteBranches: true})
	if !errors.As(err, &ref) || len(ref.Locked) != 1 {
		t.Fatalf("locked must refuse regardless of flags: %v", err)
	}
	f.assertUntouched(t, f.a)
}

func TestRemoveSession_StopAndDiscard(t *testing.T) {
	f := newRmFixture(t)
	f.makeLive(f.a)
	f.git.dirty[f.a.Repos[0].WorktreePath] = []string{"?? junk"}
	res, err := f.wm.RemoveSession(context.Background(), f.sm, f.a.ID, RemoveOptions{Stop: true, Discard: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.StoppedSessions) != 1 || len(f.proc.terminated) != 1 || len(res.RemovedWorktrees) != 2 {
		t.Fatalf("%+v", res)
	}
}

func TestRemoveSession_OwnerWithJoinersRefused_JoinerAlone(t *testing.T) {
	f := newRmFixture(t)
	joined, err := f.sm.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "review", In: f.a.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.wm.RemoveSession(context.Background(), f.sm, f.a.ID, RemoveOptions{})
	var ref *RemoveRefusal
	if !errors.As(err, &ref) || len(ref.Joiners) != 1 || ref.Joiners[0] != joined.Session.ID {
		t.Fatalf("owner with joiners must refuse: %v", err)
	}
	// Removing the joiner touches no git.
	before := len(f.git.worktree)
	res, err := f.wm.RemoveSession(context.Background(), f.sm, joined.Session.ID, RemoveOptions{})
	if err != nil || len(res.RemovedWorktrees) != 0 || len(f.git.worktree) != before || f.store.state.Session(joined.Session.ID) != nil {
		t.Fatalf("%v %+v", err, res)
	}
	// Now the owner can go.
	if _, err := f.wm.RemoveSession(context.Background(), f.sm, f.a.ID, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveWorkspace_AllSessions(t *testing.T) {
	f := newRmFixture(t)
	f.makeLive(f.b)
	if _, err := f.sm.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "review", In: f.a.ID}); err != nil {
		t.Fatal(err)
	}
	// Running session: refused without --stop, even though joiners would be fine.
	_, err := f.wm.Remove(context.Background(), f.sm, "feature-x", RemoveOptions{})
	var ref *RemoveRefusal
	if !errors.As(err, &ref) || len(ref.Running) != 1 || len(ref.Joiners) != 0 {
		t.Fatalf("%v", err)
	}
	if f.store.state.Workspace(f.ws.ID).Status != WorkspaceReady {
		t.Fatal("refusal must not mark deleting")
	}
	res, err := f.wm.Remove(context.Background(), f.sm, "feature-x", RemoveOptions{Stop: true, DeleteBranches: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.RemovedSessions) != 3 || len(res.StoppedSessions) != 1 || len(res.RemovedWorktrees) != 4 || len(res.DeletedBranches) != 4 || res.Workspace == nil {
		t.Fatalf("%+v", res)
	}
	if len(f.store.state.Workspaces) != 0 || len(f.store.state.Sessions) != 0 || dirExists(f.ws.Root) {
		t.Fatalf("everything gone: %+v", f.store.state)
	}
	for _, r := range []string{"/src/api", "/src/web"} {
		if len(f.git.repos[r]) != 1 {
			t.Fatalf("dmux branches should be deleted in %s: %v", r, f.git.repos[r])
		}
	}
	// Joiners were removed before owners.
	if !strings.HasPrefix(res.RemovedSessions[0], "s_") || res.RemovedSessions[0] == f.a.ID || res.RemovedSessions[0] == f.b.ID {
		t.Fatalf("joiner first: %v", res.RemovedSessions)
	}
}

func TestRemoveWorkspace_FailureLeavesDeleting(t *testing.T) {
	f := newRmFixture(t)
	f.git.failOn["worktreeremove:"+f.b.Repos[1].WorktreePath] = errors.New("disk error")
	_, err := f.wm.Remove(context.Background(), f.sm, "feature-x", RemoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "dmux doctor") {
		t.Fatalf("%v", err)
	}
	ws := f.store.state.Workspace(f.ws.ID)
	if ws == nil || ws.Status != WorkspaceDeleting {
		t.Fatalf("workspace should remain in deleting: %+v", ws)
	}
	// a removed fully; b stuck in deleting with its repos recorded.
	if f.store.state.Session(f.a.ID) != nil {
		t.Fatal("a should be gone")
	}
	if b := f.store.state.Session(f.b.ID); b == nil || b.Status != WorkspaceDeleting || len(b.Repos) != 2 {
		t.Fatalf("b for doctor: %+v", b)
	}
}

func TestRemove_NotFound(t *testing.T) {
	f := newRmFixture(t)
	if _, err := f.wm.Remove(context.Background(), f.sm, "nope", RemoveOptions{}); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatal(err)
	}
	if _, err := f.wm.RemoveSession(context.Background(), f.sm, "nope", RemoveOptions{}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal(err)
	}
}

func (f *rmFixture) assertUntouched(t *testing.T, s Session) {
	t.Helper()
	if got := f.store.state.Session(s.ID); got == nil || got.Status != WorkspaceReady {
		t.Fatalf("status changed on refusal: %+v", got)
	}
	for _, c := range f.git.calls {
		if strings.HasPrefix(c, "worktreeremove") || strings.HasPrefix(c, "branchdelete") || strings.HasPrefix(c, "prune") {
			t.Fatalf("refusal must have no side effects: %v", f.git.calls)
		}
	}
	if len(f.proc.terminated)+len(f.proc.killed)+len(f.tm.killed) != 0 {
		t.Fatal("refusal must not touch processes or windows")
	}
	if _, err := os.Stat(s.Root); err != nil {
		t.Fatal("session dir must still exist")
	}
}
