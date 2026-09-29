package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bradhu25/devin-mux/internal/hooks"
)

func newInitCmd(a *app) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install dmux's Devin hooks so session status can be observed",
		Long: `Register dmux's status hook in Devin CLI's user-level config
(~/.config/devin/config.json). The hook is passive: it records lifecycle events
for sessions started by dmux and ignores every other Devin session. Existing
configuration and other hooks are preserved; a timestamped backup is written
before any change. Safe to run repeatedly (also after moving the dmux binary).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.init(); err != nil {
				return err
			}
			path := configPath
			if path == "" {
				p, err := hooks.DefaultConfigPath()
				if err != nil {
					return err
				}
				path = p
			}
			res, backup, err := hooks.Install(path, a.sessions.DmuxBin)
			if err != nil {
				return fmt.Errorf("install hooks into %s: %w", path, err)
			}
			out := cmd.OutOrStdout()
			switch {
			case !res.Changed():
				fmt.Fprintf(out, "Hooks already installed in %s (%d events).\n", path, len(res.Kept))
			default:
				fmt.Fprintf(out, "Installed dmux hooks in %s\n", path)
				if len(res.Added) > 0 {
					fmt.Fprintf(out, "  added:   %s\n", strings.Join(res.Added, ", "))
				}
				if len(res.Updated) > 0 {
					fmt.Fprintf(out, "  updated: %s (binary path changed)\n", strings.Join(res.Updated, ", "))
				}
				if backup != "" {
					fmt.Fprintf(out, "  backup:  %s\n", backup)
				}
			}
			fmt.Fprintf(out, "  command: %s\n  state:   %s\n", hooks.Command(a.sessions.DmuxBin), a.store.Dir())
			if !filepath.IsAbs(a.sessions.DmuxBin) || strings.Contains(a.sessions.DmuxBin, string(os.PathSeparator)+"bin"+string(os.PathSeparator)+"dmux") && strings.Contains(a.sessions.DmuxBin, "devin-mux") {
				fmt.Fprintf(out, "  note: hooks point at this binary's current location; re-run `dmux init` if you move or reinstall it\n")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "Devin config file to modify (default: ~/.config/devin/config.json)")
	return cmd
}
