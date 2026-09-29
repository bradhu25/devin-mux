# devin-mux (`dmux`)

Run several [Devin CLI](https://docs.devin.ai/cli) sessions at once, each in
its own git worktree, and always know which one needs you.

```
$ dmux ls
WORKSPACE    SESSION   STATUS                      TASK                       LAST EVENT                  DEVIN
feature-x    s_0a7ae6  ● working                   Fix the auth bug in api/   tool_started 3s ago         olive-turkey
feature-x    s_3c1751  ! awaiting-approval (exec)  Add tests for web/         approval_requested 40s ago  glaze-toque
payment-fix  s_d40b7c  ○ idle                      Validate refund edge…      turn_completed 2m ago       dapper-device
```

**Status: v0.** The command-line tool is complete and validated end to end
against the real Devin CLI; a full-screen dashboard (`dmux ui`) is planned but
not built. Everything below works today, from the terminal, with no UI.

## What it does

- **Workspaces** — a named set of git worktrees, one per repo, under
  `~/.devin-mux/workspaces/<name>/`. Parallel agents never share a checkout.
- **Sessions** — Devin CLI sessions running detached in tmux, with the
  workspace root as their working directory so every repo is in scope.
- **Live status** — `working` / `idle` / `awaiting-approval` / `exited`,
  derived from Devin's lifecycle hooks, the tmux window, and Devin's own
  record of tool-call outcomes (so a permission you *denied* shows `idle`,
  not a stuck "awaiting approval").
- **Navigation** — `dmux jump` picks a session and drops you into it.
- **Lifecycle** — `kill` a session, `resume` its exact conversation later,
  remove a workspace safely (refuses to lose work unless you say so),
  `doctor` to repair anything left half-done.

## Requirements

- macOS or Linux
- [Devin CLI](https://docs.devin.ai/cli) (`devin`) logged in
- `tmux` 3.x and `git` 2.x
- Go 1.27+ to build (until binaries are published)

## Install

```bash
git clone https://github.com/bradhu25/devin-mux
cd devin-mux
make install          # go install -> $(go env GOPATH)/bin/dmux, then checks PATH
dmux init             # register the status hook in ~/.config/devin/config.json
dmux doctor           # everything should read "No problems found."
```

**Hooks.** `dmux init` edits Devin's user config: it appends its own hook
entries, leaves everything else exactly as it was, and writes a timestamped
backup next to the file. The entries point at the binary's absolute path, so
re-run `dmux init` after building to a new location (`dmux doctor` will tell
you). `dmux init --uninstall` removes the entries again.

Developing on dmux itself? Use `make build` and put a symlink on PATH once
(`ln -s "$PWD/bin/dmux" ~/.local/bin/dmux`) so every rebuild is picked up
without copying binaries around.

The hook is passive and cheap (~10 ms). It only records events for sessions
dmux started; your other Devin sessions are ignored.

## Quick start

```bash
# 1. A workspace: one worktree per repo, on a new branch dmux/<name>
dmux workspace new feature-x --repo ~/code/api --repo ~/code/web

# 2. Sessions: each runs in its own tmux window, cwd = the workspace root.
#    -t is the task: Devin's first prompt, and the session's label from then on.
dmux spawn feature-x -t "Fix the auth bug in api/ and add a regression test"
dmux spawn feature-x -t "Update web/ to handle the new error code"
#    -> Spawned session s_0a7ae6 ...   (ids also appear in `dmux ls`)

# 3. Watch
dmux ls                   # the board, once
dmux ls -w                # same, refreshing every 2s until Ctrl-C

# 4. Go there when one needs you
dmux jump                 # picker (outside tmux: attaches; inside: switches window)
dmux jump auth            # or name it: task text, session id, or id prefix

# 5. Later — every <session> argument accepts task text, id, or id prefix
dmux kill auth
dmux resume auth -t "Pick up where you left off and run the tests"
dmux workspace status feature-x
dmux workspace rm feature-x --stop --delete-branches
```

### Naming sessions

Wherever a command takes a `<session>`, you can pass the session id
(`s_0a7ae6`), a unique id prefix (`s_0a`), or any part of the task text,
case-insensitive (`auth`). It must match exactly one session; if it's
ambiguous, dmux lists the candidates. Ids come from `dmux spawn`'s output and
the `SESSION` column of `dmux ls`; in practice the task text is what you'll
type. `-t` is optional on `spawn` — without it Devin opens at an empty prompt —
but a short task makes the session findable, so give one even if you plan to
type the real request yourself.

### Reading `dmux ls`

| Badge | Meaning |
| --- | --- |
| `● working` | the agent is executing |
| `! awaiting-approval (exec)` | a permission prompt is waiting for you — jump there |
| `○ idle` | the agent finished its turn and is waiting for input |
| `◌ starting` | process launched, no events yet |
| `× exited` / `✗ failed` | process gone; `dmux resume` continues the conversation |

`dmux ls -v` adds the agent's last message and the evidence behind each status.

### Inside a session

You are in a normal Devin CLI session inside a tmux window. Leave it running
with tmux's detach (`Ctrl-b d`, or run `tmux detach` from **another** terminal
if your terminal intercepts `Ctrl-b`). Do not type `tmux detach` into Devin's
prompt — it will politely explain that it is not your shell.

`/exit` (or `Ctrl-D` on an empty prompt) ends the Devin process; dmux keeps the
window open with a note and records the exit, and the conversation stays
resumable.

## Command reference

| Command | Notes |
| --- | --- |
| `dmux init [--uninstall]` | install / remove the status hook in Devin's user config |
| `dmux workspace new <name> --repo <path>[@branch] ...` | worktree per repo; `@branch` reuses an existing branch instead of creating `dmux/<name>`; `--base <ref>` for the branch point |
| `dmux workspace list` | |
| `dmux workspace status <name>` (alias `diff`) | per repo: uncommitted changes, commits not in the base ref, worktree locks |
| `dmux workspace rm <name>` | see [Safety](#safety) |
| `dmux spawn <workspace> [-t "task"] [--permission-mode m] [--model m]` | new session; `-t` is Devin's first prompt and the session's label; warns when others already share the workspace |
| `dmux ls [-w] [-v]` | status board, once; `-w` refreshes every 2s; `-v` adds evidence and last message |
| `dmux jump [<session>]` | picker when no argument; see [Naming sessions](#naming-sessions) |
| `dmux kill <session> [--grace 5s]` | SIGTERM → wait → SIGKILL, close window; record kept |
| `dmux resume <session> [-t "prompt"]` | `devin -r <conversation>` in a new window, from the workspace root; `-t` is submitted on resume |
| `dmux doctor [--fix]` | report problems; `--fix` applies only repairs that cannot lose work |

Every command has `--help`.

### Shell completion

`<TAB>` completes commands, flags, **workspace names** (`spawn`, `workspace
status|rm`) and **session ids** with their task — live sessions for `jump` and
`kill`, resumable ones for `resume` — so you never have to remember an id.
Enable it once for your shell:

```bash
# bash (macOS ships bash 3.2, where `source <(...)` fails silently — use a file)
dmux completion bash > ~/.dmux-completion.bash
echo 'source ~/.dmux-completion.bash' >> ~/.bash_profile
# zsh
dmux completion zsh > ~/.dmux-completion.zsh
echo 'source ~/.dmux-completion.zsh' >> ~/.zshrc
# fish
dmux completion fish > ~/.config/fish/completions/dmux.fish
```

Regenerate the file after upgrading dmux.

## Safety

The tool is built around a few rules that hold in code, not just in docs:

- **`kill` ends a process; `workspace rm` deletes work. Never the other way.**
- `workspace rm` **refuses** while a session is running (`--stop` to end them),
  **refuses** if any repo has uncommitted changes or commits not in its base
  ref (`--discard` to lose them), and **never** removes a worktree locked with
  `git worktree lock`. Destructive flags prompt for confirmation; pass `--yes`
  in scripts.
- Branches are kept by default. `--delete-branches` deletes only branches dmux
  created, only with `git branch -d` (unmerged branches are kept). dmux has no
  code path that runs `git branch -D`.
- `resume` continues the *recorded* conversation (`devin -r <id>`). It never
  guesses by directory, so several sessions in one workspace stay distinct.
- Isolation is **per workspace, not per session**: two sessions in the same
  workspace share its files and branches. Use one workspace per task and
  multiple sessions only for sub-tasks you know don't conflict.
- `doctor --fix` finishes interrupted operations only when the worktrees are
  clean, closes windows of exited sessions, and drops records that point at
  nothing. It never touches uncommitted work, unmerged branches, or resumable
  sessions — for those it prints the exact command to run.
- Devin's conversation history is never modified. Removing a workspace prints
  the conversation ids so you can still `devin -r` them by hand.

## Where things live

```
~/.devin-mux/
├── state.json                 workspace + session records (atomic writes, flock)
├── events/<session>.jsonl     lifecycle events from the hook and the launcher
└── workspaces/<name>/         the worktrees, plus a generated AGENTS.md map
~/.config/devin/config.json    Devin's user config (dmux adds "hooks" entries)
```

Deleting `~/.devin-mux/events/` is safe (status recomputes from tmux and Devin's
store); deleting `state.json` orphans your worktrees — use `workspace rm`.

Set `DMUX_ROOT` to use a different state directory and `DMUX_TMUX_SOCKET` to use
an isolated tmux server (`tmux -L`); both are meant for testing.

## Known limitations (v0)

- macOS/Linux only.
- A source repo whose **directory name** contains a space cannot be used
  (it would become a workspace subdirectory). Symlink or rename the checkout.
- Some terminals intercept `Ctrl-b`, tmux's prefix. `dmux jump` is unaffected;
  detaching then needs `tmux detach` from another terminal.
- Status depends on the hook being installed and pointing at the current
  binary; `dmux doctor` checks this.
- Devin CLI's hook payloads and session store are version-specific; dmux was
  verified against Devin CLI 3000.11.x. After a Devin upgrade, run
  `dmux doctor` and `make smoke`.
- No `dmux ui` yet; no packaged binaries yet.

## Troubleshooting

- **Status stuck on `starting` / `unknown`** — hooks not installed or pointing
  elsewhere: `dmux doctor`, then `dmux init`.
- **`dmux jump` says the window is gone** — the tmux server restarted (reboot,
  `tmux kill-server`). The session is `exited`; `dmux resume <id>`.
- **Something was interrupted mid-way** — `dmux doctor`, then `--fix` if it
  offers one; otherwise it tells you the command.
- **Reset everything** — `dmux workspace rm <name> …` for each workspace,
  `dmux init --uninstall`, then `rm -rf ~/.devin-mux`.

## Development

```bash
make build      # bin/dmux
make test       # unit + adapter tests (real git/tmux in isolation, no Devin)
make lint       # go vet + golangci-lint
make smoke      # end-to-end with the real Devin CLI (launches a few tiny sessions)
```

See [`PLAN.md`](PLAN.md) for the design and [`docs/TESTING.md`](docs/TESTING.md)
for the testing methodology.
