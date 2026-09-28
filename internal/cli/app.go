package cli

import (
	"path/filepath"
	"sync"

	gitadapter "github.com/bradhu25/devin-mux/internal/adapters/git"
	"github.com/bradhu25/devin-mux/internal/core"
	"github.com/bradhu25/devin-mux/internal/state"
)

// app wires adapters into core services. It is constructed lazily on first
// use so that commands which need no state (e.g. --version, hook-event) do
// not touch ~/.devin-mux.
type app struct {
	once       sync.Once
	err        error
	store      *state.Store
	git        core.Git
	workspaces *core.WorkspaceManager
}

func (a *app) init() error {
	a.once.Do(func() {
		st, err := state.OpenDefault()
		if err != nil {
			a.err = err
			return
		}
		a.store = st
		a.git = &gitadapter.Adapter{}
		a.workspaces = &core.WorkspaceManager{
			Git:            a.git,
			Store:          st,
			WorkspacesRoot: filepath.Join(st.Dir(), "workspaces"),
		}
	})
	return a.err
}
