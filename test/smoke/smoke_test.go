//go:build smoke

// Package smoke runs dmux end to end against the real Devin CLI, git, and
// an isolated tmux server. It launches paid agent sessions (with trivial
// prompts), so it is double-gated: build tag `smoke` and DMUX_SMOKE=1.
// Run with `make smoke`.
package smoke

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type harness struct {
	t      *testing.T
	bin    string
	root   string // DMUX_ROOT
	socket string // isolated tmux -L
	src    string // throwaway source repos
	devin  []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if os.Getenv("DMUX_SMOKE") != "1" {
		t.Skip("set DMUX_SMOKE=1 to run smoke tests (launches real Devin sessions)")
	}
	for _, tool := range []string{"devin", "tmux", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	bin, err := filepath.Abs("../../bin/dmux")
	if err != nil || !fileExists(bin) {
		t.Fatal("build first: make build")
	}
	h := &harness{t: t, bin: bin, root: t.TempDir(), socket: fmt.Sprintf("dmux-smoke-%d", time.Now().UnixNano()%1_000_000), src: t.TempDir()}
	t.Cleanup(h.cleanup)
	return h
}

func (h *harness) cleanup() {
	_ = exec.Command("tmux", "-L", h.socket, "kill-server").Run()
	_ = os.Remove(filepath.Join("/private/tmp", fmt.Sprintf("tmux-%d", os.Getuid()), h.socket))
	// Remove the Devin conversations this run created so the user's history
	// stays clean. Best effort.
	for _, id := range h.devin {
		_ = exec.Command("devin", "rm", "--force", id).Run()
	}
}

// dmux runs the binary with the isolated environment and returns stdout+stderr.
func (h *harness) dmux(args ...string) (string, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.bin, args...)
	cmd.Env = append(os.Environ(), "DMUX_ROOT="+h.root, "DMUX_TMUX_SOCKET="+h.socket)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (h *harness) must(args ...string) string {
	h.t.Helper()
	out, err := h.dmux(args...)
	if err != nil {
		h.t.Fatalf("dmux %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (h *harness) mustFail(contains string, args ...string) string {
	h.t.Helper()
	out, err := h.dmux(args...)
	if err == nil {
		h.t.Fatalf("dmux %s should have failed\n%s", strings.Join(args, " "), out)
	}
	if !strings.Contains(out, contains) {
		h.t.Fatalf("dmux %s: want %q in output:\n%s", strings.Join(args, " "), contains, out)
	}
	return out
}

func (h *harness) repo(name string) string {
	h.t.Helper()
	dir := filepath.Join(h.src, name)
	for _, args := range [][]string{{"init", "-q", "-b", "main", dir}, {"-C", dir, "-c", "user.email=s@s", "-c", "user.name=smoke", "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			h.t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

// waitStatus polls `dmux ls` until the session's status column matches.
func (h *harness) waitStatus(session, want string, timeout time.Duration) string {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		last = h.must("ls")
		for _, line := range strings.Split(last, "\n") {
			if strings.Contains(line, session) && strings.Contains(line, want) {
				return line
			}
		}
		time.Sleep(time.Second)
	}
	h.t.Fatalf("session %s never reached %q within %s; last ls:\n%s", session, want, timeout, last)
	return ""
}

func (h *harness) sessionID(spawnOut string) string {
	h.t.Helper()
	for _, f := range strings.Fields(spawnOut) {
		if strings.HasPrefix(f, "s_") {
			return f
		}
	}
	h.t.Fatalf("no session id in: %s", spawnOut)
	return ""
}

func (h *harness) state() map[string]any {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.root, "state.json"))
	if err != nil {
		h.t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		h.t.Fatal(err)
	}
	return st
}

func (h *harness) devinIDs() []string {
	var ids []string
	for _, s := range h.state()["sessions"].([]any) {
		if id, _ := s.(map[string]any)["devinSessionId"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (h *harness) pane(window string) string {
	out, _ := exec.Command("tmux", "-L", h.socket, "capture-pane", "-p", "-t", window).Output()
	return string(out)
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// TestLifecycle is the full vertical path: workspace -> two concurrent
// sessions -> live status -> kill -> resume with recall -> status -> rm.
func TestLifecycle(t *testing.T) {
	h := newHarness(t)
	api, web := h.repo("api"), h.repo("web")

	// Hooks are read from the user's real Devin config; they must point at
	// THIS binary for status to work. Verify rather than modify.
	if out := h.must("doctor"); strings.Contains(out, "hooks are not installed") {
		t.Fatalf("run `dmux init` with this binary first (%s):\n%s", h.bin, out)
	}

	// Workspace: a repo group, no worktrees yet.
	out := h.must("workspace", "new", "feature-x", "--repo", api, "--repo", web)
	if !strings.Contains(out, "api/") || !strings.Contains(out, "web/") || !strings.Contains(out, "sessions branch from HEAD") {
		t.Fatalf("workspace new:\n%s", out)
	}
	h.mustFail("already exists", "workspace", "new", "feature-x", "--repo", api)
	for _, r := range []string{api, web} {
		if wt, _ := exec.Command("git", "-C", r, "worktree", "list").Output(); strings.Count(strings.TrimSpace(string(wt)), "\n") != 0 {
			t.Fatalf("workspace new must create no worktrees:\n%s", wt)
		}
	}

	// Two concurrent sessions, each in its own worktrees on its own branch.
	out = h.must("spawn", "feature-x", "-t", "Reply with exactly the word ALPHA and nothing else. Do not use tools.")
	s1 := h.sessionID(out)
	if !strings.Contains(out, "dmux/feature-x/reply-with-exactly-the-word-alpha") {
		t.Fatalf("spawn should report the task branch:\n%s", out)
	}
	s2 := h.sessionID(h.must("spawn", "feature-x", "-t", "Reply with exactly the word BRAVO and nothing else. Do not use tools."))
	for _, sid := range []string{s1, s2} {
		if !fileExists(filepath.Join(h.root, "workspaces", "feature-x", sid, "AGENTS.md")) || !fileExists(filepath.Join(h.root, "workspaces", "feature-x", sid, "api", ".git")) {
			t.Fatalf("session %s should have its own dir with worktrees and a map", sid)
		}
	}
	for _, r := range []string{api, web} {
		wt, _ := exec.Command("git", "-C", r, "worktree", "list").Output()
		if strings.Count(strings.TrimSpace(string(wt)), "\n") != 2 {
			t.Fatalf("each session should add one worktree per repo:\n%s", wt)
		}
	}
	ls := h.must("ls")
	if !strings.Contains(ls, "BRANCH") || strings.Count(ls, "dmux/feature-x/") != 2 {
		t.Fatalf("ls should show a branch per session:\n%s", ls)
	}

	// Status: both reach idle via hooks; devin ids captured.
	h.waitStatus(s1, "idle", 60*time.Second)
	h.waitStatus(s2, "idle", 60*time.Second)
	h.devin = h.devinIDs()
	if len(h.devin) != 2 {
		t.Fatalf("devinSessionId not captured for both sessions: %v", h.devin)
	}
	ls = h.must("ls", "-v")
	if !strings.Contains(ls, "» ALPHA") || !strings.Contains(ls, "» BRAVO") {
		t.Fatalf("ls -v should show last messages:\n%s", ls)
	}

	// Jump resolution (non-interactive paths): ambiguous, by task, gone.
	h.mustFail("multiple sessions", "jump", "s_")
	// Attaching needs a tty; reaching tmux's error proves the path was taken.
	if out, err := h.dmux("jump", "bravo"); err == nil || !strings.Contains(out, "terminal") {
		t.Fatalf("jump outside tmux should exec into attach (fails for lack of tty): %v %s", err, out)
	}

	// Kill s1 cleanly; record kept; resume with recall.
	out = h.must("kill", s1)
	if !strings.Contains(out, "clean exit") || !strings.Contains(out, "dmux resume "+s1) {
		t.Fatalf("kill:\n%s", out)
	}
	h.waitStatus(s1, "exited", 10*time.Second)
	h.mustFail("no live tmux window", "jump", s1)
	h.mustFail("still running", "resume", s2)

	out = h.must("resume", s1, "-t", "What single word did you reply with earlier in this conversation? Reply with just that word.")
	if !strings.Contains(out, h.devin[0]) && !strings.Contains(out, h.devin[1]) {
		t.Fatalf("resume should name the devin conversation:\n%s", out)
	}
	line := h.waitStatus(s1, "idle", 60*time.Second)
	ls = h.must("ls", "-v")
	if !strings.Contains(ls, "» ALPHA") {
		t.Fatalf("resumed agent should recall ALPHA; ls -v:\n%s\n(line: %s)", ls, line)
	}
	if len(h.devinIDs()) != 2 {
		t.Fatal("resume must reuse the recorded conversation, not create a new one")
	}

	// Status per session: clean; dirty s2 -> only s2 is unsafe; rm refuses.
	if out := h.must("workspace", "status", "feature-x"); strings.Count(out, "clean — nothing would be lost") != 4 {
		t.Fatalf("status:\n%s", out)
	}
	if err := os.WriteFile(filepath.Join(h.root, "workspaces", "feature-x", s2, "web", "junk.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := h.must("session", "status", s1); strings.Contains(out, "junk.txt") {
		t.Fatalf("s1 must not see s2's changes (isolation):\n%s", out)
	}
	h.mustFail("--stop", "workspace", "rm", "feature-x")
	h.mustFail("--discard", "workspace", "rm", "feature-x")
	h.mustFail("pass --yes", "workspace", "rm", "feature-x", "--stop", "--discard")
	// Single-session removal keeps the rest.
	h.mustFail("--discard", "rm", s2, "--stop", "--yes")
	out = h.must("rm", s2, "--stop", "--discard", "--delete-branches", "--yes")
	if !strings.Contains(out, "removed worktrees: "+s2+"/api, "+s2+"/web") || !strings.Contains(out, "deleted branches") {
		t.Fatalf("rm session:\n%s", out)
	}
	if !fileExists(filepath.Join(h.root, "workspaces", "feature-x", s1, "api")) {
		t.Fatal("removing s2 must leave s1's worktrees")
	}
	out = h.must("workspace", "rm", "feature-x", "--stop", "--delete-branches", "--yes")
	for _, want := range []string{"Removed workspace feature-x", "removed sessions:  " + s1, "stopped sessions", "removed worktrees: " + s1 + "/api, " + s1 + "/web", "deleted branches", "Devin conversations kept"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rm output missing %q:\n%s", want, out)
		}
	}
	for _, r := range []string{api, web} {
		b, _ := exec.Command("git", "-C", r, "branch", "--format=%(refname:short)").Output()
		if strings.Contains(string(b), "dmux/") {
			t.Fatalf("dmux branch left in %s: %s", r, b)
		}
		wt, _ := exec.Command("git", "-C", r, "worktree", "list").Output()
		if strings.Count(strings.TrimSpace(string(wt)), "\n") != 0 {
			t.Fatalf("worktrees left in %s:\n%s", r, wt)
		}
	}
	if fileExists(filepath.Join(h.root, "workspaces", "feature-x")) {
		t.Fatal("workspace root not removed")
	}
	if out := h.must("ls"); !strings.Contains(out, "No workspaces") {
		t.Fatalf("ls after rm:\n%s", out)
	}
	if out := h.must("doctor"); !strings.Contains(out, "No problems found") {
		t.Fatalf("doctor after rm:\n%s", out)
	}
}

// TestServerRestart simulates a reboot: the tmux server disappears under
// running sessions. Status must degrade honestly and resume must work.
func TestServerRestart(t *testing.T) {
	h := newHarness(t)
	api := h.repo("api")
	h.must("workspace", "new", "w", "--repo", api)
	s := h.sessionID(h.must("spawn", "w", "-t", "Reply with exactly CHARLIE and nothing else. Do not use tools."))
	h.waitStatus(s, "idle", 60*time.Second)
	h.devin = h.devinIDs()

	if err := exec.Command("tmux", "-L", h.socket, "kill-server").Run(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second) // wrapper records process_exited on SIGHUP

	line := h.waitStatus(s, "exited", 10*time.Second)
	if !strings.Contains(line, "process_exited") {
		t.Fatalf("wrapper should have recorded process_exited on SIGHUP: %s", line)
	}
	out := h.must("doctor")
	if !strings.Contains(out, "dmux resume "+s) {
		t.Fatalf("doctor should suggest resume:\n%s", out)
	}
	h.must("resume", s, "-t", "What word did you reply with earlier? Just the word.")
	h.waitStatus(s, "idle", 60*time.Second)
	if ls := h.must("ls", "-v"); !strings.Contains(ls, "» CHARLIE") {
		t.Fatalf("resume after server restart should recall CHARLIE:\n%s", ls)
	}
	h.must("kill", s)
	h.must("workspace", "rm", "w", "--yes", "--delete-branches")
}

// TestOneStepNewAndJoin: `dmux new` creates workspace + session; `--in`
// shares worktrees explicitly; owner removal is refused while shared.
func TestOneStepNewAndJoin(t *testing.T) {
	h := newHarness(t)
	api := h.repo("api")
	out := h.must("new", "w", "--repo", api, "-t", "Reply with exactly DELTA and nothing else. Do not use tools.")
	if !strings.Contains(out, "Created workspace w") || !strings.Contains(out, "Spawned session") {
		t.Fatalf("new:\n%s", out)
	}
	owner := h.sessionID(out[strings.Index(out, "Spawned session"):])
	h.waitStatus(owner, "idle", 60*time.Second)
	out = h.must("spawn", "w", "-t", "Reply with exactly ECHO and nothing else. Do not use tools.", "--in", "delta")
	joiner := h.sessionID(out)
	if !strings.Contains(out, "shares worktrees with "+owner) {
		t.Fatalf("join:\n%s", out)
	}
	h.waitStatus(joiner, "idle", 60*time.Second)
	h.devin = h.devinIDs()
	if wt, _ := exec.Command("git", "-C", api, "worktree", "list").Output(); strings.Count(strings.TrimSpace(string(wt)), "\n") != 1 {
		t.Fatalf("join must not add a worktree:\n%s", wt)
	}
	if ls := h.must("ls"); !strings.Contains(ls, "↳ "+owner) {
		t.Fatalf("ls should mark the joiner:\n%s", ls)
	}
	h.mustFail("share these worktrees", "rm", owner, "--stop", "--yes")
	h.must("rm", joiner, "--stop", "--yes")
	h.must("rm", owner, "--stop", "--yes", "--delete-branches")
	h.must("workspace", "rm", "w", "--yes")
}

// TestConcurrentSpawns exercises the state lock: several spawns at once
// must all be recorded. Uses a shell shim instead of Devin to avoid cost.
func TestConcurrentSpawns(t *testing.T) {
	h := newHarness(t)
	api := h.repo("api")
	h.must("workspace", "new", "w", "--repo", api)
	shimDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(shimDir, "devin"), []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const n = 6
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			cmd := exec.Command(h.bin, "spawn", "w", "-t", fmt.Sprintf("task %d", i))
			cmd.Env = append(os.Environ(), "DMUX_ROOT="+h.root, "DMUX_TMUX_SOCKET="+h.socket, "PATH="+shimDir+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("%v: %s", err, out)
			}
			errs <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := len(h.state()["sessions"].([]any)); got != n {
		t.Fatalf("lost sessions under concurrency: %d/%d", got, n)
	}
	wins, _ := exec.Command("tmux", "-L", h.socket, "list-windows", "-a", "-F", "#{@dmux_session}").Output()
	if got := strings.Count(strings.TrimSpace(string(wins)), "\n") + 1; got != n {
		t.Fatalf("windows: %d/%d\n%s", got, n, wins)
	}
	h.must("workspace", "rm", "w", "--stop", "--yes", "--delete-branches")
}
