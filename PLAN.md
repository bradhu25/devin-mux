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
      verify: (a) `DMUX_SESSION_ID` env set on the `devin` process is
      inherited by hook subprocesses; (b) which hooks fire after a
      PermissionRequest is **approved**, **denied**, and **cancelled** — this
      determines how approval_resolved is inferred; (c) a hook exiting 0 with
      no stdout leaves the permission flow untouched; (d) `Stop` fires per
      turn and `SessionEnd` fires on exit (never confuse them)
- [ ] Spike 4 (critical): **specific-conversation resumption.** Start two
      Devin sessions in the same worktree dir, exit both, then verify:
      (a) `devin -r <id>` restores the *specific* conversation, not the most
      recent in the directory; (b) the `session_id` in hook stdin
      (`SessionStart`) is the same identifier `devin -r` accepts — this is how
      dmux captures `devinSessionId` without user input; (c) whether resume
      works from a different cwd or is directory-bound; (d) how resumed
      sessions surface in `SessionStart` (`source` field) so dmux can
      distinguish resume from fresh start. If (a) or (b) fails, resumption
      design must change before Phase 1.

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
