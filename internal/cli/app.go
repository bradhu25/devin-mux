package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	devinadapter "github.com/bradhu25/devin-mux/internal/adapters/devin"
	gitadapter "github.com/bradhu25/devin-mux/internal/adapters/git"
	procadapter "github.com/bradhu25/devin-mux/internal/adapters/proc"
	tmuxadapter "github.com/bradhu25/devin-mux/internal/adapters/tmux"
	"github.com/bradhu25/devin-mux/internal/core"
	"github.com/bradhu25/devin-mux/internal/state"
)

// EnvTmuxSocket, if set, makes dmux use an isolated tmux server (-L). Meant
// for tests and demos; normal use shares the user's default server so
// switch-client works.
const EnvTmuxSocket = "DMUX_TMUX_SOCKET"

// app wires adapters into core services. It is constructed lazily on first
// use so that commands which need no state (e.g. --version, hook-event) do
// not touch ~/.devin-mux.
type app struct {
	once       sync.Once
	err        error
	store      *state.Store
	git        core.Git
	tmux       core.Tmux
	devin      core.Devin
	workspaces *core.WorkspaceManager
	sessions   *core.SessionManager
	reconciler *core.Reconciler
}

func (a *app) init() error {
	a.once.Do(func() {
		st, err := state.OpenDefault()
		if err != nil {
			a.err = err
			return
		}
		self, err := os.Executable()
		if err != nil {
			a.err = fmt.Errorf("locate dmux binary: %w", err)
			return
		}
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			self = resolved
		}
		a.store = st
		a.git = &gitadapter.Adapter{}
		a.tmux = &tmuxadapter.Adapter{Socket: os.Getenv(EnvTmuxSocket)}
		a.devin = &devinadapter.Adapter{}
		a.workspaces = &core.WorkspaceManager{
			Git:            a.git,
			Store:          st,
			WorkspacesRoot: filepath.Join(st.Dir(), "workspaces"),
		}
		a.sessions = &core.SessionManager{
			Tmux:    a.tmux,
			Devin:   a.devin,
			Store:   st,
			Proc:    procadapter.Adapter{},
			Events:  state.EventLog{},
			DmuxBin: self,
		}
		a.reconciler = &core.Reconciler{
			Store:  st,
			Events: state.EventLog{},
			Tmux:   a.tmux,
			Devin:  a.devin,
		}
	})
	return a.err
}
