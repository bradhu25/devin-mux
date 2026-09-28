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
      appends JSON lines to `~/.devin-mux/events/`; confirm orchestrator can
      tail it for live status and measure hook invocation latency. Also
      verify: (a) ~~`DMUX_SESSION_ID` env inherited by hooks~~ VERIFIED in
      Spike 4; (b) which hooks fire after a
      PermissionRequest is **approved**, **denied**, and **cancelled** — this
      determines how approval_resolved is inferred; (c) a hook exiting 0 with
      no stdout leaves the permission flow untouched; (d) ~~`Stop` per turn,
      `SessionEnd` on exit~~ VERIFIED in Spike 4
- [x] Spike 4 (critical): **specific-conversation resumption.** VERIFIED
      2026-09-27 on Devin CLI 3000.11.3 (non-interactive `-p` mode; re-check
      interactive in Spike 1):
      - (a) PASS — with two sessions in one dir, `devin -r olive-turkey`
        restored the older, non-most-recent conversation correctly.
      - (b) PASS — hook stdin `session_id` (`olive-turkey`) is exactly the id
        `devin -r` accepts. IDs are `adjective-noun` slugs.
      - (c) PASS with caveat — resume works from any cwd, BUT **rebinds the
        session's `working_directory` to the new cwd** (observed in
        sessions.db). `dmux resume` MUST launch from the workspace dir.
      - (d) PASS — `SessionStart` payload: `{"source":"startup"|"resume",
        "session_id":...}`. Resume is distinguishable.
      - Bonus: `DMUX_SESSION_ID` env on the `devin` process IS inherited by
        hook subprocesses (Spike 3a done). `Stop` payload includes
        `last_assistant_message` + `prompt_id`; `SessionEnd` includes
        `reason`. Both fired per turn / per exit as expected (Spike 3d done).
      - Devin's local store: SQLite at `~/.local/share/devin/cli/sessions.db`,
        table `sessions(id, working_directory, workspace_dirs, title,
        last_activity_at, hidden, ...)`. Read-only access is a viable
        secondary source for reconciliation (e.g. surfacing Devin's
        auto-generated `title`). Never write to it.

### Resumption semantics (design constraint)

Two distinct operations that must never be conflated:

| Case | Process state | Correct action |
| --- | --- | --- |
| tmux reattachment | Devin process alive, user navigated away | `dmux jump` — switch to the existing window |
| Conversation resumption | Devin process exited; conversation history persists | `dmux resume <session>` — relaunch `devin -r <devinSessionId>` in the workspace |

`dmux resume` must restore the recorded conversation, never silently start a
new one. Because a workspace can host multiple sessions, directory-based
resume (`devin -c`) is **never** a valid resume primitive for dmux —
`devinSessionId` is load-bearing. If it's missing for an exited session,
`dmux resume` must say so rather than fall back to `-c`.

### Live status architecture (design constraint)

dmux and Devin are separate processes; dmux needs an *observation* pipeline,
not introspection. Hooks report facts; dmux interprets them.

```
Devin process ──hook──> dmux hook-event ──append──> ~/.devin-mux/events/<dmuxSessionId>.jsonl
                                                            │
dmux run (launch wrapper, Devin's parent) ──process_exited──┤
                                                            ▼
                                            reducer (FSM) ──> derived Session state
                                                            │
                                            reconciler (+ tmux/process liveness)
                                                            ▼
                                                      dmux ls / dmux ui
```

**Normalized event contract** (independent of Devin's raw hook payload — the
Phase 3 ACP adapter emits the same events, so UI/reducer never change):

```go
type EventType string
const (
    SessionStarted    EventType = "session_started"
    PromptSubmitted   EventType = "prompt_submitted"
    ToolStarted       EventType = "tool_started"
    ToolCompleted     EventType = "tool_completed"
    ApprovalRequested EventType = "approval_requested"
    ApprovalResolved  EventType = "approval_resolved" // inferred, see below
    TurnCompleted     EventType = "turn_completed"
    SessionEnded      EventType = "session_ended"
    ProcessExited     EventType = "process_exited"    // from launch wrapper
)

type SessionEvent struct {
    EventID        string    `json:"eventId"`
    SessionID      string    `json:"sessionId"`      // dmux session id
    DevinSessionID string    `json:"devinSessionId,omitempty"`
    Type           EventType `json:"type"`
    Timestamp      time.Time `json:"timestamp"`
    Data           map[string]any `json:"data,omitempty"` // tool name, exit code, source...
}
```

Hook → event mapping: `SessionStart`→session_started, `UserPromptSubmit`→
prompt_submitted, `PreToolUse`→tool_started, `PostToolUse`→tool_completed,
`PermissionRequest`→approval_requested, `Stop`→turn_completed,
`SessionEnd`→session_ended. **There is no hook for "permission resolved"**:
approval_resolved is inferred by the reducer from the next tool_started /
tool_completed / turn_completed after an approval_requested.

**Reducer (FSM)**: session_started/prompt_submitted/tool_started → running +
working; approval_requested → awaiting-approval; turn_completed → idle;
session_ended → exited + unknown; process_exited → exited (or failed if
nonzero) + unknown.

Rules:
- **`Stop` is NOT `SessionEnd`.** Stop = end of a turn → idle. SessionEnd →
  exited. Conflating them marks live conversations dead.
- **Passive observer.** The `PermissionRequest` hook exits 0 with no
  `decision` output — it must never approve/deny/alter the permission flow.
  Hook failure must never interrupt the agent (swallow errors, exit 0).
- **Correlation via env, not cwd.** `dmux spawn` sets `DMUX_SESSION_ID`
  on the Devin process; the hook reads it (cwd is ambiguous when N sessions
  share a workspace). Also record Devin's `session_id` from the payload to
  maintain the dmux↔Devin id mapping. Ignore events with no known dmux id.
- **Launch wrapper owns exit detection.** tmux window runs
  `dmux run --session <id> -- devin ...`; as Devin's parent it always emits
  process_exited (incl. crashes), independent of hooks firing.
- **Reconciler precedence** (last event + liveness): process/tmux window
  absent → exited/missing; alive + pending approval → awaiting-approval;
  alive + recent working event → working; alive + last turn_completed → idle;
  alive + undeterminable → **unknown**. A tmux window existing is not proof
  Devin is alive (could be the wrapper/shell) — check the wrapper's child.
- **Never infer idle from silence.** Long inference or long-running tests
  produce no events while working. Unknown is honest; false idle is not.

### Persistence (design constraint)

Filesystem-backed; no database in v0.1 (SQLite is the future replacement if
volume/query needs justify it — not before).

```
~/.devin-mux/
├── config.json          # static settings: workspaces root, naming prefs, tmux prefix
├── state.json           # dynamic: Workspace + Session records
├── state.lock           # flock target for read-modify-write transactions
├── events/
│   └── <dmuxSessionId>.jsonl   # per-session append-only event log
└── workspaces/
    └── <name>/<repo>/   # git worktrees; dir named at creation, NEVER moved on
                         # rename (worktreePath is recorded explicitly in state)
```

Rules:
- **Config ≠ state.** `config.json` is user-edited and rarely changes;
  `state.json` is machine-owned. Never mix.
- **Atomic state writes.** Write to a temp file *in the same directory*,
  fsync, `os.Rename` over `state.json`, fsync the directory. Readers see the
  old complete document or the new one, never a torn write.
- **Atomicity ≠ concurrency control.** Two concurrent `dmux spawn` calls
  doing read-modify-write would lose a record. All mutations go through
  `state.Update(ctx, func(*State) error)` which acquires an exclusive
  `flock` on `state.lock`, reads, modifies, atomically writes, releases.
  Use `flock` (via `gofrs/flock`), not a PID lockfile: the kernel releases
  it on process death, so **stale locks cannot occur**. Contention: block
  with a context timeout (e.g. 5s), then fail loudly.
- **Per-session event files, multiple short-lived appenders.** Writers are
  each hook invocation (separate process) plus the `dmux run` wrapper. Safe
  because every event is one JSONL line written with **`O_APPEND` in a
  single `write()`** — POSIX guarantees such appends don't interleave. Never
  buffer/split a line across writes. No lock needed on event files.
- **Event ordering** ≈ arrival order. Parallel `PreToolUse` hooks (batched
  tool calls) may land out of order; `timestamp` + `eventId` disambiguate.
  Reducer must tolerate this (e.g. tool_completed before tool_started).
- **Read path never locks.** `dmux ls`/`ui` read `state.json` (atomic
  snapshot) and tail event files without acquiring `state.lock`.
- **Derived state is not persisted.** lifecycle/activity are recomputed from
  events + liveness on read; `state.json` holds only facts dmux authored
  (ids, paths, tmux targets, devinSessionId, timestamps). Reset = delete
  events dir, never corrupts records.

### Navigation: `dmux jump` (design constraint)

One place to find every session. Three steps: reconcile → pick → navigate.

```
Select a session:

  feature-x
    ● auth implementation       working
    ! security review           awaiting approval

  payment-fix
    ○ validation tests          idle
```

Picker: `charmbracelet/huh` grouped select in Phase 1 (same Bubble Tea
runtime as the Phase 2 `dmux ui`). `dmux jump <query>` also accepts a
fuzzy/prefix match for non-interactive use.

**Inside tmux vs outside tmux are different operations** (detect via `$TMUX`):

| Context | Operation | Go implementation |
| --- | --- | --- |
| Inside tmux (`$TMUX` set) | Switch the *current client* to the target | `tmux switch-client -t <session>` then `tmux select-window -t @N` via `os/exec`; dmux exits normally |
| Outside tmux | Attach the terminal to the target session | `tmux select-window -t @N` then **`syscall.Exec`** `tmux attach-session -t <session>` — replace the dmux process so tmux owns the TTY and signals; never spawn-and-wait |

**Stable targets, not names.** Window names are user-renamable; store the
tmux window ID (`@N`) in `Session.tmux.windowId`. But `@N` is stable only for
the lifetime of the tmux *server* — after reboot/`kill-server`, IDs restart
and an unrelated window may reuse `@12`. So before acting:

1. Confirm the window exists and belongs to the expected managed session:
   `tmux list-windows -t <session> -F '#{window_id}'`.
2. **Positive identity via tmux user option**: at spawn, tag the window with
   `tmux set-option -w -t @N @dmux_session <dmuxSessionId>`; on jump,
   verify `tmux show-option -wv -t @N @dmux_session` matches. Membership +
   tag = safe.
3. If validation fails → the window is gone: update the record (lifecycle
   exited/missing), tell the user, and offer `dmux resume` if a
   `devinSessionId` is recorded. Never jump to a wrong window.

tmux layout: one tmux session per workspace (`dmux-<workspaceName>`,
session name also recorded — tmux sessions are renamable too, so record the
tmux session ID `$N` alongside the name), one window per Devin session.

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
  tmux: { sessionName: string; sessionId: string; windowId: string }; // $N / @N stable ids; names are display-only
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
- [ ] tmux adapter (session-per-workspace, window-per-Devin-session; inside
      tmux → switch-client/select-window, outside → `syscall.Exec` attach;
      `@dmux_session` window tag set at spawn and verified on jump)
- [ ] State store: `config.json`/`state.json` split, atomic temp+rename
      writes, `flock`-guarded `Update()` transaction, O_APPEND single-write
      JSONL event appender
- [ ] Status pipeline: `dmux hook-event` (hook writer) + `dmux run` (launch
      wrapper w/ process_exited) + JSONL event store + reducer + reconciler
- [ ] Hooks plugin (`hooks.json`) shipping the `dmux hook-event` bindings
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
