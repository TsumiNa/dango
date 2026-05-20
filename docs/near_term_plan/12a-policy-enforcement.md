# 12a — Execution Policy: Model + `passby`/`off` Enforcement (code)

Kind: code. Foundation. Implements the parts of the `10` execution-
policy axis that have a real consumer today. The approval round-trip is
split out to `12b` and deferred.

**Prerequisite.** `11` merged (the tool-config contract).

## Goal

Add the execution-policy data model to the `11` tool config, enforce
`passby` and `off`, and *record* the `need_approve` classification
(including the bash command-pattern list) without gating on it. Build
the runner snapshot + dynamic-adjust interface. Do **not** build the
approval suspend/event/wait machinery — there is no approver to consume
it until the app/cmd cycle, so it lives in `12b`.

## Honesty about the interim: no destructive-command protection

Decision (b): until `12b` ships an approver, `need_approve` provides
**no runtime protection**. A capability classified `need_approve`
executes as if `passby` — the call runs. The classification is inert
data that `12b` will later consume. There is no pause, no block, no
audit-as-protection in this subtask.

This is acceptable for the pre-alpha, trusted-developer setting. It is
**not** disguised as protection: `12a` must not claim destructive
commands are gated. The only near-term deterrent for destructive
commands is prompt-level guidance in `system_instructions.md` (see
`20`). A capability the user genuinely wants blocked now should be set
to `off`, not `need_approve`.

## Scope

1. **Policy type and config.** Extend the `11` tool config with a
   per-capability execution policy enum (`passby` default,
   `need_approve`, `off`). Applies uniformly to builtins, extras, MCP
   tools, and skills.
2. **Enforcement.** Before dispatching any tool call:
   - `passby` → run.
   - `off` → reject with a typed "disabled" error.
   - `need_approve` → run (treated as `passby` in `12a`). The value is
     recorded so `12b` can later gate it; `12a` does not pause or
     block. Do **not** build suspend/wait here.
3. **Bash command-pattern classification.** Add the ordered
   (command-head, optional subcommand/flag predicate) → policy list,
   consulted by bash after the existing allowlist and redirection
   checks. Define the matching semantics precisely (e.g. `git push`
   matches `git push ...` and `git -c k=v push ...` but not
   `git push-mirror`). Default empty; `20`'s retrofit seeds the git
   patterns. A `need_approve` match is recorded only; an `off` match
   rejects.
4. **Runner snapshot + dynamic adjust.** At runner init, copy the
   app/cmd preset into a per-run policy set. Expose an explicit runner
   API to adjust the per-run set during the run (flip to `off`,
   change a capability's policy) without mutating the preset.

## Tests

- `TestPolicyPassbyRunsImmediately`.
- `TestPolicyOffRejects`.
- `TestPolicyNeedApproveRunsInInterim` — a `need_approve` capability
  executes (no pause, no block) in `12a`.
- `TestBashCommandPatternMatchSemantics` — `git push` and
  `git -c k=v push` match; `git push-mirror` does not.
- `TestBashCommandPatternOffRejects` — an `off` pattern is blocked.
- `TestRunnerSnapshotIsolatesFromPreset`.
- `TestRunnerDynamicAdjustAffectsOnlyThisRun`.

## Out of scope

- No approval suspend/event/wait round-trip (that is `12b`).
- No specific git patterns yet (seeded by `20`'s retrofit).
- No MCP wiring (MCP consumes this in its own cycle).

## Honshu observation

`12a` introduces no user-facing pause (decision (b)), so it needs no
honshu observation of its own. The user-facing approval behavior is
`12b`, and honshu is its primary input.

## Verifiable acceptance

- New and existing tests pass; `go test ./...`, `go vet ./...`,
  `go build ./...` green.
- The policy enum, `passby`/`off` enforcement, `need_approve`
  recording (without gating), command-pattern matching, and runner
  snapshot/adjust API exist and are exercised by tests.
