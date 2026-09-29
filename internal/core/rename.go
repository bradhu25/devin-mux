package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// RenameResult reports what Rename changed.
type RenameResult struct {
	Workspace   Workspace
	OldName     string
	TmuxRenamed bool
	Warnings    []string
}

// Rename changes a workspace's display name. Because records reference the
// workspace by id, nothing else needs to change: the directory keeps its
// original name (worktreePath is recorded explicitly) and branches keep
// theirs. The workspace AGENTS.md map is regenerated and, if a tmux session
// exists for it, that session is renamed too so `dmux-<name>` stays
// recognizable. Both of those are best effort.
func (m *WorkspaceManager) Rename(ctx context.Context, tmux Tmux, query, newName string) (*RenameResult, error) {
	if err := ValidateName(newName); err != nil {
		return nil, err
	}
	var res RenameResult
	err := m.Store.Update(ctx, func(st *State) error {
		ws := st.WorkspaceByName(query)
		if ws == nil {
			ws = st.Workspace(query)
		}
		if ws == nil {
			return fmt.Errorf("%w: %s", ErrWorkspaceNotFound, query)
		}
		if other := st.WorkspaceByName(newName); other != nil && other.ID != ws.ID {
			return fmt.Errorf("%w: %s", ErrWorkspaceExists, newName)
		}
		res.OldName = ws.Name
		ws.Name = newName
		for i := range st.Sessions {
			if st.Sessions[i].WorkspaceID == ws.ID && st.Sessions[i].Tmux.SessionName != "" {
				st.Sessions[i].Tmux.SessionName = "dmux-" + newName
			}
		}
		res.Workspace = *ws
		return nil
	})
	if err != nil {
		return nil, err
	}
	if res.OldName == newName {
		return &res, nil
	}

	if err := os.WriteFile(filepath.Join(res.Workspace.Root, "AGENTS.md"), []byte(WorkspaceAgentsMD(&res.Workspace)), 0o644); err != nil {
		res.Warnings = append(res.Warnings, "could not regenerate AGENTS.md: "+err.Error())
	}
	if tmux != nil {
		if wins, err := tmux.ListWindows(ctx); err == nil {
			for _, w := range wins {
				if w.WorkspaceID == res.Workspace.ID {
					if err := tmux.RenameSession(ctx, w.SessionID, "dmux-"+newName); err != nil {
						res.Warnings = append(res.Warnings, "tmux session not renamed: "+err.Error())
					} else {
						res.TmuxRenamed = true
					}
					break
				}
			}
		}
	}
	return &res, nil
}
