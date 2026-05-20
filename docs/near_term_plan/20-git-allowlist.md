# 20 — Add `git` to the Bash Allowlist (code, wave 1)

Kind: code. Coverage memo § 3.4. Wave 1 — ships against the current
allowlist surface, no security-model dependency.

**Prerequisite.** None.

## Goal

Allow `git <subcommand>` through the bash tool so skills can run
read-oriented git inspection (`log`, `diff`, `show`, `blame`, `status`,
`rev-parse`). Destructive subcommands stay reachable for now; gating
them is a retrofit once `12a` lands.

## Scope (wave 1)

1. In `internal/llm/internal/builtin/allowlist.go`: add a version-control
   comment block and append `"git"` to `defaultAllowlist`.
2. Update `internal/llm/internal/builtin/system_instructions.md` bash
   bullet: list the read-oriented git uses and note (for now, as
   guidance, not enforcement) that destructive subcommands like
   `git push` / `git reset --hard` should be avoided unless the task
   calls for them.

## Tests (wave 1)

- `TestDefaultAllowlistIncludesGit`.
- `TestBashAllowsGitVersion`.
- `TestBashAllowsGitLogInsideWorkspace` — init a temp repo in the
  workspace, commit, `git -C <ws.Root> log -1`, assert the subject
  appears.
- `TestBashRejectsGitOutsideWorkspaceTarget` — `git log > /tmp/escape`
  still rejected by the PR C-1 redirection check.

## No runtime protection for destructive git (decision (b))

In the near term there is **no runtime gate** on destructive git
subcommands. `git push`, `git reset --hard`, `git clean`, `git rebase`,
`git gc` run if the model invokes them. The only deterrent is
prompt-level guidance in `system_instructions.md`. This is acceptable
for the pre-alpha, trusted-developer setting; the plan does not pretend
otherwise (see `12a` "Honesty about the interim").

Real gating arrives only with `12b`'s approval round-trip, which is
deferred until an approver exists. If a destructive subcommand must be
hard-blocked before then, set it to `off` via the `12a` policy layer —
do not rely on `need_approve`, which does not gate until `12b`.

## Retrofit (after `12a`, groundwork only)

Seed the bash command-pattern policy list with `need_approve` for
`git push`, `git reset --hard`, `git clean`, `git rebase`, `git gc`.
This is inert classification that `12b` will later consume; it does not
protect anything on its own. Add `TestBashGitPushClassifiedNeedApprove`
asserting the pattern is recorded (and still runs, per `12a`). Do
**not** add wording that implies these pause or are blocked.

## Out of scope

- No `git_*` wrapper tool.
- No subcommand allowlist beyond the eventual `need_approve` patterns.

## Honshu observation

The wave-1 allowlist-only step has no user-facing behavior change and
needs no honshu observation. (Destructive-git UX is a `12b` concern.)

## Verifiable acceptance

- Wave 1: new and existing tests pass; `go test ./...` green; non-git
  skills see no change.
- Retrofit: the git destructive patterns are recorded by the `12a`
  policy layer (and still execute) and covered by a test.
