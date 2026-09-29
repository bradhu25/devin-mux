package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// inspectFixture: workspace with two sessions, each owning api+web worktrees.
func inspectFixture(t *testing.T) (*WorkspaceManager, *fakeGit, Session, Session) {
	t.Helper()
	m, _, git, _, _ := spawnFixture(t)
	a, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "tests"})
	if err != nil {
		t.Fatal(err)
	}
	wm := &WorkspaceManager{Git: git, Store: m.Store}
	return wm, git, a.Session, b.Session
}

func TestInspect_CleanWorkspaceAllSessions(t *testing.T) {
	wm, _, a, b := inspectFixture(t)
	st, err := wm.Inspect(context.Background(), "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Sessions) != 2 || st.AnyUnsafe() {
		t.Fatalf("fresh sessions should be clean: %+v", st.Sessions)
	}
	if len(st.Sessions[0].Repos) != 2 || st.Sessions[0].Session.ID != a.ID || st.Sessions[1].Session.ID != b.ID {
		t.Fatalf("%+v", st.Sessions)
	}
	if _, err := wm.Inspect(context.Background(), "nope"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatal(err)
	}
}

func TestInspectSession_DirtyAheadLockedMissing(t *testing.T) {
	wm, git, a, b := inspectFixture(t)
	api, web := filepath.Join(a.Root, "api"), filepath.Join(a.Root, "web")
	git.dirty[api] = []string{" M README.md", "?? new.txt"}
	git.ahead[api] = 3
	git.locked[web] = true

	si, err := wm.InspectSession(context.Background(), "auth")
	if err != nil {
		t.Fatal(err)
	}
	ra, rw := si.Repos[0], si.Repos[1]
	if len(ra.Dirty) != 2 || ra.CommitsAhead != 3 || ra.Clean() || !rw.Locked || rw.Clean() || !si.AnyUnsafe() {
		t.Fatalf("%+v %+v", ra, rw)
	}
	// Session b is unaffected: its own worktrees.
	if sb, _ := wm.InspectSession(context.Background(), b.ID); sb.AnyUnsafe() {
		t.Fatalf("other session must be clean: %+v", sb.Repos)
	}
	// Worktree unregistered behind our back.
	delete(git.worktree, api)
	si, _ = wm.InspectSession(context.Background(), a.ID)
	if !si.Repos[0].Missing || si.Repos[0].Clean() {
		t.Fatalf("%+v", si.Repos[0])
	}
	// Git error reads as unsafe.
	git.failOn["status:"+filepath.Join(b.Root, "api")] = errors.New("boom")
	if sb, _ := wm.InspectSession(context.Background(), b.ID); sb.Repos[0].Err == nil || !sb.AnyUnsafe() {
		t.Fatalf("%+v", sb.Repos[0])
	}
}

func TestInspect_JoinerOwnsNothing(t *testing.T) {
	m, _, _, _, _ := spawnFixture(t)
	owner, _ := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "implement"})
	_, err := m.Spawn(context.Background(), SpawnInput{Workspace: "feature-x", Task: "review", In: owner.Session.ID})
	if err != nil {
		t.Fatal(err)
	}
	wm := &WorkspaceManager{Git: m.Git, Store: m.Store}
	si, _ := wm.InspectSession(context.Background(), "review")
	if len(si.Repos) != 0 || si.AnyUnsafe() {
		t.Fatalf("joiner inspection should be empty: %+v", si)
	}
}

func TestSamePath(t *testing.T) {
	if !samePath("/a/b/../c", "/a/c") {
		t.Fatal("clean paths should match")
	}
	if samePath("/a/c", "/a/d") {
		t.Fatal("different paths must not match")
	}
}
