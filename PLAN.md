# PLAN — devin-mux

Workflow plan for building and shipping the worktree-session Devin orchestrator.

## Before / After — what exists today vs. what we're building

### Today (no orchestrator)

Running multiple Devin CLI sessions in parallel is possible but entirely
manual, and each pain point compounds with session count:

- **Manual isolation.** All sessions launched in the same checkout share files
  and a git branch — parallel agents stomp each other. Users must know to run
  `git worktree add` themselves, invent a directory layout, and clean up
  worktrees/branches by hand afterwards.
- **Manual process management.** Each session is a `devin` process in its own
  terminal tab/pane the user creates and arranges themselves (raw tmux, or a
  pile of terminal windows). Nothing groups sessions by task or repo set.
- **No cross-session visibility.** No way to see at a glance which sessions
  are working, idle, or blocked waiting on a permission prompt. Users
  alt-tab through terminals polling for the one that needs attention.
- **Jumping is manual.** Finding "the session doing the auth refactor" means
  remembering which tab it's in. Resume (`devin -c`) is per-directory, so it
  works only if you remember which directory maps to which task.
- **Multi-repo tasks are awkward.** A task spanning repos requires manually
  creating a worktree per repo, then launching with `--add-dir` for each.
- **Prior art doesn't cover Devin.** Claude Squad / Conductor / Crystal /
  Vibe Kanban solve this for Claude Code, mostly single-repo; nothing targets
  Devin CLI.

### After (devin-mux v0.1)

One tool owns the worktree-session lifecycle end to end:

- **One-command workspaces.** `dmux worktree new feature-x --repo api --repo web`
  creates a named worktree set (git worktrees for up to N repos under a
  managed root) — isolation by default, cleanup with `dmux worktree rm`.
- **One-command sessions.** `dmux spawn feature-x -t "fix auth"` launches a
  Devin session in tmux, scoped to that worktree, multi-repo dirs wired up
  via `--add-dir` automatically.
- **Live status board.** `dmux ls` (and later `dmux ui`) shows every
  worktree → session with hook-driven status: working / idle /
  awaiting-approval. The "which session needs me?" question has an answer.
- **Instant jumping.** `dmux jump` opens a picker across all live sessions
  and switches the tmux client straight to the chosen one.
- **Everything concurrent.** All sessions keep running while you're elsewhere;
  worktree isolation makes parallelism safe rather than risky.

### Phase 2+ upside (beyond v0.1)

- ACP-based dashboard: unified inbox of permission prompts across sessions,
  custom multi-session UI not bound to tmux.
- Mixed local/cloud board via `devin --cloud` and `/handoff` — scale-out
  story none of the Claude-Code-era tools have.

## Phase 0 — Scaffold & spike (validate riskiest assumptions first)

- [ ] Scaffold Go module (`go mod init`, `cmd/dmux/main.go`, cobra root
      command, `internal/` packages: `state`, `gitwt`, `tmux`, `devin`,
      `hooks`), golangci-lint, Makefile
- [ ] Spike 1: create a git worktree programmatically + launch `devin` in it
      inside a tmux window; confirm session history binds to worktree dir
      (`devin -c` resumes correctly per worktree)
- [ ] Spike 2: multi-repo worktree — two repos' worktrees under one folder,
      launch `devin --add-dir`; confirm workspace spans both
- [ ] Spike 3: status hook — plugin `hooks.json` (SessionStart/Stop/
      PermissionRequest/PostToolUse) invoking `dmux hook-event`, which
      appends JSON lines to `~/.devin-mux/state/`; confirm orchestrator can
      tail it for live status and measure hook invocation latency

## Phase 1 — Core CLI (MVP)

Data model (persisted as JSON under `~/.devin-mux/`). Design principles:
**stable logical IDs** distinct from display names (renames don't cascade;
tmux referenced by stable window ID, not window name), physical paths recorded
explicitly, and **lifecycle (process) separated from activity (agent state)**
— a session can be alive-but-idle, alive-but-blocked-on-approval, or dead
with a resumable `devinSessionId`. Keeping these orthogonal is what makes
recovery/resumption tractable.

```ts
type Workspace = {
  id: string;            // stable, e.g. ws_a91c
  name: string;          // display name, freely renamable
  repos: WorkspaceRepo[];
  status: "creating" | "ready" | "deleting";
  createdAt: string;
};

type WorkspaceRepo = {
  repoId: string;
  sourcePath: string;    // original checkout (worktree ops run against it)
  worktreePath: string;  // physical path under the managed root
  branch: string;
  baseRef: string;       // for future merge-back/PR helpers
};

type Session = {
  id: string;
  workspaceId: string;   // stable reference, not the name
  task: string;
  tmux: { sessionName: string; windowId: string }; // windowId is the stable @N id
  devinSessionId?: string;  // enables `devin -r` resumption after exit
  lifecycle: "starting" | "running" | "exited" | "failed";
  activity: "working" | "idle" | "awaiting-approval" | "unknown";
  createdAt: string;
  lastEventAt?: string;
};
```

Lifecycle × activity display matrix: starting/unknown = initializing;
running/working = executing; running/idle = waiting for task;
running/awaiting-approval = user action required; exited|failed/unknown =
process gone (resumable if `devinSessionId` recorded). Collapse for display,
never in storage.

Commands:
- [ ] `dmux workspace new <name> --repo <path>[@branch] ...` — create workspace
      (git worktree per repo under the managed root)
- [ ] `dmux workspace list / rm / rename` — list, clean up (git worktree
      remove + prune), rename (display name only; ID stable)
- [ ] `dmux spawn <workspace> [-t "task prompt"]` — new Devin session in tmux
- [ ] `dmux jump` — interactive picker (workspace → session) that switches
      tmux client to the chosen session; show status badges
- [ ] `dmux ls` — table of all workspaces/sessions with lifecycle + activity
- [ ] `dmux kill <session>` — end a session (tmux + record)
- [ ] `dmux resume <session>` — relaunch an exited session via `devin -r
      <devinSessionId>` in its workspace

Cross-cutting:
- [ ] tmux adapter (session-per-workspace, window-per-Devin-session naming
      scheme, attach/switch logic for inside vs outside tmux)
- [ ] Status via hooks plugin (working / idle / awaiting-approval)
- [ ] Graceful degradation when tmux/devin missing; doctor command

## Phase 2 — Polish & ship v0.1

- [ ] `dmux ui` — full-screen Bubble Tea TUI: tree of workspaces/sessions,
      status badges, one-key jump
- [ ] Docs: README with demo GIF, install (Homebrew tap + `go install` +
      release binaries), quickstart
- [ ] Tests: `go test` unit (state, git worktree ops, tmux adapter with fake
      exec) + smoke script
- [ ] goreleaser: cross-platform binaries (darwin/linux, amd64/arm64), tag
      v0.1.0

## Phase 3 — Later (validated by usage)

- ACP dashboard: spawn `devin acp` per session, custom UI, unified
  permission inbox
- Cloud session integration (`/handoff`, `devin --cloud`) on the same board
- Worktree lifecycle niceties: auto-branch naming, PR creation, merge-back
  helpers, stale worktree GC

## Shipping principles

- Spike before building: each phase-0 spike de-risks a core assumption
- Keep every milestone demoable end-to-end
- Update AGENTS.md when conceptual decisions change; keep this file current
