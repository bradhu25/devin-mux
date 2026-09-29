package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func inspectFixture(t *testing.T) (*WorkspaceManager, *fakeGit, Workspace) {
	t.Helper()
	git := newFakeGit("/src/api", "/src/web")
	m, store := newManager(t, git)
	ws, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "w", Repos: []RepoSpec{{Path: "/src/api"}, {Path: "/src/web"}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = store
	return m, git, *ws
}

func TestInspect_CleanWorkspace(t *testing.T) {
	m, _, ws := inspectFixture(t)
	st, err := m.Inspect(context.Background(), "w")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Repos) != 2 || st.AnyUnsafe() {
		t.Fatalf("fresh workspace should be clean: %+v", st.Repos)
	}
	if st.Workspace.ID != ws.ID {
		t.Fatal("wrong workspace")
	}
	// Lookup by id works too.
	if _, err := m.Inspect(context.Background(), ws.ID); err != nil {
		t.Fatal(err)
	}
}

func TestInspect_DirtyAheadLockedMissing(t *testing.T) {
	m, git, ws := inspectFixture(t)
	api, web := filepath.Join(ws.Root, "api"), filepath.Join(ws.Root, "web")
	git.dirty[api] = []string{" M README.md", "?? new.txt"}
	git.ahead[api] = 3
	git.locked[web] = true

	st, _ := m.Inspect(context.Background(), "w")
	a, w := st.Repos[0], st.Repos[1]
	if len(a.Dirty) != 2 || a.CommitsAhead != 3 || a.Clean() {
		t.Fatalf("api: %+v", a)
	}
	if !w.Locked || w.Clean() {
		t.Fatalf("web: %+v", w)
	}
	if !st.AnyUnsafe() {
		t.Fatal("must be unsafe")
	}

	// Worktree unregistered behind our back (user ran git worktree remove).
	delete(git.worktree, api)
	st, _ = m.Inspect(context.Background(), "w")
	if !st.Repos[0].Missing || st.Repos[0].Clean() {
		t.Fatalf("missing worktree: %+v", st.Repos[0])
	}
}

func TestInspect_GitErrorIsUnsafe(t *testing.T) {
	m, git, ws := inspectFixture(t)
	git.failOn["status:"+filepath.Join(ws.Root, "api")] = errors.New("boom")
	st, err := m.Inspect(context.Background(), "w")
	if err != nil {
		t.Fatal(err)
	}
	if st.Repos[0].Err == nil || st.Repos[0].Clean() || !st.AnyUnsafe() {
		t.Fatalf("inspection failure must read as unsafe: %+v", st.Repos[0])
	}
}

func TestInspect_NotFound(t *testing.T) {
	m, _, _ := inspectFixture(t)
	if _, err := m.Inspect(context.Background(), "nope"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatal(err)
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
