# PR A-3 — Engine Wiring and Caller Migration

Kind: code, rename cascade + call-site migration.

**Prerequisite.** PR A-2 merged (`logging.NewLogger`,
`logging.DefaultConfig` must exist).

## Goal

Unify the logger option across orchestrator / runner / agent under one
name (`WithLogger`), default to the discard logger from
`logging.DefaultConfig`, and migrate the only two callers in-tree
(`demo/orchestrate/main.go`, `examples/honshu_groundwater/main.go`) to
the new shape. After this PR, the wiring flow described in
[master plan §4](../pr-a-logging-integration-refactor-plan.md#4-wiring-flow)
is the only path.

This PR must land as a single commit/PR because removing
`WithOrchestratorLogger` and `WithAgentLogger` breaks every caller in
the same tree.

## Scope

### Orchestrator (`internal/engine/orchestrator.go`)

- Rename `WithOrchestratorLogger` → `WithLogger`.
- In `NewOrchestrator(...)`, initialize `o.logger` to
  `logging.NewLogger(logging.DefaultConfig())` before applying options.
- Doc comment on `WithLogger` states the logger propagates to every
  runner and (transitively) every agent the orchestrator constructs.

### Agent (`internal/engine/agent.go`)

- Rename `WithAgentLogger` → `WithLogger` (the option type is
  `AgentOption`, no collision with the orchestrator's `WithLogger`).
- In `NewAgent(...)`, initialize `e.logger` to
  `logging.NewLogger(logging.DefaultConfig())` before applying options.
- **Delete every `if e.logger != nil { ... }` guard** in this file —
  the logger is now always non-nil. Call `e.logger.Info(...)` directly.

### Runner

- `internal/engine/runner/runner.go`: replace the
  `logger: slog.Default()` default in the constructor with
  `logger: logging.NewLogger(logging.DefaultConfig())` so a
  test-constructed runner with no `WithLogger` also gets the preset
  discard logger.
- `internal/engine/runner/options.go`: doc-comment update on
  `WithLogger` noting it is normally injected by the orchestrator;
  direct calls are the test-only path. Signature unchanged.

### Request plumbing (`internal/engine/request.go`)

- `newRunnerFromPlan` and `buildPlanNodes` keep their `logger`
  parameter but receive `o.logger` from the orchestrator
  unconditionally (it is now always non-nil).
- Update the `WithAgentLogger(logger)` call site to
  `WithLogger(logger)`.

### Tests in `internal/engine/`

- Rename in:
  - `internal/engine/helpers_test.go`
  - `internal/engine/orchestrator_test.go`
  - `internal/engine/agent_test.go`
  - `internal/engine/queue_test.go`
- Substitutions: `WithOrchestratorLogger(testLogger)` →
  `WithLogger(testLogger)`; `WithAgentLogger(...)` →
  `WithLogger(...)`.
- **Add** `TestOrchestratorDefaultsToDiscardLogger` in
  `internal/engine/orchestrator_test.go`:
  - Construct an orchestrator with no `WithLogger` option.
  - Drive a minimal runner/agent path that exercises `logger.Info`
    (or call `o.logger.Info("x")` directly via a test-only accessor
    if needed — preferred: use the existing helper that builds and
    runs a no-op runner).
  - Capture `os.Stderr` (pipe swap) and assert nothing was written.
  - Confirms the discard default is wired end-to-end.

### Demo (`demo/orchestrate/main.go`)

- Replace the local `slog.New(slog.NewTextHandler(os.Stderr, ...))`
  construction with:
  ```go
  logger := logging.NewLogger(logging.Config{
      Level:     slog.LevelInfo,
      Output:    os.Stderr,
      AddSource: true,
  })
  ```
- Replace `orchestrate.WithOrchestratorLogger(logger)` with
  `orchestrate.WithLogger(logger)`.

### Example (`examples/honshu_groundwater/main.go`)

- Same logger-construction migration as the demo.
- If the example writes a log file in addition to stderr, use
  `sink, err := logging.OpenFileSink(path)`, defer `sink.Close()`,
  and wrap with `io.MultiWriter(os.Stderr, sink)` as the
  `Config.Output`.
- Switch to `orchestrate.WithLogger(logger)`.

## Files modified

- `internal/engine/orchestrator.go`
- `internal/engine/agent.go`
- `internal/engine/request.go`
- `internal/engine/runner/runner.go`
- `internal/engine/runner/options.go`
- `internal/engine/helpers_test.go`
- `internal/engine/orchestrator_test.go`
- `internal/engine/agent_test.go`
- `internal/engine/queue_test.go`
- `demo/orchestrate/main.go`
- `examples/honshu_groundwater/main.go`

## Files added

- None (the new test goes into an existing test file).

## Tests

- All renamed call sites must compile (`go build ./...`).
- `go test ./internal/engine/...` green.
- `go test ./...` green (catches any consumer of the removed names
  outside the audited set).
- New `TestOrchestratorDefaultsToDiscardLogger` passes.
- Manually run the demo (`go run ./demo/orchestrate`) and eyeball:
  - Timestamps, 3-letter level tokens, source column, and key=value
    attributes appear as in master plan §7.
  - Colors visible on a TTY; absent when piped to `cat`.

## Acceptance

- `grep -rn "WithOrchestratorLogger\\|WithAgentLogger" --include="*.go"`
  returns nothing.
- `grep -rn "e\\.logger != nil" internal/engine/agent.go` returns
  nothing.
- No `slog.NewTextHandler` / `slog.NewJSONHandler` constructions
  remain outside `internal/logging/` and test fixtures that explicitly
  need raw handlers (none expected after this PR).
- All `WithLogger` call sites accept a `*slog.Logger`; orchestrator
  / runner / agent each expose the same name.

## Notes for reviewer

- This PR is the only one with cross-package edits. The rename
  cascade is mechanical; the substantive review is the
  "default-to-discard" wiring (orchestrator + agent + runner all start
  with a non-nil discard logger) and the deletion of the
  `e.logger != nil` guards.
- Per
  `.github/instructions/in-branch-api-compat.instructions.md`, no
  deprecated aliases or wrapper shims are added. Old names are
  removed outright.
- If any test fixture currently relies on `e.logger == nil` as a
  side-channel (e.g. asserting no log emit by checking the field),
  rewrite the assertion to capture output through a buffer-backed
  logger instead.
