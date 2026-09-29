package core

import (
	"encoding/json"
	"time"
)

// v1 schema: workspaces owned the worktrees; sessions were conversations in
// the workspace root. Kept only for MigrateV1.
type v1State struct {
	Workspaces []v1Workspace `json:"workspaces"`
	Sessions   []Session     `json:"sessions"`
	KnownRepos []string      `json:"knownRepos"`
}

type v1Workspace struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Root      string          `json:"root"`
	Repos     []WorkspaceRepo `json:"repos"`
	Status    WorkspaceStatus `json:"status"`
	CreatedAt time.Time       `json:"createdAt"`
}

// MigrateV1 converts a v1 state document to the current schema without side
// effects on disk or git. The workspace's worktree set is handed to its
// oldest session as owner (Root, Repos); every other session of that
// workspace becomes a joiner (SharedWith = owner), which is exactly the
// situation those sessions were already in. A workspace with worktrees but
// no sessions gets a placeholder owner session so the worktrees stay
// visible and removable with `dmux rm`.
func MigrateV1(data []byte) (*State, error) {
	var old v1State
	if err := json.Unmarshal(data, &old); err != nil {
		return nil, err
	}
	st := &State{Version: StateVersion, KnownRepos: old.KnownRepos}
	sessions := old.Sessions
	for _, w := range old.Workspaces {
		ws := Workspace{ID: w.ID, Name: w.Name, Root: w.Root, Status: w.Status, CreatedAt: w.CreatedAt}
		if ws.Status == WorkspaceCreating || ws.Status == WorkspaceFailed {
			ws.Status = WorkspaceReady // the worktree saga now lives on the session, which inherits the status below
		}
		for _, r := range w.Repos {
			ws.Repos = append(ws.Repos, RepoRef{Name: r.Name, SourcePath: r.SourcePath, BaseRef: "HEAD"})
		}
		st.Workspaces = append(st.Workspaces, ws)

		var mine []int
		for i := range sessions {
			if sessions[i].WorkspaceID == w.ID {
				mine = append(mine, i)
			}
		}
		if len(w.Repos) == 0 {
			continue
		}
		if len(mine) == 0 {
			sessions = append(sessions, Session{
				ID: NewUniqueID(NewSessionID, func(id string) bool {
					for _, s := range sessions {
						if s.ID == id {
							return true
						}
					}
					return false
				}),
				WorkspaceID: w.ID, Task: "(worktrees migrated from workspace " + w.Name + ")",
				Root: w.Root, Repos: w.Repos, Status: w.Status, CreatedAt: w.CreatedAt,
			})
			continue
		}
		owner := mine[0]
		for _, i := range mine[1:] {
			if sessions[i].CreatedAt.Before(sessions[owner].CreatedAt) {
				owner = i
			}
		}
		for _, i := range mine {
			sessions[i].Root, sessions[i].Repos = w.Root, w.Repos
			sessions[i].Status = w.Status
			if i != owner {
				sessions[i].SharedWith = sessions[owner].ID
			}
		}
	}
	for i := range sessions {
		if sessions[i].Status == "" {
			sessions[i].Status = WorkspaceReady
		}
	}
	st.Sessions = sessions
	return st, nil
}
