# devin-mux — Multi-Session Devin Orchestrator

## Concept (memory — keep updated as scope evolves)

A worktree-session organizational framework for running multiple Devin CLI
sessions simultaneously:

- **Workspace** (entity name; "worktree" reserved for the git primitive) = a
  named unit containing git worktrees of 1..N repos (user picks the repo set).
  Lives under a managed root dir, e.g. `~/.devin-mux/workspaces/<id>/<repo>/`.
- **Session** = one Devin CLI session scoped to a task, launched inside a
  workspace. A workspace can host multiple sessions. Multi-repo workspaces
  launch Devin with all repo dirs as workspace dirs (`--add-dir` / `/add-dir`).
- Users can jump between all live sessions; all run concurrently.

## Key platform facts (verified against Devin CLI 3000.x docs)

- Devin sessions are **per-directory**: `devin -c` resumes most recent session
  in cwd; `devin -r <id>` resumes by ID. Worktree = natural isolation unit.
- Concurrent `devin` processes are fine; git worktrees prevent file/branch
  collisions between parallel agents.
- Plugins CANNOT add UI to Devin (CLI or web). Plugin surface: skills, rules,
  hooks (hooks.json), MCP servers, subagents. So the orchestrator is a
  **standalone companion tool**, with an optional plugin for integration.
- Hooks useful for status: `Stop`, `PostToolUse`, `PermissionRequest`,
  `SessionStart/End` command hooks receive JSON on stdin (incl. `session_id`),
  can write per-session status to a shared state file the orchestrator watches
  ("working / idle / awaiting approval").
- `devin acp` runs a session as a headless JSON-RPC-over-stdio subprocess
  (structured events: messages, tool calls, plans, permission requests) —
  foundation for a future custom dashboard UI (phase 2).
- `/handoff` can escalate a local session to a cloud Devin — potential
  differentiator: mixed local/cloud session board.

## Architecture decisions

- **Language: Go** (switched from TS before Phase 1). Rationale: this is a
  systems CLI (spawning git/tmux/devin, filesystem state, event streams);
  `os/exec` + `context` timeouts fit naturally; single static binary
  (brew/curl install, no Node runtime); goroutines/channels map onto the
  phase-3 multi-session event aggregation; ACP Go SDKs exist
  (`coder/acp-go-sdk`, `caelis-labs/acp-go-sdk`, interop-tested vs official
  SDKs). **Decisive factor: hook latency** — the status hook runs
  synchronously inside Devin's tool loop on every tool call; a Go binary
  starts in ~1-2ms vs ~50-100ms for Node, and the same `dmux` binary can serve
  as the hook command. Stack: cobra (CLI), Bubble Tea + Lip Gloss + Bubbles
  (TUI), goreleaser (release).
- **v1 multiplexer: tmux** — each session runs interactive `devin` in a tmux
  window/pane; orchestrator provides picker to jump between them.
- **Data model principles** (full schema in PLAN.md): stable logical IDs
  separate from display names (workspace `id` vs `name`; tmux stable
  `windowId` vs window name); physical paths recorded explicitly
  (`sourcePath`/`worktreePath`/`baseRef`); session **lifecycle**
  (starting/running/exited/failed) kept orthogonal to **activity**
  (working/idle/awaiting-approval/unknown) — collapse only for display.
  `devinSessionId` recorded to enable `devin -r` resumption after exit.
- Phase 2 (later): ACP-based multi-session dashboard (own UI, unified
  permission inbox), replacing/augmenting tmux.

## Prior art / positioning

Claude Squad, Conductor, Crystal, Vibe Kanban do worktree-per-agent for Claude
Code. None target Devin CLI. Differentiators: multi-repo worktree sets,
cloud handoff integration.

## Project conventions

- Plan lives in PLAN.md; keep it current as milestones complete.
