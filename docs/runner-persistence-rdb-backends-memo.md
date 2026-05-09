# Runner Persistence RDB Backends Memo

Last updated: 2026-05-09

This memo turns the deferred SQLite/Postgres backend question from
`docs/exchange-system-upgrade-memo.md` into a follow-up implementation plan for
the unified runner persistence interface.

The work should stay split across multiple PRs. Each PR must have one primary
goal, a narrow code surface, and tests that can pass before the next PR starts.

## Decision

SQLite and Postgres support should be added as explicit RDB implementations of
the unified runner persistence backend, not as ad hoc side stores and not as a
rewrite of the memo/exchange/handoff contracts.

The durable backends should preserve the same logical records as the current
unified persistence surface:

- runner lifecycle records and resume cursor state
- stream event records needed by the runner persistence lane
- exchange documents
- handoff emissions, deliveries, and referenced artifacts
- memo snapshots
- per-runner workspace metadata derived from the configured path rule

Markdown remains useful for human inspection. RDB backends are for durable
query, resume, and operational storage. Composite backends may combine an RDB
backend with Markdown when callers want both behaviors.

## Implementation Rules

- Keep the runner-owned workspace model intact. The orchestrator still passes a
  persistence handle and runner ID; the runner/backend resolves the per-runner
  path through the same path rule.
- Do not make RDB storage the only persistence shape. `None`, `Markdown`, and
  `Composite` should continue to work.
- Do not merge all artifacts into a single opaque row. Large artifact bodies may
  live in files or blobs, but metadata and routing records must be queryable.
- Keep migrations backend-owned and deterministic. Startup should fail clearly
  if a configured durable backend cannot open, migrate, or prove writability.
- Add a backend conformance test suite before adding the second database. The
  same behavioral expectations should run against Markdown, SQLite, and
  Postgres where applicable.
- Do not land a Postgres PR with only skipped tests. If Postgres support lands,
  the same PR must include a runnable local or CI test path for it.

## Data Model Sketch

The exact schema can evolve during implementation, but the RDB model should make
these records first-class:

- `runner_workspaces`: runner ID, persistence root, path-rule output,
  workspace root, created/closed timestamps.
- `runner_records`: append-only lifecycle/checkpoint records with stable
  sequence ordering.
- `stream_events`: runner-persistence event records with raw JSON payloads and
  helper columns for scope, type, source, and sequence.
- `exchange_documents`: producer, sequence, front matter, markdown body, and
  workspace-relative path.
- `handoffs`: producer, declared recipients or successor set, front matter,
  markdown body, and emission sequence.
- `handoff_deliveries`: handoff ID, successor node, delivered inbox path, and
  delivery status.
- `artifacts`: logical artifact metadata, content address or file path,
  media/type hints, size, checksum when available, and ownership.
- `memo_snapshots`: node ID, snapshot sequence, snapshot path, checksum, and
  optional compact summary.
- `snapshot_cursors`: runner/request identity, consumer name, last materialized
  event sequence, and update timestamp.

Backends should reject ambiguous duplicate appends unless the append is
provably idempotent by stable key and identical payload.

## PR Breakdown

### PR 1 - Persistence Contract And Conformance Tests

Goal: freeze the behavior expected from every backend before adding RDB code.

Change surface:

- Add a backend conformance test package under the runner persistence package.
- Define reusable fixtures for a small DAG with exchange, handoff delivery,
  artifact metadata, memo snapshot, runner record, and cursor update.
- Run the conformance suite against existing `None`, `Markdown`, and
  `Composite` behavior where applicable.
- Document which behaviors are intentionally no-ops for `None`.

Test signal:

- `go test ./internal/engine/runner/persistence/...`
- Existing runner tests still pass with the Markdown backend.
- The suite proves record ordering, duplicate handling, path-rule resolution,
  artifact metadata round-trip, and cursor update semantics.

### PR 2 - SQLite Backend Storage Layer

Goal: add SQLite as a backend implementation without changing orchestrator or
example adoption yet.

Change surface:

- Add SQLite migrations for the data model above.
- Add append/load/update methods that satisfy the unified persistence backend.
- Reuse repository-local SQLite helpers and migration style where available.
- Store large artifact bodies according to the existing persistence contract:
  path reference when the workspace owns the file, blob only if the interface
  already requires inline durable content.

Test signal:

- SQLite migration tests create a temp database from scratch and reopen it.
- The conformance suite runs against SQLite.
- Tests cover transaction rollback on partial handoff/artifact writes,
  duplicate append behavior, cursor update ordering, and reopen/load behavior.

### PR 3 - SQLite Runtime Wiring

Goal: make SQLite selectable by production wiring while keeping Markdown and
Composite behavior available.

Change surface:

- Add the constructor/configuration surface for a SQLite persistence backend.
- Wire runtime/store helpers to return either SQLite alone or
  `Composite(SQLite, Markdown)` when callers request human-readable mirrors.
- Update runner/orchestrator tests to use SQLite through the same public
  persistence option callers use.
- Keep examples unchanged unless a small option rename is required by the new
  constructor.

Test signal:

- `go test ./internal/engine/...`
- An integration test drives a small runner DAG through SQLite, closes the
  backend, reopens it, and verifies runner records, exchange documents,
  handoff deliveries, artifact metadata, memo snapshots, and cursor state.
- Existing Markdown-backed tests continue to pass.

### PR 4 - SQLite Adoption In Honshu Or Equivalent Example

Goal: prove a real consumer can opt into durable RDB persistence without
rebuilding debug artifacts by hand.

Change surface:

- Update the Honshu example or an equivalent integration example to configure
  SQLite persistence through the unified option.
- Keep Markdown mirrors only through `Composite` when human-inspection output
  is desired.
- Write compact debug summaries that point to persisted records instead of
  duplicating the backend's data model.

Test signal:

- `go test ./examples/honshu_groundwater` or the chosen integration example.
- Tests verify final deliverables still land under `Request.ArtifactsDir`.
- Tests verify durable persistence can be reopened after the run and queried
  for the runner workspace, exchange entries, handoff routes, memo snapshots,
  and cursor state.

### PR 5 - Postgres Test Harness And Backend

Goal: add Postgres support only when the PR can test it end to end.

Change surface:

- Add Postgres migrations equivalent to the SQLite schema.
- Add the Postgres backend implementation behind the same interface.
- Add a local/CI test path, such as a documented `DANGO_POSTGRES_TEST_DSN` plus
  CI service configuration or another repository-approved disposable database
  harness.
- Keep Postgres disabled unless explicitly configured.

Test signal:

- The backend conformance suite runs against Postgres in the PR's supported
  test environment.
- Migration tests cover fresh database setup and reopen.
- Tests cover transactionality for handoff plus artifact writes, duplicate
  append behavior, cursor update ordering, and load-by-runner/request queries.
- If CI cannot run Postgres tests in this PR, do not merge the backend yet.

### PR 6 - Postgres Runtime Wiring And Documentation

Goal: expose Postgres as an operational option after the backend is proven.

Change surface:

- Add runtime constructor/configuration for Postgres.
- Document required DSN/settings, migration behavior, and failure modes.
- Add a small integration test that uses the public configuration path rather
  than direct backend construction.

Test signal:

- `go test ./internal/store/... ./internal/engine/...` plus the Postgres
  integration target documented in PR 5.
- Tests prove a caller can choose `Postgres`, `Composite(Postgres, Markdown)`,
  or existing non-RDB backends without changing runner/orchestrator call sites.

## Non-Goals

- Do not change the public memo/exchange/handoff document shapes.
- Do not make Postgres a requirement for normal local tests unless the
  repository also provides a reliable local test database setup.
- Do not add a new example for every backend. One integration consumer is
  enough once backend conformance tests cover the common contract.
- Do not move persistence ownership back into examples or callers. Runner and
  orchestrator wiring remain the source of truth.
