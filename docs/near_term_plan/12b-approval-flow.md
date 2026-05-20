# 12b — Approval Round-Trip for `need_approve` (code, deferred)

Kind: code. **Deferred** — do not start until an interactive approver
exists. There is no consumer for the suspend/event/wait machinery
before the app/cmd cycle ships a human-in-the-loop surface; building it
now would be a bridge to an absent shore.

**Prerequisite.** `12a` merged, **and** an approver consumer exists
(app/cmd interactive surface, or a defined programmatic approver).

## Goal

Turn the `need_approve` classification from `12a` into a real
round-trip: suspend the call, publish an approval-request event, wait
for a decision, and act on it.

## Scope (refine when the approver is real)

1. **Approval event contract.** Publish an approval-request on the
   relevant stream with the capability name and an argument summary
   (enough for a decision, not the full payload).
2. **Suspend / resume.** Hold the call until the top-level caller
   responds: approve / deny / approve-for-session (the last downgrades
   the running policy to `passby` via the `12a` dynamic-adjust API).
3. **Denied path.** Return a typed error the model can react to.
4. **Headless behavior.** Decide the default when no approver responds
   (hold-then-deny with a bounded, configurable timeout, or another
   default informed by the `12a` honshu observation). Document it.

## Tests (sketch; finalize with the approver)

- `TestNeedApprovePublishesRequestAndWaits` (stub approver approves →
  runs; denies → typed error).
- `TestApproveForSessionDowngradesPolicy`.
- `TestHeadlessNeedApproveDefault`.

## Honshu observation

This is the most user-facing subtask in the security model. Honshu is
the primary signal for: which operations genuinely warrant a pause,
how the approval prompt should read, and whether approve-for-session is
the right escape hatch. Treat honshu observations here as design input,
captured before finalizing the contract.

## Verifiable acceptance

Defined once the approver consumer is known. Until then this file is a
placeholder that keeps the `need_approve` classification in `12a` from
implying a finished feature.
