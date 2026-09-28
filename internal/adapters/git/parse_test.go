package git

import (
	"reflect"
	"testing"

	"github.com/bradhu25/devin-mux/internal/core"
)

// Fixture captured from git 2.39 on 2026-09-28 (see PLAN.md Spike 1/M1).
const porcelainFixture = `worktree /tmp/x/r
HEAD 71db6031190f44006889fdf26e65ae718f2fa235
branch refs/heads/main

worktree /tmp/x/wt1
HEAD 71db6031190f44006889fdf26e65ae718f2fa235
branch refs/heads/feat
locked keep me

worktree /tmp/x/wt2
HEAD 71db6031190f44006889fdf26e65ae718f2fa235
detached
`

func TestParseWorktreeList(t *testing.T) {
	got := parseWorktreeList(porcelainFixture)
	want := []core.Worktree{
		{Path: "/tmp/x/r", Head: "71db6031190f44006889fdf26e65ae718f2fa235", Branch: "main"},
		{Path: "/tmp/x/wt1", Head: "71db6031190f44006889fdf26e65ae718f2fa235", Branch: "feat", Locked: true, LockMsg: "keep me"},
		{Path: "/tmp/x/wt2", Head: "71db6031190f44006889fdf26e65ae718f2fa235", Detached: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parse mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestParseWorktreeList_EdgeCases(t *testing.T) {
	if got := parseWorktreeList(""); len(got) != 0 {
		t.Fatalf("empty input -> %d entries", len(got))
	}
	// No trailing newline, lock without reason, path with spaces.
	got := parseWorktreeList("worktree /tmp/with space/wt\nHEAD abc\nbranch refs/heads/x\nlocked")
	if len(got) != 1 || got[0].Path != "/tmp/with space/wt" || !got[0].Locked || got[0].LockMsg != "" {
		t.Fatalf("unexpected: %+v", got)
	}
}
