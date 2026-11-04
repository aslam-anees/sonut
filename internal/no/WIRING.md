# internal/no — vendoring notes

`internal/no` vendors the core git-safety logic from
[`no-mistakes`](https://github.com/kunchenguid/no-mistakes) (checked out
locally at `./no-mistakes`, its own Go module, excluded from the sonut
build) into the sonut codebase under `github.com/aistatine/sonut/internal/no`.

## What was vendored

no-mistakes is primarily a full push-gating pipeline (worktree spin-up,
AI-driven review/test/lint/docs steps, PR automation, a daemon, a TUI). Only
the pure, dependency-light *mistake-prevention* logic was ported — the parts
that answer "is this git mutation safe to run", not the CI/agent
orchestration around it:

- **Force-push data-loss guard** (`internal/no/forcepush.go`), ported from
  upstream `internal/pipeline/steps/forcepush.go`
  (`resolveForcePushDecision` and its helpers, renamed to
  `ResolveForcePushDecision`). Re-reads the live remote head before a
  force-push and refuses it unless the branch is new, the remote is already
  at the target SHA, the remote hasn't moved since it was last observed, or
  every commit still on the remote is already incorporated by patch-id (or
  is part of history the caller already knew about, via `baseSHA`).
  Otherwise it fails closed with a `*ForcePushWouldDiscardError` rather than
  ever degrading to a blind `--force`.
- **Protected-branch guard** (`internal/no/guard.go`,
  `checkProtectedBranch`) — new, sonut-specific: warns (does not block) when
  a commit/push targets a configured protected branch (default
  `main`/`master`). Upstream has no direct equivalent; this repo's git layer
  (`internal/sonutgit`) only does local commits, not upstream-branch
  protection via a remote host, so this is a minimal local stand-in.
- **Config loading** (`internal/no/config.go`) — a deliberately small subset
  of upstream's `.no-mistakes.yaml` schema (`internal/config/config.go`'s
  `RepoConfig`), covering only the two guard-relevant keys:
  `protected_branches` and `block_force_push_data_loss`
  (plus `allow_protected_branch_commits` to opt out of the warning
  entirely). A missing file resolves to `DefaultConfig()` — guards on, but
  nothing to set up first ("permissive-but-sane").
- **Findings model** (`internal/no/no.go`) — a small `Finding`/`Findings`
  type with `SeverityBlock`/`SeverityWarn`, replacing upstream's much larger
  `internal/types` findings machinery (auto-fix classification, ask-user
  review flow, intent conformance, etc.) with just enough to express "block
  this operation" vs. "warn but proceed".

## What was skipped, and why

- **CLI packaging** (`cmd/no-mistakes`, `internal/cli`) — sonut has its own
  CLI surface; no-mistakes' subcommands (`init`, `axi`, `daemon`, ...) don't
  apply.
- **Daemon/pipeline/agent orchestration** (`internal/daemon`,
  `internal/pipeline`, `internal/agent`, `internal/ipc`, `internal/db`,
  `internal/lifecycle`) — the whole "spin up a worktree, run an AI review
  pipeline, open a PR" flow is out of scope; sonut only needed the git
  mutation guard, not a second orchestration layer.
- **TUI** (`internal/tui`) — no-mistakes ships an interactive terminal UI
  for approving findings; sonut has no equivalent surface to wire it into.
- **SCM host integrations** (`internal/scm/github`, `gitlab`,
  `azuredevops`, `bitbucket`) — PR creation/CI polling against hosted
  forges. Not part of "guard git operations before running them locally".
- **Release/workflow test files** (`workflow_*_test.go`,
  `install_script_test.go`, `makefile_test.go` at the repo root) — these
  pin no-mistakes' own release pipeline and GitHub Actions workflows; they
  have no sonut equivalent.
- **Demo assets** (`demo.gif`, `demo.mp4`, `demo.tape`) and **skills**
  (`skills/no-mistakes/SKILL.md`, `internal/skill`) — packaging/marketing
  and the `/no-mistakes` agent skill generator; not core guard logic.
- **`internal/gate`, `internal/git/hook.go`, `internal/git/env.go`,
  `internal/safeurl`, `internal/winproc`, `internal/shellenv`,
  `internal/buildinfo`, `internal/cimonitor`, `internal/conventional`,
  `internal/paths`, `internal/telemetry`, `internal/update`,
  `internal/wizard`, `internal/e2e`, `internal/bitbucket`,
  `internal/intent`** — supporting infrastructure for the bare-repo gate
  proxy, post-receive hooks, the daemon, and telemetry. sonut's git layer
  (`internal/sonutgit`) already has its own working-tree git plumbing
  (`internal/sonutgit/sonutgit.go`), so none of this was needed; only the
  pure decision function (`resolveForcePushDecision`) was lifted out of
  `internal/git`'s orbit.

## Integration point

`internal/sonutgit/guard.go` adds `guardCommit`, called from
`Commit()` in `internal/sonutgt/sonutgit.go` right before staging/committing
(after `Inspect()` has resolved `summary.Root` and `summary.Branch`, before
`git add -A` / `git commit`). It:

1. Loads `.no-mistakes.yaml` from the repo root via `no.LoadConfig`. A
   missing file is not an error (falls back to `no.DefaultConfig()`); a
   malformed file fails *open* to the default config rather than blocking
   every commit in the repo on a config typo.
2. Runs `no.Check(ctx, cfg, adaptedRunner, no.Operation{Kind: no.KindCommit,
   Cwd: root, Branch: branch})`.
3. If any finding is blocking (`Findings.Blocking()`), `Commit()` returns a
   wrapped `*sonutgit.GuardError` and the commit does not happen. Warnings
   are returned alongside a nil error so callers can surface them without
   stopping the commit.

With the default config (or no config at all), the only guard `Commit()`
can trip is the protected-branch check, which is **warn-only** — so this
wiring is non-breaking: every existing `sonutgit` commit path (and its
existing tests) behaves exactly as before. The force-push guard
(`no.KindForcePush`, blocking) is wired into `no.Check` and covered by
tests in `internal/no`, but `internal/sonutgit` has no push path yet to
call it from; it's ready for the next caller (e.g. a future
`sonutgit.Push`) to reuse via the same `no.Check` entry point.

## New dependencies

- `gopkg.in/yaml.v3` (already present in `go.sum` transitively; promoted to
  a direct `require` in `go.mod` for `internal/no/config.go`'s
  `.no-mistakes.yaml` parsing). No new module was added to the dependency
  graph — this is the same YAML library no-mistakes itself uses.

## Tests

- `internal/no/guard_test.go` — config load defaults/overrides/malformed
  file, protected-branch warn vs. clean, force-push guard block/allow paths
  (new branch, up-to-date, unseen-remote-commits refusal), disabled
  force-push guard skipping the remote check entirely.
- `internal/sonutgit/guard_test.go` — `guardCommit` permissive-by-default
  and fail-open-on-malformed-config behavior, `GuardError` message
  formatting, and a regression test (`TestCommitOnProtectedBranchStillSucceeds`)
  proving `Commit()` on `main` still succeeds with no `.no-mistakes.yaml`
  present, i.e. the hook did not change existing behavior.

Run: `go build ./... && go test ./internal/no/... ./internal/sonutgit/...`
