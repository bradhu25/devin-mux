# PLAN — devin-fun

Workflow plan for building and shipping the worktree-session Devin orchestrator.

## Phase 0 — Scaffold & spike (validate riskiest assumptions first)

- [ ] Scaffold TS project (`npm init`, tsconfig, eslint, vitest, `bin` entry)
- [ ] Spike 1: create a git worktree programmatically + launch `devin` in it
      inside a tmux window; confirm session history binds to worktree dir
      (`devin -c` resumes correctly per worktree)
- [ ] Spike 2: multi-repo worktree — two repos' worktrees under one folder,
      launch `devin --add-dir`; confirm workspace spans both
- [ ] Spike 3: status hook — plugin `hooks.json` (SessionStart/Stop/
      PermissionRequest) writing JSON lines to `~/.devin-fun/state/`;
      confirm orchestrator can tail it for live status

## Phase 1 — Core CLI (MVP)

Data model (persisted as JSON under `~/.devin-fun/`):
- `Worktree { name, repos: [{ path, branch, worktreePath }], createdAt }`
- `Session { id, worktree, task, tmuxTarget, devinSessionId?, status, createdAt }`

Commands:
- [ ] `dfun worktree new <name> --repo <path>[@branch] ...` — create worktree set
- [ ] `dfun worktree list / rm` — list, clean up (git worktree remove + prune)
- [ ] `dfun spawn <worktree> [-t "task prompt"]` — new Devin session in tmux
- [ ] `dfun jump` — interactive picker (worktree → session) that switches
      tmux client to the chosen session; show status badges
- [ ] `dfun ls` — table of all worktrees/sessions with live status
- [ ] `dfun kill <session>` — end a session (tmux + record)

Cross-cutting:
- [ ] tmux adapter (session-per-worktree, window-per-Devin-session naming
      scheme, attach/switch logic for inside vs outside tmux)
- [ ] Status via hooks plugin (working / idle / awaiting-approval)
- [ ] Graceful degradation when tmux/devin missing; doctor command

## Phase 2 — Polish & ship v0.1

- [ ] `dfun ui` — full-screen ink TUI: tree of worktrees/sessions, status,
      one-key jump
- [ ] Docs: README with demo GIF, install (`npm i -g devin-fun`), quickstart
- [ ] Tests: unit (state, git worktree ops) + smoke script
- [ ] Publish to npm; tag v0.1.0

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
