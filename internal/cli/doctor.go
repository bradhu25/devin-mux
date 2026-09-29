package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/core"
	"github.com/bradhu25/devin-mux/internal/hooks"
	"github.com/bradhu25/devin-mux/internal/state"
)

func newDoctorCmd(a *app) *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check dmux's state, tools, hooks, worktrees, and tmux windows for problems",
		Long: `Inspect everything dmux depends on and report inconsistencies: missing tools,
uninstalled hooks, workspaces stuck mid-operation, worktrees that git no longer
knows about, directories no record references, and sessions whose windows are
gone.

With --fix, repairs that are safe by construction are applied: finishing the
rollback of a stuck workspace whose worktrees are clean, closing held-open
windows of exited sessions, and dropping records that point at nothing. Doctor
never deletes uncommitted work, unmerged branches, or resumable sessions; for
those it tells you the explicit command.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			d := &core.Doctor{
				Store: a.store, Git: a.git, Tmux: a.tmux, Devin: a.devin, Events: state.EventLog{},
				WorkspacesRoot: filepath.Join(a.store.Dir(), "workspaces"),
				HooksInstalled: func() (bool, error) { return hooksInstalled(a.sessions.DmuxBin) },
			}
			fs, err := d.Run(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprint(out, core.Summarize(fs))
			if !fix {
				if n := countFixable(fs); n > 0 {
					fmt.Fprintf(out, "\n%d finding(s) can be repaired with `dmux doctor --fix`.\n", n)
				}
				return nil
			}
			var failed int
			for _, f := range fs {
				if f.Fix == nil {
					continue
				}
				if err := f.Fix(cmd.Context()); err != nil {
					failed++
					fmt.Fprintf(out, "fix failed for %s: %v\n", f.Subject, err)
					continue
				}
				fmt.Fprintf(out, "fixed %s: %s\n", f.Subject, f.FixDesc)
			}
			if failed > 0 {
				return fmt.Errorf("%d fix(es) failed", failed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "apply safe repairs")
	return cmd
}

func countFixable(fs []core.Finding) int {
	n := 0
	for _, f := range fs {
		if f.Fix != nil {
			n++
		}
	}
	return n
}

// hooksInstalled reports whether Devin's user config has dmux's hook for
// every event, pointing at this binary.
func hooksInstalled(dmuxBin string) (bool, error) {
	path, err := hooks.DefaultConfigPath()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var cfg struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string } `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, err
	}
	want := hooks.Command(dmuxBin)
	for _, ev := range hooks.Events {
		found := false
		for _, e := range cfg.Hooks[ev] {
			for _, h := range e.Hooks {
				if strings.TrimSpace(h.Command) == want {
					found = true
				}
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}
