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
	m     *WorkspaceManager
	sm    *SessionManager
	git   *fakeGit
	store *fakeStore
	tm    *listingTmux
	proc  *fakeProc
	ws    Workspace
}

func newRmFixture(t *testing.T) *rmFixture {
	t.Helper()
	git := newFakeGit("/src/api", "/src/web")
	git.repos["/src/api"]["hotfix"] = true
	m, store := newManager(t, git)
	ws, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "w", Repos: []RepoSpec{{Path: "/src/api", Branch: "hotfix"}, {Path: "/src/web"}}})
	if err != nil {
		t.Fatal(err)
	}
	tm := &listingTmux{}
	tm.nextWindow = 10
	proc := &fakeProc{alive: map[int]bool{}}
	evs := &mutableEvents{byID: map[string][]SessionEvent{}}
	sm := newSessionManager(store, &tm.fakeTmux)
	sm.Tmux, sm.Proc, sm.Events = tm, proc, evs
	return &rmFixture{m: m, sm: sm, git: git, store: store, tm: tm, proc: proc, ws: *ws}
}

func (f *rmFixture) addSession(id string, alive bool, devinID string) {
	win := "@" + id[2:]
	f.store.state.Sessions = append(f.store.state.Sessions, Session{ID: id, WorkspaceID: f.ws.ID, Task: id, DevinSessionID: devinID, Tmux: TmuxTarget{WindowID: win}})
	pid := 1000 + len(f.store.state.Sessions)
	f.tm.windows = append(f.tm.windows, TmuxWindow{WindowID: win, DmuxSession: id, PanePID: pid, PaneDead: !alive})
	f.proc.alive[pid] = alive
	if alive {
		f.proc.onExit = func(int) {}
	}
}

func TestRemove_CleanWorkspace_DefaultsKeepBranches(t *testing.T) {
	f := newRmFixture(t)
	f.addSession("s_11", false, "olive-turkey")
	res, err := f.m.Remove(context.Background(), f.sm, "w", RemoveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.RemovedRepos) != 2 || len(res.DeletedBranches) != 0 || len(res.KeptBranches) != 2 {
		t.Fatalf("res: %+v", res)
	}
	if !f.git.repos["/src/web"]["dmux/w"] || !f.git.repos["/src/api"]["hotfix"] {
		t.Fatal("branches must be kept by default")
	}
	if res.DevinSessionIDs[0] != "olive-turkey" {
		t.Fatalf("devin ids for the user: %v", res.DevinSessionIDs)
	}
	if dirExists(f.ws.Root) {
		t.Fatal("root dir should be removed")
	}
	if len(f.store.state.Workspaces) != 0 || len(f.store.state.Sessions) != 0 {
		t.Fatalf("records must be deleted last: %+v", f.store.state)
	}
	if !f.git.hasCall("prune:/src/api") || !f.git.hasCall("prune:/src/web") {
		t.Fatalf("prune missing: %v", f.git.calls)
	}
	// Dead window for the exited session was closed.
	if len(f.tm.killed) != 1 || f.tm.killed[0] != "@11" {
		t.Fatalf("leftover window should be closed: %v", f.tm.killed)
	}
}

func TestRemove_DeleteBranches_OnlyCreatedOnes(t *testing.T) {
	f := newRmFixture(t)
	res, err := f.m.Remove(context.Background(), f.sm, "w", RemoveOptions{DeleteBranches: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DeletedBranches) != 1 || res.DeletedBranches[0] != "web@dmux/w" {
		t.Fatalf("deleted: %v", res.DeletedBranches)
	}
	if !f.git.repos["/src/api"]["hotfix"] || f.git.hasCall("branchdelete:hotfix") {
		t.Fatal("pre-existing branch must never be deleted")
	}
	if f.git.repos["/src/web"]["dmux/w"] {
		t.Fatal("dmux-created branch should be deleted with --delete-branches")
	}
}

func TestRemove_DeleteBranches_UnmergedIsKeptWithWarning(t *testing.T) {
	f := newRmFixture(t)
	f.git.failOn["branchdelete:dmux/w"] = errors.New("error: The branch 'dmux/w' is not fully merged.")
	res, err := f.m.Remove(context.Background(), f.sm, "w", RemoveOptions{DeleteBranches: true, Discard: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DeletedBranches) != 0 || len(res.Warnings) != 1 || !strings.Contains(res.KeptBranches[1], "not fully merged") {
		t.Fatalf("res: %+v", res)
	}
}

func TestRemove_RefusesRunningWithoutStop(t *testing.T) {
	f := newRmFixture(t)
	f.addSession("s_11", true, "")
	_, err := f.m.Remove(context.Background(), f.sm, "w", RemoveOptions{})
	var ref *RemoveRefusal
	if !errors.As(err, &ref) || len(ref.Running) != 1 || !strings.Contains(err.Error(), "--stop") {
		t.Fatalf("want running refusal, got %v", err)
	}
	f.assertUntouched(t)
}

func TestRemove_RefusesDirtyWithoutDiscard(t *testing.T) {
	f := newRmFixture(t)
	f.git.dirty[filepath.Join(f.ws.Root, "web")] = []string{" M x"}
	f.git.ahead[filepath.Join(f.ws.Root, "api")] = 2
	_, err := f.m.Remove(context.Background(), f.sm, "w", RemoveOptions{})
	var ref *RemoveRefusal
	if !errors.As(err, &ref) || len(ref.Unsafe) != 2 || !strings.Contains(err.Error(), "--discard") {
		t.Fatalf("want unsafe refusal for both repos, got %v", err)
	}
	f.assertUntouched(t)
}

func TestRemove_LockedIsRefusedEvenWithEveryFlag(t *testing.T) {
	f := newRmFixture(t)
	f.git.locked[filepath.Join(f.ws.Root, "web")] = true
	_, err := f.m.Remove(context.Background(), f.sm, "w", RemoveOptions{Stop: true, Discard: true, DeleteBranches: true})
	var ref *RemoveRefusal
	if !errors.As(err, &ref) || len(ref.Locked) != 1 || !strings.Contains(err.Error(), "never removes locked") {
		t.Fatalf("want locked refusal, got %v", err)
	}
	f.assertUntouched(t)
}

func TestRemove_StopAndDiscard(t *testing.T) {
	f := newRmFixture(t)
	f.addSession("s_11", true, "olive-turkey")
	f.git.dirty[filepath.Join(f.ws.Root, "web")] = []string{"?? junk"}
	res, err := f.m.Remove(context.Background(), f.sm, "w", RemoveOptions{Stop: true, Discard: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.StoppedSessions) != 1 || len(f.proc.terminated) != 1 {
		t.Fatalf("session should be stopped: %+v proc=%+v", res, f.proc)
	}
	if len(res.RemovedRepos) != 2 || len(f.store.state.Workspaces) != 0 {
		t.Fatalf("res: %+v", res)
	}
}

// A repo the user already removed with `git worktree remove` is skipped,
// not fatal.
func TestRemove_MissingWorktreeSkipped(t *testing.T) {
	f := newRmFixture(t)
	delete(f.git.worktree, filepath.Join(f.ws.Root, "web"))
	res, err := f.m.Remove(context.Background(), f.sm, "w", RemoveOptions{})
	if err != nil || len(res.RemovedRepos) != 2 {
		t.Fatalf("%v %+v", err, res)
	}
	if f.git.hasCall("worktreeremove:" + filepath.Join(f.ws.Root, "web")) {
		t.Fatal("must not try to remove an unregistered worktree")
	}
}

// Failure mid-saga leaves the workspace visibly in status deleting.
func TestRemove_FailureLeavesDeletingForDoctor(t *testing.T) {
	f := newRmFixture(t)
	f.git.failOn["worktreeremove:"+filepath.Join(f.ws.Root, "web")] = errors.New("disk error")
	_, err := f.m.Remove(context.Background(), f.sm, "w", RemoveOptions{})
	if err == nil || !strings.Contains(err.Error(), "dmux doctor") {
		t.Fatalf("want doctor hint, got %v", err)
	}
	if got := f.store.state.Workspaces[0].Status; got != WorkspaceDeleting {
		t.Fatalf("status = %s, want deleting", got)
	}
	// api's worktree was removed before the failure; record still lists it
	// so doctor knows what to check.
	if len(f.store.state.Workspaces[0].Repos) != 2 {
		t.Fatal("repos must remain in the record for doctor")
	}
}

func TestRemove_NotFound(t *testing.T) {
	f := newRmFixture(t)
	if _, err := f.m.Remove(context.Background(), f.sm, "nope", RemoveOptions{}); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatal(err)
	}
}

func (f *rmFixture) assertUntouched(t *testing.T) {
	t.Helper()
	if f.store.state.Workspaces[0].Status != WorkspaceReady {
		t.Fatalf("status changed on refusal: %s", f.store.state.Workspaces[0].Status)
	}
	for _, c := range f.git.calls {
		if strings.HasPrefix(c, "worktreeremove") || strings.HasPrefix(c, "branchdelete") || strings.HasPrefix(c, "prune") {
			t.Fatalf("refusal must have no side effects: %v", f.git.calls)
		}
	}
	if len(f.proc.terminated)+len(f.proc.killed)+len(f.tm.killed) != 0 {
		t.Fatal("refusal must not touch processes or windows")
	}
	if _, err := os.Stat(f.ws.Root); err != nil {
		t.Fatal("root must still exist")
	}
}
