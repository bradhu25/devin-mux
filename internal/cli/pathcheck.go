package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// binaryOnPath reports whether `dmux` on PATH is this binary, and if not,
// how to fix it for the user's shell. Two failure modes matter:
//
//   - not found: the install directory is not on PATH
//   - found but different: a stale copy shadows the running build
func binaryOnPath(self string) (bool, string) {
	self = resolve(self)
	found, err := lookPath("dmux")
	if err == nil && resolve(found) == self {
		return true, ""
	}
	dir := filepath.Dir(self)
	if err == nil {
		return false, fmt.Sprintf("PATH resolves `dmux` to %s, but you are running %s. Remove or replace the stale copy, or put %s earlier on PATH.", found, self, dir)
	}
	return false, fmt.Sprintf("%s is not on PATH. Add it for your shell:\n          %s\n        then open a new terminal (or `source` the file).", dir, pathFixCommand(dir))
}

// pathFixCommand returns the exact line to append, for the user's login
// shell. macOS terminals start *login* shells, which read ~/.bash_profile
// (bash) or ~/.zprofile / ~/.zshrc (zsh); a line in ~/.bashrc alone is not
// picked up by a login bash, which is the classic cause of "it works in one
// terminal but not another".
func pathFixCommand(dir string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(dir, home) {
		dir = "$HOME" + strings.TrimPrefix(dir, home)
	}
	line := fmt.Sprintf(`export PATH="%s:$PATH"`, dir)
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		return fmt.Sprintf("echo '%s' >> ~/.zshrc", line)
	case "fish":
		return fmt.Sprintf("fish_add_path %s", dir)
	case "bash":
		return fmt.Sprintf("echo '%s' >> ~/.bash_profile   # login shells (macOS terminals) read this, not ~/.bashrc", line)
	}
	return fmt.Sprintf("add to your shell's startup file: %s", line)
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// lookPath finds the first executable named file on PATH, like
// exec.LookPath, without importing os/exec (the cli layer must not spawn
// processes; this only inspects the filesystem).
func lookPath(file string) (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, file)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", errors.New("not found on PATH")
}
