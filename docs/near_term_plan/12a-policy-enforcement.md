# 12a — Execution Policy: Model + `passby`/`off` Enforcement (code)

Kind: code. Foundation. Implements the parts of the `10` execution-
policy axis that have a real consumer today. The approval round-trip is
split out to `12b` and deferred.

**Prerequisite.** `11` merged (the tool-config contract).

## Goal

Add the execution-policy data model to the `11` tool config, enforce
`passby` and `off`, and classify (but do not yet gate via approval)
the `need_approve` cases including the bash command-pattern list. Build
the runner snapshot + dynamic-adjust interface. Do **not** build the
approval suspend/event/wait machinery — there is no approver to consume
it until the app/cmd cycle, so it lives in `12b`.

## Scope

1. **Policy type and config.** Extend the `11` tool config with a
   per-capability execution policy enum (`passby` default,
   `need_approve`, `off`). Applies uniformly to builtins, extras, MCP
   tools, and skills.
2. **`passby` / `off` enforcement.** Before dispatching any tool call:
   - `passby` → run.
   - `off` → reject with a typed "disabled" error.
   - `need_approve` → for now, apply the chosen interim behavior
     (recommended: run but emit a clearly-marked audit event noting the
     call would require approval once `12b` lands). Pick one interim
     behavior and document it; do **not** build suspend/wait here.
3. **Bash command-pattern classification.** Add the ordered
   (command-head, optional subcommand/flag predicate) → policy list,
   consulted by bash after the existing allowlist and redirection
   checks. Define the matching semantics precisely (e.g. `git push`
   matches `git push ...` and `git -c k=v push ...` but not
   `git push-mirror`). Default empty; `20`'s retrofit seeds the git
   patterns. A match is classified now; actual approval gating is
   `12b`.
4. **Runner snapshot + dynamic adjust.** At runner init, copy the
   app/cmd preset into a per-run policy set. Expose an explicit runner
   API to adjust the per-run set during the run (flip to `off`,
   change a capability's policy) without mutating the preset.

## Tests

- `TestPolicyPassbyRunsImmediately`.
- `TestPolicyOffRejects`.
- `TestPolicyNeedApproveInterimBehavior` — asserts the documented
  interim behavior (e.g. runs + emits the marked audit event).
- `TestBashCommandPatternMatchSemantics` — `git push` and
  `git -c k=v push` match; `git push-mirror` does not.
- `TestRunnerSnapshotIsolatesFromPreset`.
- `TestRunnerDynamicAdjustAffectsOnlyThisRun`.

## Out of scope

- No approval suspend/event/wait round-trip (that is `12b`).
- No specific git patterns yet (seeded by `20`'s retrofit).
- No MCP wiring (MCP consumes this in its own cycle).

## Honshu observation

`need_approve` interim behavior is user-facing. After tests pass, run
honshu and observe: is the "would-require-approval" signal surfaced at
a level the user finds useful, or is it noise? Record whether the
interim behavior should lean toward run-and-note vs hold, to inform the
`12b` design. UX signal only, not a gate.

## Verifiable acceptance

- New and existing tests pass; `go test ./...`, `go vet ./...`,
  `go build ./...` green.
- The policy enum, `passby`/`off` enforcement, command-pattern
  classification, and runner snapshot/adjust API exist and are
  exercised by tests.
