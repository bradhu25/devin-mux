package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type renamingTmux struct {
	listingTmux
	renamed map[string]string
}

func (r *renamingTmux) RenameSession(_ context.Context, id, name string) error {
	if r.renamed == nil {
		r.renamed = map[string]string{}
	}
	r.renamed[id] = name
	return nil
}

func TestRename(t *testing.T) {
	git := newFakeGit("/src/api")
	m, store := newManager(t, git)
	ws, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "typo-x", Repos: []RepoSpec{{Path: "/src/api"}}})
	if err != nil {
		t.Fatal(err)
	}
	sessRoot := filepath.Join(ws.Root, "s_1")
	if err := os.MkdirAll(sessRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store.state.Sessions = []Session{{ID: "s_1", WorkspaceID: ws.ID, Root: sessRoot, Status: WorkspaceReady, Repos: []WorkspaceRepo{{Name: "api", Branch: "dmux/typo-x/x"}}, Tmux: TmuxTarget{SessionName: "dmux-typo-x", SessionID: "$3", WindowID: "@1"}}}
	tm := &renamingTmux{}
	tm.windows = []TmuxWindow{{SessionID: "$3", WindowID: "@1", DmuxSession: "s_1", WorkspaceID: ws.ID}}

	res, err := m.Rename(context.Background(), tm, "typo-x", "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if res.OldName != "typo-x" || res.Workspace.Name != "feature-x" || res.Workspace.ID != ws.ID || !res.TmuxRenamed || len(res.Warnings) != 0 {
		t.Fatalf("res: %+v", res)
	}
	// Record renamed, id stable, root unchanged, session's tmux name updated.
	got := store.state.Workspace(ws.ID)
	if got.Name != "feature-x" || got.Root != ws.Root || store.state.Sessions[0].Tmux.SessionName != "dmux-feature-x" {
		t.Fatalf("state: %+v %+v", got, store.state.Sessions[0])
	}
	if tm.renamed["$3"] != "dmux-feature-x" {
		t.Fatalf("tmux session not renamed: %v", tm.renamed)
	}
	// Session AGENTS.md regenerated with the new name; directories not moved.
	md, _ := os.ReadFile(filepath.Join(sessRoot, "AGENTS.md"))
	if !strings.Contains(string(md), "# Workspace: feature-x") || filepath.Base(ws.Root) != "typo-x" {
		t.Fatalf("map/dir: %s %s", md, ws.Root)
	}
	// Lookup by new name works; old name is gone.
	if store.state.WorkspaceByName("feature-x") == nil || store.state.WorkspaceByName("typo-x") != nil {
		t.Fatal("name lookup after rename")
	}
}

func TestRename_Validation(t *testing.T) {
	git := newFakeGit("/src/api", "/src/web")
	m, _ := newManager(t, git)
	if _, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "a", Repos: []RepoSpec{{Path: "/src/api"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(context.Background(), CreateWorkspaceInput{Name: "b", Repos: []RepoSpec{{Path: "/src/web"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rename(context.Background(), nil, "a", "b"); !errors.Is(err, ErrWorkspaceExists) {
		t.Fatalf("collision: %v", err)
	}
	if _, err := m.Rename(context.Background(), nil, "a", "bad name"); err == nil {
		t.Fatal("invalid name must be rejected")
	}
	if _, err := m.Rename(context.Background(), nil, "nope", "c"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("not found: %v", err)
	}
	// Renaming to the same name is a no-op, not an error.
	if res, err := m.Rename(context.Background(), nil, "a", "a"); err != nil || res.TmuxRenamed {
		t.Fatalf("same name: %v %+v", err, res)
	}
}
