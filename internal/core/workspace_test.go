package core

import (
	"context"
	"errors"
	"os"
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

func TestCreate_HappyPath_MultiRepo(t *testing.T) {
	git := newFakeGit("/src/api", "/src/web")
	m, store := newManager(t, git)

	ws, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "feature-x", Repos: []RepoSpec{{Path: "/src/api"}, {Path: "/src/web/sub/dir"}}})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Status != WorkspaceReady || ws.Root != filepath.Join(m.WorkspacesRoot, "feature-x") || !strings.HasPrefix(ws.ID, "ws_") {
		t.Fatalf("unexpected workspace: %+v", ws)
	}
	if len(ws.Repos) != 2 || ws.Repos[0].Name != "api" || ws.Repos[1].Name != "web" {
		t.Fatalf("repos: %+v", ws.Repos)
	}
	for _, r := range ws.Repos {
		if r.Branch != "dmux/feature-x" || !r.CreatedBranch || r.WorktreePath != filepath.Join(ws.Root, r.Name) || len(r.BaseRef) != 40 {
			t.Fatalf("repo record: %+v", r)
		}
		if !dirExists(r.WorktreePath) {
			t.Fatalf("worktree dir missing: %s", r.WorktreePath)
		}
	}
	// Persisted state matches, status ready.
	if got := store.workspace(ws.ID); got == nil || got.Status != WorkspaceReady || len(got.Repos) != 2 {
		t.Fatalf("persisted: %+v", got)
	}
	// AGENTS.md map written at root, mentions both repos and the branch.
	md, err := os.ReadFile(filepath.Join(ws.Root, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Workspace: feature-x", "| api | ./api | dmux/feature-x |", "| web | ./web |", "not a git repository"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("AGENTS.md missing %q:\n%s", want, md)
		}
	}
	// git was asked to create branches from the resolved sha, not "HEAD".
	if !git.hasCall("resolveref:HEAD") || !git.hasCall("worktreeadd:"+ws.Repos[0].WorktreePath) {
		t.Fatalf("calls: %v", git.calls)
	}
}

func TestCreate_ExistingBranchIsNotOwned(t *testing.T) {
	git := newFakeGit("/src/api")
	git.repos["/src/api"]["hotfix"] = true
	m, _ := newManager(t, git)

	ws, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "fix", Repos: []RepoSpec{{Path: "/src/api", Branch: "hotfix"}}})
	if err != nil {
		t.Fatal(err)
	}
	r := ws.Repos[0]
	if r.Branch != "hotfix" || r.CreatedBranch {
		t.Fatalf("existing branch must not be marked created: %+v", r)
	}
}

func TestCreate_ValidationFailsBeforeAnySideEffect(t *testing.T) {
	cases := []struct {
		name string
		in   CreateWorkspaceInput
		want string
	}{
		{"bad name", CreateWorkspaceInput{Name: "has space", Repos: []RepoSpec{{Path: "/src/api"}}}, "invalid name"},
		{"no repos", CreateWorkspaceInput{Name: "x"}, "at least one"},
		{"not a repo", CreateWorkspaceInput{Name: "x", Repos: []RepoSpec{{Path: "/nowhere"}}}, "not a git repository"},
		{"missing branch", CreateWorkspaceInput{Name: "x", Repos: []RepoSpec{{Path: "/src/api", Branch: "ghost"}}}, "does not exist"},
		{"duplicate repo names", CreateWorkspaceInput{Name: "x", Repos: []RepoSpec{{Path: "/src/api"}, {Path: "/other/api"}}}, "both be named"},
		{"branch already exists", CreateWorkspaceInput{Name: "taken", Repos: []RepoSpec{{Path: "/src/api"}}}, "already exists"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			git := newFakeGit("/src/api", "/other/api")
			git.repos["/src/api"]["dmux/taken"] = true
			m, store := newManager(t, git)
			_, err := m.Create(context.Background(), tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			if git.hasCall("worktreeadd:") || len(store.state.Workspaces) != 0 {
				t.Fatalf("validation failure must have no side effects: calls=%v state=%+v", git.calls, store.state)
			}
			for _, c := range git.calls {
				if strings.HasPrefix(c, "worktreeadd") {
					t.Fatalf("worktree created during validation: %v", git.calls)
				}
			}
			if dirExists(filepath.Join(m.WorkspacesRoot, tc.in.Name)) {
				t.Fatal("root dir created during validation")
			}
		})
	}
}

func TestCreate_DuplicateNameRejectedByState(t *testing.T) {
	git := newFakeGit("/src/api")
	m, store := newManager(t, git)
	store.state.Workspaces = []Workspace{{ID: "ws_old", Name: "feature-x"}}
	_, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "feature-x", Repos: []RepoSpec{{Path: "/src/api"}}})
	if !errors.Is(err, ErrWorkspaceExists) {
		t.Fatalf("want ErrWorkspaceExists, got %v", err)
	}
	if len(store.state.Workspaces) != 1 {
		t.Fatal("state must be unchanged")
	}
}

// The core saga test: second worktree fails -> first is rolled back,
// dmux-created branch deleted, root removed, workspace left as failed.
func TestCreate_SecondRepoFails_RollsBackFirst(t *testing.T) {
	git := newFakeGit("/src/api", "/src/web")
	m, store := newManager(t, git)
	root := filepath.Join(m.WorkspacesRoot, "feature-x")
	git.failOn["worktreeadd:"+filepath.Join(root, "web")] = errors.New("disk full")

	_, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "feature-x", Repos: []RepoSpec{{Path: "/src/api"}, {Path: "/src/web"}}})
	var ce *CreateError
	if !errors.As(err, &ce) || ce.Step != "worktree add web" || ce.Rollback != nil {
		t.Fatalf("want clean-rollback CreateError at 'worktree add web', got %v", err)
	}
	// Compensation happened.
	if !git.hasCall("worktreeremove:"+filepath.Join(root, "api")) || !git.hasCall("branchdelete:dmux/feature-x") {
		t.Fatalf("rollback calls missing: %v", git.calls)
	}
	if git.repos["/src/api"]["dmux/feature-x"] {
		t.Fatal("dmux-created branch should be deleted on rollback")
	}
	if dirExists(root) {
		t.Fatal("workspace root should be removed after rollback")
	}
	// Record kept, visibly failed, for doctor.
	ws := store.state.Workspaces
	if len(ws) != 1 || ws[0].Status != WorkspaceFailed {
		t.Fatalf("workspace should remain with status failed: %+v", ws)
	}
}

// If the user supplied the branch, rollback must NOT delete it.
func TestCreate_RollbackNeverDeletesPreexistingBranch(t *testing.T) {
	git := newFakeGit("/src/api", "/src/web")
	git.repos["/src/api"]["hotfix"] = true
	m, _ := newManager(t, git)
	root := filepath.Join(m.WorkspacesRoot, "fix")
	git.failOn["worktreeadd:"+filepath.Join(root, "web")] = errors.New("boom")

	_, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "fix", Repos: []RepoSpec{{Path: "/src/api", Branch: "hotfix"}, {Path: "/src/web"}}})
	if err == nil {
		t.Fatal("expected failure")
	}
	if git.hasCall("branchdelete:hotfix") || !git.repos["/src/api"]["hotfix"] {
		t.Fatalf("pre-existing branch must survive rollback: %v", git.calls)
	}
}

// Rollback itself failing is reported, not hidden, and the record stays failed.
func TestCreate_RollbackFailureIsReported(t *testing.T) {
	git := newFakeGit("/src/api", "/src/web")
	m, store := newManager(t, git)
	root := filepath.Join(m.WorkspacesRoot, "ws")
	git.failOn["worktreeadd:"+filepath.Join(root, "web")] = errors.New("boom")
	git.failOn["worktreeremove:"+filepath.Join(root, "api")] = errors.New("locked")

	_, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "ws", Repos: []RepoSpec{{Path: "/src/api"}, {Path: "/src/web"}}})
	var ce *CreateError
	if !errors.As(err, &ce) || ce.Rollback == nil || !strings.Contains(err.Error(), "dmux doctor") {
		t.Fatalf("rollback failure must be surfaced with doctor hint: %v", err)
	}
	if store.state.Workspaces[0].Status != WorkspaceFailed || len(store.state.Workspaces[0].Repos) != 1 {
		t.Fatalf("failed record should list the repo that still exists: %+v", store.state.Workspaces[0])
	}
}

func TestCreate_StateReservationFailure_NoSideEffects(t *testing.T) {
	git := newFakeGit("/src/api")
	m, store := newManager(t, git)
	store.failNext = errors.New("lock timeout")
	_, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "x", Repos: []RepoSpec{{Path: "/src/api"}}})
	if err == nil || !strings.Contains(err.Error(), "lock timeout") {
		t.Fatalf("want store error, got %v", err)
	}
	for _, c := range git.calls {
		if strings.HasPrefix(c, "worktreeadd") {
			t.Fatal("no worktree may be created if reservation failed")
		}
	}
	if dirExists(filepath.Join(m.WorkspacesRoot, "x")) {
		t.Fatal("no root dir may be created if reservation failed")
	}
}

func TestList(t *testing.T) {
	m, store := newManager(t, newFakeGit())
	store.state.Workspaces = []Workspace{{ID: "a"}, {ID: "b"}}
	got, err := m.List()
	if err != nil || len(got) != 2 {
		t.Fatalf("List = %v %v", got, err)
	}
}

func TestWorkspaceAgentsMD_TruncatesShaAndListsRepos(t *testing.T) {
	md := WorkspaceAgentsMD(&Workspace{Name: "w", Repos: []WorkspaceRepo{
		{Name: "api", Branch: "dmux/w", BaseRef: "0123456789abcdef0123456789abcdef01234567"},
		{Name: "web", Branch: "hotfix", BaseRef: "hotfix"},
	}})
	if !strings.Contains(md, "| api | ./api | dmux/w | 0123456789ab |") || !strings.Contains(md, "| web | ./web | hotfix | hotfix |") {
		t.Fatalf("unexpected table:\n%s", md)
	}
	if fileExists("/definitely/not/here") {
		t.Fatal("helper sanity")
	}
}
