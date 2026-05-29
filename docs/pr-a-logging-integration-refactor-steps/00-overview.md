# PR A — Per-PR Breakdown (Overview)

Drafted: 2026-05-26.

This directory splits
[`docs/pr-a-logging-integration-refactor-plan.md`](../pr-a-logging-integration-refactor-plan.md)
into three reviewable PRs. The master plan stays as the source of truth
for design rationale, format spec, and sample output. These files are
the per-PR scope, dependency, and acceptance contract.

## PR sequence

| PR | Title | Touches | Depends on |
| --- | --- | --- | --- |
| A-1 | Pretty handler | `internal/logging/handler.go` + tests (additive) | — |
| A-2 | Logging package API rewrite | `internal/logging/{logging.go,logging_test.go,doc.go}` | A-1 |
| A-3 | Engine wiring + caller migration | `internal/engine/**`, `demo/orchestrate/main.go`, `examples/honshu_groundwater/main.go` | A-2 |

Each PR is small enough for a single review pass. A-1 is purely
additive (zero risk to existing callers). A-2 is the
breaking-but-contained API rewrite. A-3 is the rename cascade across
engine + examples and must land as a single PR because removing
`WithOrchestratorLogger` / `WithAgentLogger` breaks every caller at
once.

## Why this split is safe

- `internal/logging` has **no Go consumers outside the package** today
  (verified via `grep -rn "logging\\.\(New\|DefaultConfig\|Config\|Component\|From\)" --include="*.go"`).
  This is why A-1 and A-2 can rewrite the public surface without
  touching any other tree.
- Engine code and examples build their loggers directly with
  `slog.New(...)` and pass them via `WithOrchestratorLogger` /
  `WithAgentLogger`. A-3 is the first PR where those call sites move
  to `logging.NewLogger(...)` + the unified `WithLogger(...)`.

## Out of scope for all three PRs

Carried over unchanged from the master plan §8:

- CLI binary integration (envvars, flags).
- OpenTelemetry / `stream_events.jsonl` bridge.
- Per-component log levels.
- Log rotation.

## Cross-PR checks

After A-3 lands, confirm:

- `grep -rn "WithOrchestratorLogger\|WithAgentLogger" --include="*.go"`
  returns nothing.
- `grep -rn "slog.NewTextHandler\|slog.NewJSONHandler"` returns only
  test fixtures that explicitly need raw slog handlers (if any
  survive). The default path everywhere should go through
  `logging.NewLogger`.
- `go test ./...` is green.

## Files in this directory

- `10-pretty-handler.md` — PR A-1 spec.
- `20-logging-package-api.md` — PR A-2 spec.
- `30-engine-wiring-and-migration.md` — PR A-3 spec.
