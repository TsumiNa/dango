# 20 — Add `git` to the Bash Allowlist (code)

Kind: code. Coverage memo § 3.4.

**Prerequisite.** `12` merged (so destructive subcommands can be seeded
into the command-pattern `need_approve` list instead of a `bashBlock`).

## Goal

Allow `git <subcommand>` through the bash tool so skills can run
read-oriented git inspection (`log`, `diff`, `show`, `blame`, `status`,
`rev-parse`). Route destructive subcommands through the unified
`need_approve` policy rather than blocking them outright.

## Scope

1. In `internal/llm/internal/builtin/allowlist.go`: add a version-control
   comment block and append `"git"` to `defaultAllowlist`.
2. Seed the bash command-pattern policy list (from `12`) with
   `need_approve` for `git push`, `git reset --hard`, `git clean`,
   `git rebase`, `git gc`. Read-oriented subcommands stay `passby`.
3. Update `internal/llm/internal/builtin/system_instructions.md` bash
   bullet: list the read-oriented git uses and note that destructive
   subcommands will pause for approval.

## Tests

- `TestDefaultAllowlistIncludesGit`.
- `TestBashAllowsGitVersion`.
- `TestBashAllowsGitLogInsideWorkspace` — init a temp repo in the
  workspace, commit, `git -C <ws.Root> log -1`, assert the subject
  appears.
- `TestBashGitPushTriggersNeedApprove` — `git push` matches the seeded
  pattern and runs the approval flow rather than executing directly.
- `TestBashRejectsGitOutsideWorkspaceTarget` — `git log > /tmp/escape`
  still rejected by the PR C-1 redirection check.

## Out of scope

- No `git_*` wrapper tool.
- No subcommand allowlist beyond the seeded `need_approve` patterns.

## Verifiable acceptance

- New and existing tests pass; `go test ./...` green.
- Honshu example still completes; non-git skills see no change; a
  `git push` in a stubbed run pauses for approval.
