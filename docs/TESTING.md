# Testing methodology

Organizing rule: **test each piece at the cheapest tier that can actually
prove it works**, and never let paid Devin sessions leak into routine runs.

| Tier | Scope | Runs | Command |
| --- | --- | --- | --- |
| 1. Unit | `internal/core`, `internal/state`, `internal/hooks` | every change, CI | `make test` |
| 2. Adapter integration | `internal/adapters/*`, `test/integration/` | every change, CI (skips if tool absent) | `make test` |
| 3. Smoke | `test/smoke/` — real Devin CLI end-to-end | opt-in: before release, after Devin upgrades | `make smoke` |
| 4. Spikes | Phase 0 questions with pass/fail criteria | manual, once | recorded in PLAN.md |

## Tier 1 — Unit tests (fakes, milliseconds)

Pure logic against fake adapters. The core/adapters boundary exists largely
for this: `core` depends only on the interfaces in `core/ports.go`
(`Git`, `Tmux`, `Devin`, `Store`, `EventLog`), so orchestration is tested
by injecting fakes:

- Creation saga rollback: fake git that fails on the second worktree →
  assert the first is removed and the workspace ends in `failed`.
- Reducer FSM: feed event sequences → assert lifecycle × activity, including
  out-of-order arrivals (`tool_completed` before `tool_started`).
- Reconciler precedence: combine last-event + fake liveness → assert display
  state (`unknown` when undeterminable, never false `idle`).

The boundary is enforced mechanically: `.golangci.yml` depguard makes
`os/exec` a lint error anywhere outside `internal/adapters`.

Concurrency properties are tested with real goroutines under `-race`, not
mocked away (e.g. `state.AppendEvent`: 16 writers × 200 lines, assert zero
interleaved JSON lines).

## Tier 2 — Adapter integration tests (real tools, isolated)

Each adapter runs against the real external tool in a sandbox:

- **git**: throwaway repos created in `t.TempDir()`; assert porcelain
  parsing, worktree add/remove/list, lock detection, `createdBranch`.
- **tmux**: isolated server `tmux -L dmux-test-<random>`, killed in
  `t.Cleanup`. Tests can never touch the user's real tmux sessions. Assert
  `$N`/`@N` ids, `@dmux_*` user options, switch/attach argument construction.
- **devin**: read-only parsing of `devin list --format json` fixtures and
  `sessions.db` schema; no live sessions.

Tests `t.Skip` when the tool is not installed so a minimal CI still runs
tier 1. These catch what fakes cannot: porcelain format drift, quoting,
id semantics.

Fakes model the *interface*; integration tests prove the real tool honors it.
If they disagree, the adapter is wrong, not the core.

## Tier 3 — Smoke tests (real Devin, opt-in, costs money)

End-to-end: `dmux workspace new` → `dmux spawn` → hook events arrive →
status transitions → `dmux resume` restores the conversation. Double-gated
so they can never run by accident: build tag `smoke` **and** `DMUX_SMOKE=1`.
Prompts are minimal ("reply with exactly ALPHA") to keep cost near zero.
Use `DMUX_ROOT` pointing at a temp dir so smoke runs never touch real state.

## Tier 4 — Spikes

Manual experiments that answer one question decisively with explicit
pass/fail criteria (see PLAN.md Phase 0). Results are recorded in PLAN.md
and AGENTS.md as verified platform facts, with the Devin CLI version and
date, so code can rely on them and we know what to re-verify on upgrade.

## Habits

- **Failing test first** for bugs and for behavior with a clear spec. The
  persistence, cleanup, and status sections of PLAN.md translate almost
  directly into test cases.
- **Verify the platform contract before hardcoding it** (`devin --help`,
  hook payloads from a real run). Docs drift; the binary is the truth.
- `make lint` (vet + golangci-lint) and `make test` must pass before every
  commit. `make smoke` before tagging a release.
- Never write to Devin's own store (`sessions.db`) from any test.

## Commit message template

```
<summary line, imperative, ≤72 chars>

What changed:
- ...

Why is this needed:
- ...

How was it tested:
- ...

Additional notes/resources:
- ...
```

Every commit uses this template. "How was it tested" names the tier(s)
and the concrete command or manual steps; write "none (docs only)" when
that is the honest answer.
