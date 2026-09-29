package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bradhu25/devin-mux/internal/core"
)

func TestParseRepoSpecs(t *testing.T) {
	dir := t.TempDir()
	atDir := filepath.Join(dir, "weird@name")
	if err := os.Mkdir(atDir, 0o755); err != nil {
		t.Fatal(err)
	}

	specs, err := parseRepoSpecs([]string{dir, dir + "@release/1.2", atDir + "@"})
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.Abs(dir)
	want := []core.RepoSpec{{Path: real}, {Path: real, BaseRef: "release/1.2"}, {Path: filepath.Join(real, "weird@name")}}
	for i := range want {
		if specs[i] != want[i] {
			t.Errorf("spec %d = %+v, want %+v", i, specs[i], want[i])
		}
	}

	for _, bad := range [][]string{nil, {""}, {"@main"}, {filepath.Join(dir, "missing")}} {
		if _, err := parseRepoSpecs(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestParseRepoSpecs_ExpandsHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	specs, err := parseRepoSpecs([]string{"~"})
	if err != nil {
		t.Fatal(err)
	}
	if specs[0].Path != home {
		t.Fatalf("~ expanded to %q, want %q", specs[0].Path, home)
	}
}

func TestRenderWorkspaces(t *testing.T) {
	var buf bytes.Buffer
	if err := renderWorkspaces(&buf, &core.State{}); err != nil || !strings.Contains(buf.String(), "No workspaces") {
		t.Fatalf("empty render: %q %v", buf.String(), err)
	}
	buf.Reset()
	err := renderWorkspaces(&buf, &core.State{
		Workspaces: []core.Workspace{{Name: "feature-x", ID: "ws_1", Status: core.WorkspaceReady, Root: "/r/feature-x",
			Repos: []core.RepoRef{{Name: "api", BaseRef: "HEAD"}, {Name: "web", BaseRef: "release/1.2"}}}},
		Sessions: []core.Session{{ID: "s_1", WorkspaceID: "ws_1"}, {ID: "s_2", WorkspaceID: "ws_1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "SESSIONS", "feature-x", "ws_1", "ready", "api,web@release/1.2", "/r/feature-x"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
