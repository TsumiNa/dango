# 12 — Execution Policy Runtime (code)

Kind: code. Foundation. Implements the execution-policy axis and the
approval flow from `10`.

**Prerequisite.** `11` merged.

## Goal

Add `passby` / `need_approve` / `off` to the typed tool config from
`11`, implement enforcement, the bash command-pattern policy list, the
approval event contract, and the runner snapshot + dynamic-adjustment
interface.

## Scope

1. **Policy type and config.** Extend the `11` tool config with a
   per-capability execution policy enum (`passby` default,
   `need_approve`, `off`). Applies to builtins, extras, MCP tools, and
   skills uniformly.
2. **Tool-level enforcement.** Before dispatching any tool call, the
   runtime consults the policy:
   - `passby` → run.
   - `off` → reject with a typed "disabled" error.
   - `need_approve` → run the approval flow (below).
3. **Bash command-pattern list.** Add an ordered list of
   (command-head, optional subcommand/flag predicate) → policy,
   consulted by the bash tool after the existing allowlist and
   redirection checks. Default empty; `20` seeds the git destructive
   patterns. A match of `need_approve`/`off` behaves like the
   tool-level case.
4. **Approval flow.** Per the `10` contract:
   - Publish an approval-request event on the relevant stream with the
     capability name and an argument summary.
   - Block the call until the top-level caller responds approve / deny
     / approve-for-session (the last downgrades the running policy to
     `passby`).
   - Denied → typed error returned to the model.
   - Decide and document the headless default (recommended: hold with a
     bounded timeout, then deny) and make the timeout configurable.
5. **Runner snapshot + dynamic adjust.** At runner init, copy the
   app/cmd preset into a per-run policy set. Expose an explicit runner
   API to adjust the per-run set during the run (flip to `off`,
   downgrade `need_approve`, etc.) without touching the preset.

## Tests

- `TestPolicyPassbyRunsImmediately`
- `TestPolicyOffRejects`
- `TestPolicyNeedApprovePublishesRequestAndWaits` (stub approver
  approves → runs; denies → typed error).
- `TestBashCommandPatternNeedApprove` — seeded pattern triggers the
  flow; non-matching command runs.
- `TestRunnerSnapshotIsolatesFromPreset` — adjusting the per-run set
  does not mutate the preset.
- `TestApproveForSessionDowngradesPolicy`.
- `TestHeadlessNeedApproveTimesOutToDeny` (or chosen default).

## Out of scope

- No specific destructive patterns yet (that is `20`).
- No UI for approvals beyond the stream event + response contract.
- No MCP wiring (MCP consumes this in its own subtask).

## Verifiable acceptance

- New and existing tests pass; `go test ./...`, `go vet ./...`,
  `go build ./...` green.
- A stub end-to-end shows a `need_approve` capability holding until
  approval and rejecting on denial.
