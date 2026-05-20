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

## Retrofit (after `12a`)

Seed the bash command-pattern policy list with `need_approve` for
`git push`, `git reset --hard`, `git clean`, `git rebase`, `git gc`.
Add `TestBashGitPushClassifiedNeedApprove`. Update
`system_instructions.md` to state these pause/are-marked per the `12a`
interim behavior.

## Out of scope

- No `git_*` wrapper tool.
- No subcommand allowlist beyond the eventual `need_approve` patterns.

## Honshu observation (at retrofit)

When the `need_approve` classification lands, honshu is the signal for
whether the destructive-git pattern set is the right one — too broad
(annoying), too narrow (surprising). Record adjustments. The wave-1
allowlist-only step has no user-facing behavior change and needs no
honshu observation.

## Verifiable acceptance

- Wave 1: new and existing tests pass; `go test ./...` green; non-git
  skills see no change.
- Retrofit: the git destructive patterns are classified by the `12a`
  policy layer and covered by a test.
