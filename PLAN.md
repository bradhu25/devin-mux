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
  creating a worktree per repo, then `/add-dir` for each inside the session
  (there is no CLI flag for it).
- **Prior art doesn't cover Devin.** Claude Squad / Conductor / Crystal /
  Vibe Kanban solve this for Claude Code, mostly single-repo; nothing targets
  Devin CLI.

### After (devin-mux v0.1)

One tool owns the worktree-session lifecycle end to end:

- **One-command workspaces.** `dmux workspace new feature-x --repo api --repo web`
  creates a named workspace (git worktrees for up to N repos under a
  managed root) — isolation by default, cleanup with `dmux workspace rm`.
- **One-command sessions.** `dmux spawn feature-x -t "fix auth"` launches a
  Devin session in tmux, scoped to that workspace, with all repo worktrees
  in scope automatically.
- **Live status board.** `dmux ls` (and later `dmux ui`) shows every
  workspace → session with hook-driven status: working / idle /
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

## Architectural decisions (resolved before implementation)

**Guiding principle — four identities, four lifetimes.** A workspace, a tmux
window, an OS process, and a Devin conversation are four different things
with different identifiers and lifetimes:

| Thing | Identifier | Lifetime | Owner |
| --- | --- | --- | --- |
| Workspace | `ws_*` (dmux) | until `dmux workspace rm` | dmux |
| tmux session/window | `$N` / `@N` + `@dmux_*` tags | tmux server lifetime | tmux |
| OS process | PID (observed by `dmux run` wrapper) | until exit | OS |
| Devin conversation | `devinSessionId` (slug) | until `devin rm` | Devin |

Mapping them correctly is the foundation of navigation, termination, and
resumption. Never let one stand in for another.

| # | Decision | Resolution |
| --- | --- | --- |
| 1 | Multiple active agents per workspace? | **Permitted.** Isolation is **workspace-level, not session-level** — two sessions in one workspace share files and a branch. Documented prominently; `dmux spawn` prints a one-line notice when a workspace already has a running session. Recommended pattern: one workspace per task, sessions within it for sub-tasks that the user knows don't conflict. |
| 2 | Branch naming and ownership | `WorkspaceRepo.createdBranch` records provenance. Default branch name `dmux/<workspace-name>`; `--repo path@branch` uses an existing branch (createdBranch=false). Only dmux-created branches are ever deletable, only via `-d`. |
| 3 | What identifies a resumable conversation? | Native `devinSessionId`, captured from hook `session_id` — **verified** identical to what `devin -r` accepts. Directory-based resume is never used. |
| 4 | Source of truth for status | None is unquestionable. Hook events = observations; tmux/process checks = liveness evidence; `process_exited` from the wrapper = authoritative for exit. Persisted status is a derived snapshot recomputed on read. |
| 5 | How hooks get installed | User-level `~/.config/devin/config.json` `"hooks"` via `dmux init`, idempotent JSON merge that never overwrites existing user hooks (append to arrays, dedupe by command). Plugin route as alternative. Confirmed by Spike 3. |
| 6 | Partial success | Sagas with transitional states (`creating`/`deleting`/`failed`) and reverse compensation; `dmux doctor` recovers after interruption. |
| 7 | One tmux namespace? | **Same tmux server as the user** (a separate `-L` socket would break `switch-client` across servers). Managed sessions are named `dmux-<workspace>` AND tagged (`@dmux_workspace=<id>` on the session, `@dmux_session=<id>` on windows). dmux only ever acts on targets carrying its tags; name prefix alone is never sufficient. It never lists, modifies, or kills untagged user sessions. |

## Phase 0 — Scaffold & spike (validate riskiest assumptions first)

- [x] Scaffold Go module (2026-09-28: cobra root, `internal/` layout,
      Makefile, golangci-lint w/ depguard enforcing no `os/exec` outside
      adapters, `state.AppendEvent` O_APPEND writer + 16-writer race test,
      minimal `dmux hook-event` raw logger). Layout follows domain
      boundaries, not commands:

```
devin-mux/
├── go.mod
├── Makefile                  # build, test, lint, smoke
├── cmd/dmux/main.go          # entry; wires cobra + DI of adapters into core
├── internal/
│   ├── cli/                  # cobra commands (thin: parse → call core → render)
│   │   ├── root.go, workspace.go, spawn.go, jump.go, ls.go, kill.go,
│   │   ├── resume.go, doctor.go, run.go (launch wrapper), hookevent.go
│   ├── core/                 # orchestration logic; imports adapter INTERFACES only
│   │   ├── types.go          # Workspace, WorkspaceRepo, Session, SessionEvent
│   │   ├── workspace.go      # WorkspaceManager: create/rm sagas, rollback
│   │   ├── session.go        # SessionManager: spawn/kill/resume
│   │   ├── status.go         # reducer FSM + reconciler
│   │   └── ports.go          # Git, Tmux, Devin, Store, EventLog interfaces
│   ├── adapters/             # external tools; the ONLY place os/exec lives
│   │   ├── git/              # worktree add/remove/list --porcelain, status
│   │   ├── tmux/             # new-session/window, switch/attach, tags via @opts
│   │   └── devin/            # launch args, `devin list --format json`, sessions.db (RO)
│   ├── state/                # store.go (atomic write), lock.go (flock), events.go (O_APPEND)
│   ├── hooks/                # normalize.go: raw Devin payload → SessionEvent
│   └── ui/                   # huh picker (P1), Bubble Tea app (P2)
└── test/
    ├── integration/          # real git in temp repos; real tmux server (-L dmux-test)
    └── smoke/                # scripted end-to-end with real devin (manual/opt-in)
```

      Key boundary: `core` depends on interfaces in `ports.go`; `adapters`
      implement them. `core` never constructs a git/tmux command line.
      Testing strategy: unit-test `core` with fakes; test each adapter
      against the real tool in isolation (temp repos, isolated tmux socket);
      smoke-test with real Devin only opt-in (`make smoke`) — **never launch
      paid agent sessions in routine tests**.

Spikes answer a question decisively; "the command ran" is not success.

- [x] **Spike 1 — Worktree + session lifecycle.** *Can we reliably start,
      leave, return to, and resume a Devin session in a managed worktree?*
      **Answer: yes.** VERIFIED 2026-09-28, CLI 3000.11.3, tmux 3.7c,
      isolated server `tmux -L dmux-spike1`:
      - worktree created without modifying the original checkout — PASS
        (`git worktree add -b dmux/feature-x <path> HEAD`; source status
        clean, HEAD unchanged, still on `main`; `--porcelain` output parsed
        as expected: `worktree`/`HEAD`/`branch` stanzas)
      - Devin launches with cwd = the worktree — PASS (`DEVIN_PROJECT_DIR`
        in hook payload and tmux `#{pane_current_path}` both = worktree)
      - detaching does not terminate the agent — PASS (ran 40+ min with zero
        clients attached; no `SessionEnd`)
      - reattaching returns to the same live process — PASS (real terminal
        attach, prompt answered, detach; `pane_pid` unchanged)
      - `devin -r <id>` restores the specific conversation interactively —
        PASS (`SessionStart.source=resume`, history replayed in pane,
        answered from memory)
      - initial prompt via `devin -- "<prompt>"` — PASS, and it **composes
        with `-r`**: `devin -r <id> -- "<prompt>"` resumes and submits
      - `@dmux_session` window tag + `$N`/`@N` ids — PASS
      - not done: `devin list --format json` for two histories in one dir
        (already covered by Spike 4's sessions.db check; do in M2)
      Findings for M2 (`dmux run` wrapper / tmux adapter):
      - **Ctrl+D on empty input exits Devin** (`SessionEnd.reason=
        prompt_input_exit`). Attaching a tmux client with a dead stdin
        (e.g. `script`/non-tty automation) feeds EOF and kills the session.
        Tests must never fake-attach; only real terminals attach.
      - **Devin exit closes its tmux window; last window → session gone;
        last session → server gone.** The wrapper must hold the pane open
        after exit and print exit status so the tmux target doesn't vanish
        under the Session record (`remain-on-exit` or wrapper wait).
      - tmux `#{pane_pid}` is the launcher shim (`~/.local/bin/devin`); the
        versioned binary is its child. Liveness = pane process *tree*.
      - `/exit` via `tmux send-keys` did not end the session within 3s
        (slash palette needs selection). Programmatic graceful stop is an
        open question for `dmux kill`; SIGTERM to the process tree is the
        fallback, with `process_exited` from the wrapper as the signal.
      - Some terminals swallow the `C-b` prefix (user hit this); `dmux jump`
        is unaffected (switches from outside) but `dmux init`'s
        return-to-dashboard binding needs a doc note / alternative.
- [x] **Spike 2 — Multi-repo workspace.** *Can one agent safely operate
      across separate managed worktrees?* **Answer: yes, with cwd =
      workspace root.** VERIFIED 2026-09-28, CLI 3000.11.3. Context:
      `--add-dir` is NOT a CLI flag (the `[PATH]...` positional opens Devin
      Desktop); `/add-dir` is runtime-only. Approach tested: launch Devin
      with cwd = `workspaces/<name>/` (a plain, non-git directory) with each
      repo worktree as a subdirectory.
      - two source repos → two worktrees under one workspace dir — PASS
      - Devin's primary dir = workspace root — PASS (`sessions.db`
        `working_directory`; `workspace_dirs` stays `[]` — it only reflects
        `/add-dir` additions, subdirs are implicitly in scope)
      - read/edit/`git -C <sub>` in both repos — PASS; edits landed in
        both worktrees (`M README.md`), **source checkouts untouched**
        (clean status, HEADs unchanged)
      - per-repo `.devin/rules/*.md` (always_on) in subdirs — PASS, and the
        mechanism is now precise: **lazy discovery**. Cold start with no
        tool use → rule NOT in context (`UNKNOWN`). After reading one file
        in `api/` → rule IS in context (`API-CODENAME`) without being asked
        to look for it. Matches docs ("discovered lazily when the agent
        accesses files in that directory").
      - not tested: paths with spaces (M1 adapter unit test), single-repo
        layout decision (see below)
      Design consequences:
      - **Always use the workspace-root layout, even for one repo.** Uniform
        cwd semantics, uniform hook `projectDir`, and it gives dmux a
        non-git directory it owns.
      - **dmux writes `workspaces/<name>/AGENTS.md`** (dmux-authored, in the
        workspace root, which is NOT inside any repo → zero repo pollution)
        describing the workspace: repos, their paths, branches, base refs,
        and the session task. Loaded at session start, it gives the agent
        the multi-repo map immediately and compensates for lazy per-repo
        rule loading. Regenerated on workspace changes; never committed.
      - `/add-dir` fallback is unnecessary; drop it.
- [x] **Spike 3 — Hook-driven status.** *Can hooks reliably distinguish
      working, idle, and blocked?* **Answer: hooks alone cannot; hooks +
      read-only `sessions.db` can.** VERIFIED 2026-09-28, CLI 3000.11.3,
      interactive + `-p`:
      - every managed session's events carry its `DMUX_SESSION_ID` — PASS
      - start/turn-completion/exit observable — PASS. Full sequence per
        tool call: `PreToolUse` → `PermissionRequest` (if needed) →
        `PostToolUse` (only if it ran); all three share **`tool_use_id`**.
        `UserPromptSubmit` carries `prompt_id` (all hooks in a turn share it).
        `SessionEnd.reason`: `prompt_input_exit` (user `/exit`) vs `other`.
      - `PermissionRequest` observable — PASS. **Approve** → `PostToolUse`
        (same `tool_use_id`) → `Stop`. **Deny** → NO hook fires. **Cancel**
        (Esc) → NO hook fires. Crucially, **`Stop` does NOT fire after deny
        or cancel** — the agent goes idle at the input box with zero hook
        signal. Hook-only status would show a false "awaiting-approval"
        indefinitely.
      - resolution source found: Devin's `sessions.db` table
        `tool_call_state(session_id, tool_call_id, tool_call_json,
        tool_call_update_json)`. `tool_call_id` == hook `tool_use_id`.
        `tool_call_update_json.status` is `completed` | `failed`, with
        `_meta."cognition.ai/rejected": true` (deny) or
        `_meta."cognition.ai/canceled": true` (cancel). **Row is written
        within ~300ms of the user's action** (polled at 0.3s; deny at
        17:59:30, row at 17:59:30.289). Read-only, best-effort: schema is
        undocumented internal state.
      - passive hook (exit 0, no stdout) leaves the permission flow
        untouched — PASS (in `-p` mode Devin's own policy rejected the call,
        not us; interactively the prompt appeared normally)
      - events remain readable after process exit — PASS
      - ~~hook invocation latency~~ MEASURED 2026-09-28: `dmux hook-event`
        ≈8ms/invocation end-to-end (incl. shell fork + file append) vs ≈59ms
        for a bare `node -e` that only reads stdin — 7× faster, and the Node
        figure is a floor. Go decision validated empirically.
      - hook installation route — DECIDED: user-level
        `~/.config/devin/config.json` `"hooks"` fires identically to the
        project-level route (see below)
      - not tested: hook failure modes (nonzero exit / timeout) not
        interrupting Devin — docs say "logged, doesn't block"; verify
        opportunistically in M3, low risk since we always exit 0

### Hook installation route (DECIDED — Spike 3)

Three documented ways to register hooks; they are NOT interchangeable:

| Route | Scope | Notes |
| --- | --- | --- |
| `~/.config/devin/config.json` `"hooks"` key | user-level, every session | **Preferred for dmux.** No repo pollution; our hook already ignores events without `DMUX_SESSION_ID`, so non-dmux sessions are unaffected. `dmux init` installs it (idempotent merge, never clobber user's other hooks) |
| plugin `hooks.json` | every session where plugin installed | "best effort and fail open"; local (CLI/Desktop) only. Good as an *alternative distribution* (`devin plugins install`), not the primary |
| `.devin/hooks.v1.json` in the worktree | project-level | VERIFIED working 2026-09-27, but writes a file into the user's repo worktree — reject as primary |

Spike 3 confirmed the user-level route fires identically to the
project-level one. Config shape (per event, all 7 events):
`{"matcher": "", "hooks": [{"type": "command", "command": "<abs path>/dmux hook-event", "timeout": 5}]}`.

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
    ApprovalResolved  EventType = "approval_resolved" // reconciler-derived; Data.outcome = approved|denied|canceled|unknown
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
`SessionEnd`→session_ended. Events carry `toolUseId` and `promptId` in
`Data` when present.

**There is no hook for "permission resolved"** (Spike 3: deny and cancel
fire nothing, not even `Stop`). approval_resolved is produced by the
**reconciler**, not the hook writer, from two evidence sources in order:
1. `tool_completed` with the same `toolUseId` → resolved: approved.
2. Devin's `sessions.db` `tool_call_state` row for that `toolUseId`
   (read-only, best-effort): `completed` → approved; `_meta.rejected` →
   denied; `_meta.canceled` → canceled. Written within ~300ms of the user
   action. Denied/canceled → activity **idle** (agent is at the input box).
3. Hook-only fallback if the DB is unreadable: a later `prompt_submitted`
   with a different `promptId` implicitly resolves the pending approval
   (outcome unknown → idle). Otherwise remain awaiting-approval, but mark
   evidence as stale after a threshold and display **unknown**.

**Reducer (FSM)**: session_started → running + **idle** (at the input box until
a prompt arrives; also clears pending/exit info from a previous run — found in
dogfooding: resume without `-t` showed "working"); prompt_submitted/tool_started
→ running + working; approval_requested → awaiting-approval; turn_completed → idle;
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
  Implemented (dogfooding 2026-09-29, Ctrl-C mid-turn showed "working"
  forever): the reducer tracks in-flight tool calls; the reconciler turns
  *working with nothing in flight* into **unknown** after `StaleAfter` (3m)
  with evidence. A running tool never goes stale.

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

### Cleanup and failure handling (design constraint)

No atomic transaction spans git + filesystem + tmux + `state.json`. dmux
must manage partial failure explicitly, as a small local **saga**: ordered
steps with compensating actions, and state transitions that bracket every
external side effect so a crash at any point leaves a recognizable state.

**Workspace creation** (`dmux workspace new`):

```
1. Validate everything up front (repos exist & are git repos, branch names
   legal, no name collision, target dir absent) — fail before any side effect
2. state: reserve Workspace{status: "creating"}           ← flock'd Update()
3. for each repo: git worktree add [-b <branch>] <path> <baseRef>
      record WorkspaceRepo incl. createdBranch (did dmux create the branch?)
4. state: status → "ready"
on failure at step 3: rollback created worktrees in reverse
   (git worktree remove; git branch -d ONLY if createdBranch — `-d` refuses
   unmerged, and dmux NEVER uses `-D`), then state: status → "failed"
   (kept visible for `dmux doctor`, not silently deleted)
```

**Workspace removal** (`dmux workspace rm`):

```
1. Identify sessions in the workspace. Any lifecycle=running → REFUSE unless
   --stop given; kill ≠ rm, always
2. state: status → "deleting"
3. (--stop) terminate managed processes: signal wrapper, wait for
   process_exited (bounded), verify PID gone; kill tmux windows
4. Inspect each worktree: `git status --porcelain`, unpushed commits vs
   baseRef, `git worktree list --porcelain` for `locked`
     dirty/unpushed → REFUSE unless --discard; print summary, point to
                      `dmux workspace diff <name>`
     locked        → REFUSE always (user locked it deliberately)
5. git worktree remove <path> (never --force unless --discard)
6. Branch policy: KEEP by default. `--delete-branches` deletes via `-d` only,
   only if createdBranch. Never touch pre-existing branches.
7. git worktree prune (per source repo)
8. state: remove Workspace + its Session records          ← last
```

Devin's own conversation history is untouched by rm (it lives in Devin's
store); we just lose our `devinSessionId` mapping. Print the ids on rm so
the user can still `devin -r` manually.

**Crash recovery**: `dmux doctor` (and a lightweight check on every command)
finds workspaces stuck in `creating`/`deleting`/`failed`, sessions whose
tmux window/tag is gone, and orphaned dirs under the workspaces root, and
offers to finish the rollback/removal or reconcile the record.

Rules:
- **`dmux kill` ends a process. `dmux workspace rm` deletes work.** Never
  conflate; kill never removes files, rm never runs implicitly.
- **Destructive = explicit flag + confirmation** (`--stop`, `--discard`,
  `--delete-branches`; `--yes` to skip the prompt for scripts). Default
  invocations are always safe.
- **Respect git's worktree lock.** Locked worktrees are never removed.
- **git must not hang.** Every git invocation runs with
  `GIT_TERMINAL_PROMPT=0`, `GIT_OPTIONAL_LOCKS=0`, and a context timeout.
- **Validate before side effects; state before and after each side effect.**

## Phase 1 — Core CLI (MVP)

### Execution order: vertical slices, each demoable end-to-end

Every milestone exercises real external dependencies; no abstract
infrastructure is built ahead of the integration that proves it.

| Milestone | Slice | Demo |
| --- | --- | --- |
| **M1 — Workspace creation** (git) — **DONE 2026-09-28** | git adapter, WorkspaceManager (create saga incl. rollback), state store + flock, workspace records, workspace AGENTS.md map | `dmux workspace new feature-x --repo api` → `dmux workspace list` |
| **M2 — Session execution** (tmux + Devin) — **DONE 2026-09-28** | tmux adapter (ids, tags, switch/attach), `dmux run` wrapper, SessionManager.spawn, session records, jump picker | `dmux spawn feature-x -t "Fix authentication"` → `dmux jump` (both interactive paths verified live) |
| **M3 — Observability** (hooks) — **DONE 2026-09-29** | `dmux hook-event`, normalize, JSONL events, reducer FSM, reconciler (events + tmux liveness + Devin store), `dmux init` hook install | `dmux ls` — verified live: awaiting-approval → (user denies, no hook fires) → idle via Devin store; hooks-only would have shown awaiting-approval forever |
| **M4 — Lifecycle completeness** (reliability) — **DONE 2026-09-29** | kill, resume, workspace rm saga, branch policy, `status/diff`, `doctor` recovery | verified live: spawn → kill → resume (agent recalled prior answer); rm refusals (running/dirty/locked) then full removal with branch policy; doctor found 4 issues, fixed the 2 safe ones |
| **M5 — Product experience** (v0.1) | Bubble Tea `dmux ui`, tests, README, goreleaser, Homebrew tap | = Phase 2 |

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
  status: "creating" | "ready" | "deleting" | "failed"; // saga states; doctor reconciles non-ready
  createdAt: string;
};

type WorkspaceRepo = {
  repoId: string;
  sourcePath: string;    // original checkout (worktree ops run against it)
  worktreePath: string;  // physical path under the managed root
  branch: string;
  baseRef: string;       // for future merge-back/PR helpers
  createdBranch: boolean; // dmux created the branch → eligible for `-d` on rollback/rm; never delete pre-existing branches
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
- [x] `dmux workspace new <name> --repo <path>[@branch] ...` — create workspace
      (git worktree per repo under the managed root) and write the
      workspace-root `AGENTS.md` map (M1)
- [x] `dmux workspace list` (M1)
- [x] `dmux workspace rm` — saga: refuse if running unless `--stop`, refuse
      if dirty unless `--discard`, locked never, keep branches unless
      `--delete-branches` (M4)
- [x] `dmux workspace rename <name> <new>` — display name + tmux session; dir/branches unchanged (2026-09-29)
- [x] `dmux workspace status|diff <name>` — per-repo dirty / commits ahead of
      base / locked (M4)
- [x] `dmux doctor [--fix]` — stuck sagas, unregistered worktrees, orphan dirs,
      dead/held windows, orphan records; only safe fixes auto-apply (M4)
- [x] `dmux init` — install user-level hook config (idempotent merge into
      `~/.config/devin/config.json`) (M3). Doctor integration → M4
- [x] `dmux spawn <workspace> [-t "task prompt"]` — new Devin session in tmux (M2)
- [x] `dmux jump [query]` — picker / id / task query; switch-client inside
      tmux, exec attach outside; tag-validated (M2). Status badges → M3
- [x] `dmux ls [-w] [-v]` — table of all workspaces/sessions with derived status (M3)
- [x] `dmux kill <session>` — SIGTERM process group → grace → SIGKILL → close window; record kept (M4)
- [x] `dmux resume <session> [-t prompt]` — `devin -r <devinSessionId>` from the
      workspace root in a new tagged window; never `-c` (M4)

Cross-cutting:
- [x] tmux adapter (session-per-workspace, window-per-Devin-session; inside
      tmux → switch-client/select-window, outside → `syscall.Exec` attach;
      `@dmux_session` window tag set at spawn and verified on jump) (M2)
- [x] State store: `state.json` atomic temp+rename writes, `flock`-guarded
      `Update()` transaction, O_APPEND single-write JSONL event appender (M1).
      `config.json` deferred until a setting needs it
- [x] Status pipeline: `dmux hook-event` (hook writer) + `dmux run` (launch
      wrapper w/ process_exited) + JSONL event store + reducer + reconciler (M2/M3)
- [ ] Hooks plugin (`hooks.json`) as alternative distribution (deferred; user-level config is primary)
- [x] Graceful degradation when tmux/devin missing (doctor reports it) (M4)
- [x] git adapter: `GIT_TERMINAL_PROMPT=0`, context timeouts, porcelain
      parsing (`worktree list --porcelain`, `status --porcelain`) (M1)

## v0 validation (2026-09-29)

Functionally complete CLI (M1–M4). Validated before any UI work:

- **Tier 3 smoke harness** (`make smoke`, real Devin + isolated tmux, ~30s,
  4 cheap sessions, self-cleaning): `TestLifecycle` (workspace new → two
  concurrent spawns → both idle via hooks with devin ids captured → ls -v
  last messages → jump ambiguity/attach path → kill → exited → resume with
  recall of the earlier answer, same conversation → status clean → dirty →
  rm refusals (--stop, --discard, --yes) → full removal with branch policy
  → ls empty → doctor quiet); `TestServerRestart` (tmux server killed under
  a live session → process_exited via SIGHUP → doctor suggests resume →
  resume recalls answer); `TestConcurrentSpawns` (6 parallel spawns, all
  recorded, all windows tagged). All pass.
- Full unit + adapter suite under `-race`, lint clean, cross-compiles for
  linux/amd64, linux/arm64, darwin/amd64.
- Edge cases: state root and source paths containing spaces work; tasks
  with quotes/`$` quoted correctly into tmux; workspace names with spaces
  rejected up front; moved binary → doctor warns, `init` repoints
  ("updated") without duplicating entries.
- Known limitation: a source repo whose *directory name* contains a space
  is rejected (it would become a workspace subdirectory name). Workaround:
  rename or symlink the checkout.
- Interactive paths verified by hand earlier: picker + attach (M2),
  switch-client inside tmux (M2), approve/deny/cancel status (M3).
- Still to validate by the user: dogfooding on a real repo with a real task
  on the default tmux server.

## Phase 2 — Polish & ship v0.1

### `dmux ui` is a presentation layer, not a second implementation

By Phase 2 the architecture already supports everything the TUI needs. The
TUI calls the same `core` services as the CLI commands — it never reads raw
hook payloads, invokes git, or knows tmux topology.

```
                 ┌──────────────┐
  dmux ls ──────▶│              │◀────── dmux ui (Bubble Tea)
  dmux jump ────▶│  core.App    │
  dmux spawn ───▶│  (services)  │   Snapshot()  → []WorkspaceView{Sessions []SessionView}
  dmux kill ────▶│              │   Jump/Spawn/Kill/Resume(...) — identical calls
                 └──────┬───────┘
                        │ ports
             git · tmux · devin · state · events
```

- **One view model.** `core.Snapshot()` returns the reconciled
  workspace→session tree with derived lifecycle/activity. `dmux ls` renders
  it as a table; `dmux ui` renders it as a tree. They cannot disagree.
- **Bubble Tea mapping.** `Init`: load `Snapshot()`. `Update` receives:
  `tickMsg` (periodic reconcile, e.g. 2s — liveness check is cheap tmux
  queries), `eventsChangedMsg` (fsnotify on `~/.devin-mux/events/`, so
  status changes appear immediately without polling), key messages.
  Actions (jump/spawn/kill/resume) dispatch as `tea.Cmd`s calling the same
  `core` methods the CLI uses. `View` = pure render of the model.
- **Jumping from inside the UI.** Recommended usage: `dmux ui` runs in its
  own tmux window (`dmux-dashboard`); Enter on a session → `switch-client`
  to it, the dashboard keeps running; a global tmux binding (installed by
  `dmux init`, e.g. `prefix + D`) returns to the dashboard. Outside tmux,
  Enter → `syscall.Exec` attach (UI exits; that's expected).
- **Status badges** are derived from the lifecycle × activity matrix defined
  in Phase 1 — the TUI adds glyphs/colors, never new semantics. Unified
  "needs attention" view = filter on `awaiting-approval` (and later, ACP
  permission inbox in Phase 3 without UI changes).
- Mockup: conceptual only (not yet in repo — add `docs/mockup-ui.png` if
  wanted).

- [ ] `dmux ui` — full-screen Bubble Tea TUI over `core.Snapshot()`: tree
      of workspaces/sessions, status badges, one-key jump, fsnotify-driven
      updates, dashboard tmux window + return binding
- [x] README for v0 dogfooding: install from source, quickstart, status
      legend, command reference, safety rules, limitations, troubleshooting
      (2026-09-29). Demo GIF + Homebrew/release binaries → with M5 packaging
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
- **Stable core, replaceable integrations**: CLI/TUI → Workspace + Session
  Managers → {Git, tmux, Devin, Event} adapters → persistent state. The
  move from tmux/hooks to ACP must not require rewriting the managers, git
  lifecycle, state model, or user-facing commands.
- The three areas that deserve the most care: (1) session identity and
  ownership — four things, four ids, four lifetimes; (2) status
  observability — the differentiator, and the least certain integration;
  (3) lifecycle consistency — no atomic transaction spans git/tmux/Devin/
  state, so partial failure is handled explicitly everywhere.
