# Stream Persistence Refactor Plan

Last updated: 2026-05-08

This memo replaces the earlier deployment note with an implementation plan for
the persistence refactor that should follow the current stream and describe API
shape. The plan is intentionally split into small commits. Each phase has one
primary purpose, a narrow code surface, and tests that can pass before the next
phase begins.

## Current API Facts

The stream system is now the outward runtime observation bus:

- `stream.New(scope, cfg, opts...)` creates an append-only stream with optional
  in-memory replay and optional durable replay through `WithStore(stream.Store)`.
- `stream.Store` is deliberately narrow: `Append(ctx, event)` plus
  `Load(ctx, scope, from, filter)`.
- `Stream.Subscribe` and `Stream.Replay` expand merge bundles into logical
  events by default. `WithRawStream` exposes raw transport frames, including
  `merge.bundle`, for archive inspection and replay debugging.
- `MergeWithConfig` lets request streams collect child runner, executor, and
  skill streams through a downstream-owned merge hub. The top-level stream can
  therefore observe the full request without enabling persistence on every child
  stream.

The orchestrator and runner APIs separate runtime observation from query views:

- `Orchestrator.StartRequest` creates the request-scoped stream and returns
  immediately. Runner creation and the eventual `runner_id` are emitted on the
  stream rather than synchronously returned.
- `Orchestrator.QueryRunner`, `WaitRunner`, and `SubscribeRunnerStream` expose
  live runner state and runner stream subscriptions.
- `Orchestrator.LoadRunnerRecords` reads append-only runner lifecycle records
  from the configured `runner.RunnerStore`.
- `runner.JSONRunnerStore` is the current runner checkpoint log implementation.
  It stores init/status/event records as one JSONL file per runner.

The describe/read side is a separate persistence concern:

- `internal/store/sqlite.Store` currently owns SQLite migrations and row APIs
  for tools, tasks, edges, and logs.
- Task and edge rows are intended to support describe views that reconstruct the
  latest executable DAG without reading task directories first.
- SQLite does not yet archive request stream events, and the current request
  stream is created without a durable store.

Exchange documents remain their own artifact bus. Persistence may index or link
to exchange markdown and generated resources, but it should not merge exchange
artifact storage into the stream archive.

## Persistence Boundaries

Several persistence systems may eventually share SQLite as a backend, but they
do not share the same purpose or ownership boundary. Only one of them is stream
store persistence.

### Skill Conversation Session Persistence

Skill-owned conversation session persistence stores AI interaction history for
one runnable skill conversation. Its purpose is continuity: when an executor or
long-running skill process restarts, the skill can recover the prepared model
conversation and continue from the latest saved session state.

This persistence belongs inside the skill/conversation runtime. It is not a
request stream archive, not a describe projection, and not a general audit log
for outward system progress. It should only connect to stream events if a narrow
conversation lifecycle event family becomes part of session recovery; it should
not subscribe to or store the whole outward request stream.

### Runner Checkpoint Persistence

Runner persistence records the execution graph and its committed lifecycle
checkpoints. Its purpose is recovery after a restart outside the runner's
control: the runner should be able to reload the last certain snapshot, restore
the known DAG/node state, and continue the planned work instead of inferring
state by replaying UI/debug output.

This persistence belongs to the runner control plane. The existing
`runner.RunnerStore` append-only contract is the current durable shape, and a
future SQLite implementation should preserve its checkpoint semantics. It is
not a `stream.Store`, even if both implementations live under the same storage
package or database file.

### Request Stream Store Persistence

Stream store persistence covers only information that orchestrator, runner,
executor, and skill modules actively publish outward through the request stream
after `StartRequest`. Its purpose is observability for outer callers: during a
long-running request, API clients, terminal renderers, debug tools, and tests
should be able to subscribe late, inspect current progress, and trace what has
happened since request start.

This lane archives top-level request stream frames once. It should preserve the
raw event frames needed for replay and debugging, while letting stream replay
expand bundles into logical events for normal subscribers.

## Target Shape

The refactor should produce three explicit persistence lanes. Only the first
lane is `stream.Store` persistence:

1. **Raw request stream archive** for replay, terminal/debug inspection, and
   event-level audits. This stores top-level request stream frames once, in raw
   form, and relies on the stream package for logical expansion during replay.
2. **Runner checkpoint log** for execution graph recovery and resume-oriented
    runner records. This preserves the existing append-only `RunnerStore`
    contract while moving the durable implementation toward the shared
    persistence backend.
3. **Describe projection** for user-facing task/runner/node status, DAG shape,
   exchange summaries, and artifact references. Describe APIs should read this
   projection, not replay the full event archive.

The top-level request stream is the archive owner. Child streams should keep
their own runtime ownership and merge upward; they should not each attach their
own durable store by default. That avoids duplicate archives for one logical
request while preserving the source, scope, metadata, upstream sequence, and
bundle information needed for debugging.

## Implementation Phases

### Phase 1: Lock Current Contracts

Purpose: create a safety net before moving persistence boundaries.

Change surface:

- Add or tighten characterization tests only.
- Do not change production behavior.

Test targets:

- `internal/engine/stream`: raw `Replay` preserves `merge.bundle` frames, while
  default replay expands bundles into logical events with merged scope.
- `internal/engine`: `StartRequest` returns before runner creation, emits the
  runner-created event on the request stream, and leaves `RunnerID` empty in
  the immediate response.
- `internal/engine/runner`: `RunnerStore` still stores exchange markdown as
  `data_encoding=markdown` and preserves append-only sequence ordering.
- `internal/store/sqlite`: the current migration and tool/task/edge/log row APIs
  still open and round-trip their existing records.

Commit boundary: tests should fail only if one of the current stream, runner
record, or SQLite store contracts regresses.

### Phase 2: Add Stable Request Identity

Purpose: make request archives addressable before attaching a shared durable
store. `stream.Store.Load` scopes reads by `stream.Scope`, so archived request
streams need a stable `request_id` from the first event.

Change surface:

- Generate a request id at `StartRequest` entry before creating the request
  stream.
- Create the request stream with `stream.Scope{RequestID: ...}`.
- Add the request id to the immediate response or an equivalent existing
  describe/query entrypoint, while preserving the rule that `runner_id` is still
  discovered from stream progress.
- Ensure orchestrator, planning, runner-created, and merged child events retain
  the request id in scope.

Test targets:

- `StartRequest` returns a non-empty request id immediately.
- Replayed request stream events carry the same `scope.request_id`.
- The runner-created event carries both `request_id` and `runner_id` once the
  runner exists.
- Rejected requests still emit terminal failure events with the request id.

Commit boundary: request identity becomes observable and testable without yet
adding durable storage.

### Phase 3: Implement SQLite Request Stream Archive

Purpose: add a concrete durable archive for raw request stream frames without
changing orchestrator wiring.

Change surface:

- Add a numbered SQLite migration for a request stream event table.
- Store one raw event row per top-level stream emit. Required columns should
  support replay and describe/debug lookup: request id, stream sequence number,
  logical time, event type, source layer/id/parent id, runner id, node id,
  session id, status, timestamp, raw event JSON, and optional metadata/delta
  helper columns if they are useful for indexes.
- Add sqlc queries for append and load-by-scope/from/filter behavior.
- Add a concrete SQLite adapter that implements `stream.Store` for events. Keep
  it in the SQLite store boundary rather than in `internal/engine/stream`.

Test targets:

- `go test ./internal/store/sqlite` covers migration versioning, append/load
  ordering, scope isolation by request id, `from` sequence handling, and filter
  matching for event type/source/scope.
- A stream created with `WithStore(sqliteStreamStore)` can replay stored raw
  events after an in-memory buffer miss.
- Stored raw `merge.bundle` events can be loaded and expanded by
  `Stream.Replay` without the SQLite layer understanding bundle internals.
- Appending invalid or request-less events fails predictably if the archive
  requires request-scoped events.

Commit boundary: SQLite can act as a `stream.Store`, but no production request
path uses it yet.

### Phase 4: Attach Archives At The Request Boundary

Purpose: enable request stream persistence through orchestrator startup
configuration while keeping child streams unarchived by default.

Change surface:

- Add one startup-only orchestrator configuration entrypoint for the request
  stream store, following the existing `SetRunnerStore` style.
- `StartRequest` passes that store to the request stream through
  `stream.WithStore`.
- Do not add persistence configuration to `Request`; callers should not choose
  archive policy per request unless a later product requirement needs it.
- Do not configure runner, executor, or skill child streams with their own
  stores as part of this phase.

Test targets:

- With a configured request stream store, `StartRequest` appends planning,
  runner-created, runner, executor, skill, artifact, and terminal events through
  the top-level stream archive.
- A late subscriber can replay from SQLite after the in-memory buffer window is
  unavailable.
- Archive rows are not duplicated by child stream stores.
- Store append failures surface as stream failure behavior that callers can
  observe, without introducing `persistence.*` event types.

Commit boundary: production code can archive request streams, but describe APIs
still read their existing state sources.

### Phase 5: Move Runner Checkpoints Onto SQLite

Purpose: keep the append-only runner checkpoint contract while reducing the
split between JSONL runner logs and the shared persistence backend. This remains
runner control-plane persistence, not stream store persistence.

Change surface:

- Add SQLite tables and queries for `runner.RunnerRecord` append/load/delete.
- Implement a concrete SQLite `runner.RunnerStore` that preserves the existing
  init-first rule, monotonic `Seq`, timestamp stamping, delete behavior, and
  concurrent append safety.
- Keep `runner.JSONRunnerStore` available until application wiring and any
  migration path are deliberately changed.

Test targets:

- Reuse the current `RunnerStore` behavior tests against the SQLite
  implementation: monotonic sequence assignment, first-record validation,
  double-init rejection, missing load/delete errors, delete, and concurrent
  append serialization.
- `Orchestrator.LoadRunnerRecords` works unchanged when configured with the
  SQLite runner store.
- Exchange markdown records still round-trip as `data_encoding=markdown` and
  raw `DataText`.

Commit boundary: SQLite can replace JSONL for runner records behind the same
interface, but callers do not need to change yet.

### Phase 6: Build The Describe Projection

Purpose: make describe APIs read a compact, current-state projection instead of
depending on raw stream replay or filesystem scans.

Change surface:

- Define the smallest read model needed by current describe callers: request or
  runner status, plan/DAG nodes, node lifecycle, skill names, dependency edges,
  exchange document references, artifact/resource references, and terminal
  errors.
- Populate that read model from explicit runner lifecycle points and existing
  exchange/resource events. Use stream archive rows only as an audit/debug
  source, not as the primary describe query path.
- Keep full exchange markdown and large artifacts in files or artifact storage;
  SQLite should store summaries, metadata, and stable references.

Test targets:

- A planned request produces a describe view containing the runner id, current
  phase/status, node graph, skill names, dependencies, and artifact references.
- A rejected request produces a describe view with rejection summary/analysis
  and no fabricated runner DAG.
- Failed executor or skill phases update the describe view with terminal error
  information.
- Describe queries do not expose raw private skill implementation input or full
  unbounded LLM deltas.

Commit boundary: describe works from durable projections even when no live
stream subscriber is attached.

### Phase 7: Wire Default Persistence And Migration Policy

Purpose: make the new persistence path the application default after the pieces
are independently tested.

Change surface:

- Update CLI/server/datadir startup to open one SQLite store and configure the
  request stream archive, runner store, registry/task/edge/log store, and
  describe projection consistently.
- Choose whether existing JSONL runner logs are imported lazily, imported by an
  explicit migration command, or left as legacy debug artifacts.
- Update user-facing docs only after the runtime wiring is real.

Test targets:

- End-to-end request tests verify that one configured SQLite database can serve
  request stream replay, `LoadRunnerRecords`, and describe views.
- Restart-style tests verify that archived request events, runner records, and
  describe projections remain available after reopening the store.
- Migration or legacy behavior tests cover whatever policy is selected for old
  JSONL runner logs.

Commit boundary: SQLite-backed persistence becomes the default integrated path,
with JSONL either documented as legacy/debug storage or covered by a migration
path.

## Non-Goals

- Do not attach durable stores to every child stream by default.
- Do not make `StartRequest` wait for planning or runner creation.
- Do not turn conversation session persistence into a blanket request stream
  archive.
- Do not treat runner checkpoint persistence as request stream persistence.
- Do not add `persistence.*` stream event types unless a user-visible archive
  lifecycle later needs observable state.
- Do not make describe APIs replay arbitrary raw stream history for normal
  reads.
- Do not store full generated artifacts in SQLite when a stable file/resource
  reference is enough.

## Validation Matrix

Run the focused package tests after each phase, then broaden only when wiring
crosses package boundaries:

- Stream archive behavior: `go test ./internal/engine/stream`
- SQLite schema, sqlc, and archive adapters: `go test ./internal/store/sqlite`
- Runner record implementations: `go test ./internal/engine/runner`
- Orchestrator request wiring and describe integration: `go test ./internal/engine`
- Final integrated persistence path: `go test ./...`