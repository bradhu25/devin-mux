package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinaryOnPath(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "dmux")
	if err := os.WriteFile(self, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Not on PATH at all.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", "/bin/bash")
	ok, advice := binaryOnPath(self)
	if ok || !strings.Contains(advice, "is not on PATH") || !strings.Contains(advice, ".bash_profile") {
		t.Fatalf("ok=%v advice=%q", ok, advice)
	}

	// On PATH and identical (via a symlink, as the dev setup uses).
	link := filepath.Join(t.TempDir(), "dmux")
	if err := os.Symlink(self, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(link))
	if ok, _ := binaryOnPath(self); !ok {
		t.Fatal("symlink to the running binary should count as on PATH")
	}

	// Shadowed by a stale copy.
	stale := filepath.Join(t.TempDir(), "dmux")
	if err := os.WriteFile(stale, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(stale))
	ok, advice = binaryOnPath(self)
	if ok || !strings.Contains(advice, "stale") {
		t.Fatalf("stale copy should be reported: ok=%v advice=%q", ok, advice)
	}
}

func TestPathFixCommand_PerShell(t *testing.T) {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "bin")
	for shell, want := range map[string]string{"/bin/zsh": ".zshrc", "/bin/bash": ".bash_profile", "/usr/bin/fish": "fish_add_path", "/bin/tcsh": "startup file"} {
		t.Setenv("SHELL", shell)
		got := pathFixCommand(dir)
		if !strings.Contains(got, want) || (shell != "/usr/bin/fish" && !strings.Contains(got, "$HOME/.local/bin")) {
			t.Errorf("%s: %q", shell, got)
		}
	}
}
