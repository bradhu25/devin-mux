package devin

import (
	"context"
	"reflect"
	"testing"

	"github.com/bradhu25/devin-mux/internal/core"
)

func TestLaunchArgs(t *testing.T) {
	a := &Adapter{}
	cases := []struct {
		name string
		spec core.LaunchSpec
		want []string
	}{
		{"bare", core.LaunchSpec{}, []string{"devin", "--respect-workspace-trust", "false"}},
		{"prompt", core.LaunchSpec{Prompt: "fix the auth bug"}, []string{"devin", "--respect-workspace-trust", "false", "--", "fix the auth bug"}},
		{"resume+prompt", core.LaunchSpec{ResumeID: "olive-turkey", Prompt: "continue"}, []string{"devin", "--respect-workspace-trust", "false", "-r", "olive-turkey", "--", "continue"}},
		{"all", core.LaunchSpec{PermissionMode: "accept-edits", Model: "opus", ResumeID: "x", Prompt: "p"},
			[]string{"devin", "--respect-workspace-trust", "false", "--permission-mode", "accept-edits", "--model", "opus", "-r", "x", "--", "p"}},
	}
	for _, tc := range cases {
		if got := a.LaunchArgs(tc.spec); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
	if got := (&Adapter{Bin: "/opt/devin"}).LaunchArgs(core.LaunchSpec{}); got[0] != "/opt/devin" {
		t.Fatalf("custom bin not used: %q", got)
	}
}

func TestAvailable(t *testing.T) {
	if err := (&Adapter{Bin: "/nonexistent/devin"}).Available(context.Background()); err == nil {
		t.Fatal("missing binary must error")
	}
}
