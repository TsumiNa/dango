# Phase 7 Consumer Refactor Plan

Last updated: 2026-05-08

This memo reviews the draft plan for aligning consumer surfaces with the Phase
7 startup persistence work, then refines it into an implementation-ready
refactor plan. The intended consumers are the reusable terminal renderer in
`internal/streamrender` and the runnable Honshu groundwater example in
`examples/honshu_groundwater`.

## Executive Decision

`internal/streamrender` should remain a live-stream renderer. It already
matches the normalized request-stream model closely enough for Phase 7, so it
does not need a persistence-driven rewrite.

`examples/honshu_groundwater` should be refactored to consume the new
startup-owned persistence surface. Today it demonstrates live observation and
skill execution well, but it does not yet prove request event-log persistence,
runner checkpoint persistence, describe replay, cursor persistence, or reopen
behavior.

The refactor should therefore center on the example, with only small renderer
changes if the example exposes a concrete rendering gap.

## Review Of The Original Draft

The original draft made the right high-level call: streamrender is mostly
aligned, while the Honshu example is not. It needed more precision in four
places before it could guide implementation.

- It did not state the prerequisite that the Phase 7 engine APIs and runtime
  persistence package must be present before the example can consume them.
- It did not distinguish the example's existing debug JSONL archive from the
  new startup-owned request event log. Those are separate artifacts with
  separate purposes.
- It did not define where persistence-owned artifacts should live or what
  compact summaries the example should write for inspection.
- It did not name the tests that must prove persistence flush, replay, and
  reopen behavior instead of only live execution.

This memo keeps the original conclusion but tightens the sequencing, file
boundaries, test expectations, and risk controls.

## Prerequisites

Implement this plan only on a branch that contains the Phase 7 engine surface:

- `internal/store/runtime.Open` and its `EventLogStore`, `RunnerStore`, and
  `SnapshotCursorStore` accessors
- `Orchestrator.SetEventLogStore`
- `Orchestrator.SetRunnerStore`
- `Orchestrator.SetSnapshotCursorStore`
- `Orchestrator.DescribeRequest`
- `Orchestrator.LoadRunnerRecords` support for persisted runner records without
  requiring a live in-memory runner

If this memo is being edited on `main` before those APIs merge, the actual
implementation should happen after rebasing onto the Phase 7 branch or after
Phase 7 lands on `main`.

## Current Consumer State

### internal/streamrender

The renderer is already shaped correctly for the Phase 7 stream model.

- It consumes stream subscriptions instead of runner-specific callbacks.
- Its observed-subscription API lets callers archive exactly the events they
  render without introducing a second notification path.
- Its tests cover expanded `merge.bundle` delivery, so bundled runner lifecycle
  events are rendered as logical runner updates instead of raw merge traffic.
- It already hides low-level tool noise and token usage while surfacing
  orchestrator planning, reasoning/output, runner phases, executor lifecycle,
  artifacts, and exchange markdown references.

The renderer should not learn how to open stores, replay persisted logs, show
snapshot cursors, or browse describe views. Those responsibilities belong to
example code or future debug tooling.

### examples/honshu_groundwater

The example is a strong live integration exercise, but it still stops at the
pre-persistence observation boundary.

- It creates an orchestrator, registers skills, starts a request, renders the
  live request stream, waits for the runner, and verifies generated artifacts.
- It writes a separate debug JSONL stream archive under the example artifacts
  tree.
- It does not open the Phase 7 runtime persistence bundle.
- It does not configure the orchestrator with event-log, runner, and cursor
  stores before `StartRequest` locks startup configuration.
- It does not wait for persisted terminal request state before closing
  persistence.
- It does not call `DescribeRequest` after settlement.
- It does not reopen a fresh orchestrator against persisted stores to prove
  request replay and runner record loading after the live run is gone.

## Refactor Goals

The refactor should make the example demonstrate three things in one coherent
workflow.

- Live UX: the request stream still renders compactly through
  `internal/streamrender` while the user-facing example runs.
- Runtime persistence: the orchestrator uses startup-owned stores for request
  event logs, runner records, and describe cursors.
- Reopen evidence: tests prove that a fresh orchestrator can reconstruct useful
  state from persisted data after the original run completes.

The result should feel like a real client adopting persistence, not a one-off
test harness embedded in the executable path.

## Target Artifact Layout

Use the example artifacts directory as the single durable root.

```text
artifacts/
  debug/
    stream_events.jsonl
    describe_view.json
    runner_records.json
    persistence_summary.json
  exchanges/
    exchange-*.md
  persistence/
    dango.db
  <skill-owned outputs>
```

The existing debug JSONL archive stays useful for human inspection and renderer
debugging. It must not be treated as the source of truth for Phase 7 replay.
The SQLite database under `artifacts/persistence/` is the durable persistence
surface used by `DescribeRequest` and `LoadRunnerRecords`.

## File-Level Boundary

Expected implementation files:

- `examples/honshu_groundwater/main.go`: open persistence, configure stores,
  wait for persisted terminal state, write compact debug summaries.
- `examples/honshu_groundwater/main_test.go`: add persistence/reopen assertions
  to the existing fake-LLM integration path.
- `examples/honshu_groundwater/design_purpose.md`: update the example purpose
  to mention startup persistence and replay if the executable behavior changes.

Allowed only if a concrete gap appears:

- `internal/streamrender/renderer.go`: small formatting changes for events the
  example already renders live.
- `internal/streamrender/renderer_test.go`: focused regression tests for any
  renderer behavior changed above.

Out of scope:

- skill directories and Python scripts, unless a persistence artifact needs a
  path correction caused by the example wiring itself
- core stream semantics
- core persistence semantics
- SQLite schema or query changes
- new domain capabilities, new skills, new CLI modes, or new report outputs

## Phased Implementation Plan

### Phase 0: Establish The Consumer Contract

Goal: make the implementation branch start from an explicit contract instead
of smuggling behavior into tests.

- Add or update a small internal result type returned by
  `runHonshuGroundwaterExample` only if the current return value cannot expose
  `RequestID`, `RunnerID`, artifact paths, and persistence path to tests.
- Keep this type private to the example package.
- Do not expose a public framework-level abstraction for example persistence.

Exit condition:

- Tests can inspect the example run's request ID, runner ID, artifacts root,
  persistence path, and final runner view without parsing stdout.

### Phase 1: Wire Startup-Owned Runtime Persistence

Goal: make the example use the same persistence ownership model as production
startup code.

- Derive the default SQLite path from the artifacts directory:
  `artifacts/persistence/dango.db`.
- Open `internal/store/runtime` before constructing or configuring the
  orchestrator stores.
- Configure `SetEventLogStore`, `SetRunnerStore`, and
  `SetSnapshotCursorStore` before `StartRequest`.
- Close persistence only after the request event log has observed terminal
  runner state. Do not rely on `Runner.Wait` alone as evidence that the async
  request event-log worker has flushed.
- Keep existing stream rendering and JSONL debug archiving intact.

Exit condition:

- A normal example run creates a durable SQLite database under
  `artifacts/persistence/`.
- The live terminal stream output remains compact and does not expose raw
  request payloads, raw bundle frames, token-usage noise, or full snapshots.

### Phase 2: Consume Persisted State After Settlement

Goal: make the example visibly prove that persisted request state is usable
after the live run settles.

- After runner settlement and persisted terminal-state confirmation, call
  `DescribeRequest(ctx, requestID)`.
- Load runner records with `LoadRunnerRecords(ctx, runnerID)`.
- Write compact machine-readable summaries:
  `debug/describe_view.json`, `debug/runner_records.json`, and
  `debug/persistence_summary.json`.
- Keep stdout focused on live progress. Use stderr logs for short milestones
  such as persistence opened, request persisted, describe replay completed, and
  summaries written.

Exit condition:

- The describe view is reconstructed from the persisted request event log.
- A snapshot cursor is saved by the describe path.
- Persisted runner records are available without consulting the live runner
  object.
- The debug artifacts contain enough IDs, counts, cursor sequence values, and
  file paths to inspect the persistence path without dumping large payloads to
  the terminal.

### Phase 3: Reopen And Replay In Tests

Goal: prove the example exercises durable replay, not just in-process stores.

- Extend the existing fake-LLM example test to run with a temporary artifacts
  directory and a durable SQLite persistence path.
- After the run closes its first persistence handle, reopen runtime persistence
  from the same database path.
- Configure a fresh orchestrator with the reopened stores.
- Verify `LoadRunnerRecords(ctx, runnerID)` returns the expected persisted
  runner lifecycle records.
- Verify `DescribeRequest(ctx, requestID)` rebuilds a settled view with the
  expected runner ID and nonzero cursor event sequence.
- Verify the persisted request event log contains a terminal settled phase
  frame before the first persistence handle closes.

Exit condition:

- `go test ./examples/honshu_groundwater` proves live execution, persisted
  terminal-state flush, reopen runner-record loading, describe replay, and
  cursor persistence.

### Phase 4: Streamrender Regression Check

Goal: keep the renderer honest without broadening its ownership.

- Run the existing `internal/streamrender` tests after the example refactor.
- Add a renderer test only if the refactored example reveals a concrete live
  rendering problem, such as raw `merge.bundle` leakage, repeated noisy lines,
  or missing compact status for an already-supported event family.
- Do not add store-opening, describe-view rendering, or persistence-browser
  behavior to `internal/streamrender`.

Exit condition:

- `go test ./internal/streamrender` still passes.
- Any renderer changes are tied to live subscription rendering only.

## Test Matrix

Run the narrow tests first, then the broader package checks.

```text
go test ./examples/honshu_groundwater -run 'TestRunHonshuGroundwaterExampleExecutesNeededSkills|Test.*Persistence|Test.*Reopen'
go test ./examples/honshu_groundwater
go test ./internal/streamrender
go test ./internal/store/runtime ./internal/engine
go test ./...
```

The exact focused test names may differ, but the coverage must include:

- the current skill script tests
- the current fake-LLM live example path
- persisted terminal request-state detection
- describe replay from persisted request frames
- runner record loading from reopened persistence
- cursor save/load after describe replay
- no accidental PDF skill selection for the current user request
- no streamrender regression in compact live output

## Risk Controls

- Async persistence flush: wait for persisted settled-phase evidence before
  closing persistence. Do not assume `Runner.Wait` means the request event log
  has flushed.
- Artifact growth: write summaries and paths, not full stream payload copies in
  multiple formats.
- Stream/render coupling: keep renderer changes reactive to proven output gaps;
  do not make streamrender depend on the store/runtime packages.
- Example scope creep: do not introduce new CLI flags unless tests cannot
  provide the needed persistence path through private example configuration.
- Branch compatibility: if this work starts before Phase 7 lands on `main`,
  implement it on top of the Phase 7 branch to avoid compatibility shims.

## Non-Goals

- No generic persistence framework for all examples.
- No debug UI or persistence browser.
- No new stream event families.
- No SQLite schema changes.
- No changes to skill behavior or domain outputs.
- No replacement of the existing debug JSONL archive.

## Completion Criteria

The refactor is complete when all of the following are true.

- The Honshu example opens startup-owned runtime persistence and wires all
  three stores before request startup.
- The example still renders compact live progress through `internal/streamrender`.
- The example writes compact persistence/describe summaries under `artifacts/`.
- Tests reopen persisted state with a fresh orchestrator and verify runner
  records plus describe replay.
- `internal/streamrender` has no persistence ownership and no broad rewrite.
- The focused example tests, streamrender tests, and broader repository tests
  pass.