package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newManager(t *testing.T, git *fakeGit) (*WorkspaceManager, *fakeStore) {
	t.Helper()
	store := &fakeStore{}
	m := &WorkspaceManager{Git: git, Store: store, WorkspacesRoot: filepath.Join(t.TempDir(), "workspaces"),
		Now: func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }}
	return m, store
}

func TestCreate_RecordsReposWithoutWorktrees(t *testing.T) {
	git := newFakeGit("/src/api", "/src/web")
	git.repos["/src/web"]["release/1.2"] = true
	m, store := newManager(t, git)

	ws, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "feature-x", Repos: []RepoSpec{{Path: "/src/api"}, {Path: "/src/web/sub/dir", BaseRef: "release/1.2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Status != WorkspaceReady || len(ws.Repos) != 2 || ws.Root != filepath.Join(m.WorkspacesRoot, "feature-x") {
		t.Fatalf("%+v", ws)
	}
	if ws.Repos[0].BaseRef != "HEAD" || ws.Repos[1].BaseRef != "release/1.2" || ws.Repos[1].SourcePath != "/src/web" || ws.Repos[1].Name != "web" {
		t.Fatalf("repo refs: %+v", ws.Repos)
	}
	if !dirExists(ws.Root) {
		t.Fatal("workspace root should exist")
	}
	// No worktrees, no branches: the workspace is just a group.
	if git.hasCall("worktreeadd") || len(git.repos["/src/api"]) != 1 {
		t.Fatalf("workspace creation must not touch git worktrees/branches: %v", git.calls)
	}
	if store.state.KnownRepos[0] != "/src/api" || len(store.state.KnownRepos) != 2 {
		t.Fatalf("known repos: %v", store.state.KnownRepos)
	}
}

func TestCreate_Validation(t *testing.T) {
	git := newFakeGit("/src/api")
	m, store := newManager(t, git)
	cases := map[string]CreateWorkspaceInput{
		"bad name":     {Name: "bad name", Repos: []RepoSpec{{Path: "/src/api"}}},
		"no repos":     {Name: "ok"},
		"not a repo":   {Name: "ok", Repos: []RepoSpec{{Path: "/elsewhere"}}},
		"dup repo":     {Name: "ok", Repos: []RepoSpec{{Path: "/src/api"}, {Path: "/src/api/x"}}},
		"bad base ref": {Name: "ok", Repos: []RepoSpec{{Path: "/src/api", BaseRef: "nope"}}},
	}
	for name, in := range cases {
		if _, err := m.Create(context.Background(), in); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if len(store.state.Workspaces) != 0 || store.updates != 0 {
		t.Fatal("validation failures must not write state")
	}
}

func TestCreate_DuplicateName(t *testing.T) {
	git := newFakeGit("/src/api")
	m, _ := newManager(t, git)
	if _, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "w", Repos: []RepoSpec{{Path: "/src/api"}}}); err != nil {
		t.Fatal(err)
	}
	_, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "w", Repos: []RepoSpec{{Path: "/src/api"}}})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want exists error, got %v", err)
	}
}

func TestCreate_StateFailureNoDir(t *testing.T) {
	git := newFakeGit("/src/api")
	m, store := newManager(t, git)
	store.failNext = errors.New("disk full")
	_, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "w", Repos: []RepoSpec{{Path: "/src/api"}}})
	if err == nil || dirExists(filepath.Join(m.WorkspacesRoot, "w")) {
		t.Fatalf("state failure must leave no directory: %v", err)
	}
}

func TestList(t *testing.T) {
	m, _ := newManager(t, newFakeGit("/src/api"))
	if _, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "a", Repos: []RepoSpec{{Path: "/src/api"}}}); err != nil {
		t.Fatal(err)
	}
	if l, _ := m.List(); len(l) != 1 || l[0].Name != "a" {
		t.Fatalf("%+v", l)
	}
}

func TestSessionAgentsMD(t *testing.T) {
	ws := &Workspace{Name: "feature-x"}
	s := &Session{ID: "s_1", Task: "Fix auth", Repos: []WorkspaceRepo{
		{Name: "api", Branch: "dmux/feature-x/fix-auth", BaseRef: "0123456789abcdef0123456789abcdef01234567"},
		{Name: "web", Branch: "release/1.2", BaseRef: "release/1.2"},
	}}
	md := SessionAgentsMD(ws, s)
	for _, want := range []string{"# Workspace: feature-x — session s_1", "Task: Fix auth", "| api | ./api | dmux/feature-x/fix-auth | 0123456789ab |", "| web | ./web | release/1.2 | release/1.2 |", "other\nsessions work in other directories"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
}

func TestSlugAndBranchName(t *testing.T) {
	for in, want := range map[string]string{
		"Add table-driven tests for the router": "add-table-driven-tests-for-the-router",
		"  Fix: auth/bug #12 !!  ":              "fix-auth-bug-12",
		"":                                      "",
		strings.Repeat("word ", 20):             "word-word-word-word-word-word-word-word",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	if got := BranchName("chi", "Add tests", "s_1"); got != "dmux/chi/add-tests" {
		t.Fatalf("%q", got)
	}
	if got := BranchName("chi", "", "s_1"); got != "dmux/chi/s_1" {
		t.Fatalf("no task should fall back to the id: %q", got)
	}
}
