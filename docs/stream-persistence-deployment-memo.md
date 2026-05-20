# Stream Persistence Refactor Plan

Last updated: 2026-05-08

This memo replaces the earlier deployment note with an implementation plan for
the persistence refactor that should follow the current stream and describe API
shape. The plan is intentionally split into small commits. Each phase has one
primary purpose, a narrow code surface, and tests that can pass before the next
phase begins.

## Current API Facts

The stream system is now the outward runtime observation bus:

- `stream.New(scope, cfg, opts...)` creates an append-only stream with
  in-memory replay. Durable event-log storage is outside the stream runtime.
- `internal/store.EventLogStore` is the event-log persistence abstraction:
  `AppendEvent(ctx, event)` plus `LoadEvents(ctx, scope, from, filter)`.
- `Stream.Subscribe` and `Stream.Replay` expand merge bundles into logical
  events by default. `WithRawStream` exposes raw transport frames, including
  `merge.bundle`, for event-log inspection and replay debugging.
- `MergeWithConfig` lets request streams collect child runner, agent, and
  skill streams through a downstream-owned merge hub. The top-level stream can
  therefore observe the full request without enabling persistence on every child
  stream.

This branch moves event-log persistence out of stream delivery. It does not yet
start a request-scoped persistence worker. Later phases should subscribe to the
top-level request stream from independent goroutines so persistence cannot
block or change delivery to normal subscribers.

The orchestrator and runner APIs separate runtime observation from query views:

- `Orchestrator.StartRequest` creates the request-scoped stream and returns
  immediately. Runner creation and the eventual `runner_id` are emitted on the
  stream rather than synchronously returned.
- `Orchestrator.QueryRunner`, `WaitRunner`, and `SubscribeRunnerStream` expose
  live runner state and runner stream subscriptions.
- `Orchestrator.LoadRunnerRecords` reads append-only runner lifecycle records
  from the configured `runner.RunnerStore`.
- `runner.JSONRunnerStore` is the current checkpoint log implementation.
  It stores init/status/event records as one JSONL file per runner.

The describe/read side is a separate persistence concern:

- `internal/store/sqlite.Store` currently owns SQLite migrations and row APIs
  for tools, tasks, edges, and logs.
- Task and edge rows are intended to support describe views that reconstruct the
  latest executable DAG without reading task directories first.
- SQLite now has a request stream event table and store adapter, but the
  current production request stream is still created without an event-log
  persistence worker.

Exchange documents remain their own artifact bus. Persistence may index or link
to exchange markdown and generated resources, but it should not merge exchange
artifact storage into the event log.

## Persistence Boundaries

Several persistence systems may eventually share SQLite as a backend, but they
do not share the same purpose or ownership boundary. Only the event log uses the
stream event-log persistence contract.

On this branch, `internal/store` owns the event-log persistence abstraction and
`internal/store/sqlite` provides the SQLite implementation. Other persistence
lanes should move behind the same package boundary when they are refactored.
This branch does not yet provide a JSON event-log implementation.

### Skill Conversation Session Persistence

Skill-owned conversation session persistence stores AI interaction history for
one runnable skill conversation. Its purpose is continuity: when an agent or
long-running skill process restarts, the skill can recover the prepared model
conversation and continue from the latest saved session state.

This persistence belongs inside the skill/conversation runtime. It is not an
event log, not a snapshot cursor, and not a general audit log for outward system
progress. It should only connect to stream events if a narrow
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
not part of stream runtime persistence, even if both implementations live under
the same storage package or database file.

### Event Log Persistence

Event log persistence covers only information that orchestrator, runner,
agent, and skill modules actively publish outward through stream events after
`StartRequest`. Its purpose is observability for outer callers: during a
long-running request, API clients, terminal renderers, debug tools, and tests
should be able to subscribe late, inspect current progress, and trace what has
happened since request start.

This lane records top-level request stream frames once. It should preserve the
raw event frames needed for replay and debugging, while letting stream replay
expand bundles into logical events for normal subscribers. Event-log writes are
not themselves stream events. Persistence startup, lag, write failure, and
shutdown diagnostics belong in the component logger, not in externally visible
`persistence.*` events.

## Target Shape

The refactor should produce three explicit persistence lanes. Only the first
lane is event-log persistence:

1. **Event log** for replay, terminal/debug inspection, and event-level audits.
   This stores top-level request stream frames once, in raw form, and relies on
   the stream package for logical expansion during replay.
2. **Checkpoint log** for execution graph recovery and resume-oriented
   runner records. This preserves the existing append-only `RunnerStore`
   contract while moving the durable implementation toward the shared
   persistence backend.
3. **Snapshot cursor** for describe resume. This records the last event sequence
   a describer has materialized, not a full current-state table. A describe view
   should rebuild from the latest checkpoint plus event log replay after that
   checkpoint.

The top-level request stream owns the event log. Child streams should keep
their own runtime ownership and merge upward; they should not each attach their
own durable store by default. That avoids duplicate event logs for one logical
request while preserving the source, scope, metadata, upstream sequence, and
bundle information needed for debugging.

Persistence must not introduce a synchronous choice between writing the store
and delivering stream events. Event delivery remains the stream package's
responsibility. Durable writers subscribe or otherwise receive frames as
independent goroutines, record them through the configured store, and report
internal diagnostics through logging. If the configured persistence backend
cannot be opened, migrated, or proven writable at startup, the application
should refuse to accept requests instead of starting and later surfacing
per-event persistence failures to subscribers.

## Default And Configured Persistence

The application should always have a local JSON persistence fallback, even when
the user does not configure persistence. Startup should create temporary JSON
stores for the event log and checkpoint log, use them for the whole process
lifetime, and clean their temporary directories on normal exit. This default
JSON fallback is only a lifetime-local replay and inspection aid. It does not
promise restart recovery and should not be treated as durable history after the
process exits. That fallback is still planned rather than implemented on this
branch; when added, it should live behind `internal/store` rather than as part
of the stream package API.

User-configured persistence is the durable path. A configured JSON store or RDB
store should record all event log rows, checkpoint log rows, and snapshot cursor
rows needed for closing, reopening, and querying from outside the process. When
both JSON and RDB persistence are configured, the persistence system should
prioritize safe RDB writes before secondary JSON behavior. Replay after an
in-memory buffer miss should use RDB as the authoritative replay source so the
temporary or secondary JSON path does not shadow durable database history.

Both JSON and RDB persistence can contain incomplete writes after a crash. On
startup, corrupt records should produce a diagnostic that identifies the broken
store, request or runner id when known, file/table, sequence, and decode or
integrity error. Startup should not silently discard history and should not only
return a generic failure. The user should be able to choose how to proceed, such
as deleting the broken history or using an external recovery tool to restore a
usable store.

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

Purpose: make event logs addressable before attaching a shared durable
store. Event-log reads scope by request identity, so recorded request streams
need a stable `request_id` from the first event.

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

### Phase 3: Implement SQLite Event Log

Purpose: add a concrete durable event log for raw request stream frames without
changing orchestrator wiring.

Change surface:

- Add a numbered SQLite migration for a request stream event table.
- Store one raw event row per top-level stream emit. Required columns should
  support replay and describe/debug lookup: request id, stream sequence number,
  logical time, event type, source layer/id/parent id, runner id, node id,
  session id, status, timestamp, raw event JSON, and optional metadata/delta
  helper columns if they are useful for indexes.
- Add sqlc queries for append and load-by-scope/from/filter behavior.
- Define the event-log store abstraction under `internal/store` and add a
  concrete SQLite adapter under `internal/store/sqlite`. Keep concrete storage
  implementations out of `internal/engine/stream`.

Test targets:

- `go test ./internal/store/sqlite` covers migration versioning, append/load
  ordering, scope isolation by request id, `from` sequence handling, and filter
  matching for event type/source/scope.
- Loaded SQLite event rows preserve raw frames for replay/debug callers without
  making SQLite understand stream bundle internals.
- Stored raw `merge.bundle` events can be loaded and decoded by stream helpers
  without the SQLite layer understanding bundle internals.
- Appending invalid or request-less events fails predictably if the event log
  requires request-scoped events.

Commit boundary: SQLite can act as an event-log store, but no production
request path uses it yet.

### Phase 4: Attach The Event Log At The Request Boundary

Purpose: enable request stream persistence through orchestrator startup
configuration while preserving stream delivery semantics, the default temporary
JSON fallback, and the rule that child streams do not own event logs by default.

Change surface:

- Use the `internal/store.EventLogStore` abstraction implemented by
  `internal/store/sqlite` with the Phase 3 request stream event table. Keep the
  stream runtime independent of concrete persistence backends.
- Add one startup-only orchestrator/application configuration entrypoint for
  event-log persistence, following the existing startup-only configuration
  style.
- Startup must open, migrate, and perform a minimal write/read health check for
  the configured event-log store before accepting requests. If this check
  fails, startup returns an error and no request is processed.
- When no user store is configured, startup creates a process-lifetime
  temporary JSON event-log fallback and removes its directory on normal exit.
  This fallback does not exist on this branch yet; Phase 4 should introduce it
  behind the `internal/store` abstraction rather than under `internal/engine/stream`.
- Start one or more persistence goroutines that subscribe to top-level request
  streams and append raw request stream frames through the configured event-log
  store. These goroutines must not block stream delivery to normal subscribers.
- If both JSON and RDB stores are configured, the persistence configuration
  treats RDB as authoritative for durability and replay after in-memory misses.
  Secondary JSON behavior must not make an unsafe RDB write look successful.
- Persistence worker failures are logged through the component logger with
  request id, stream sequence, store/table/file, and error context when known.
  They are not emitted as stream events and do not close subscriber streams.
- Do not add persistence configuration to `Request`; callers should not choose
  event-log policy per request unless a later product requirement needs it.
- Do not configure runner, agent, or skill child streams with their own
  stores as part of this phase.

Test targets:

- With a configured event-log store, `StartRequest` appends planning,
  runner-created, runner, agent, skill, artifact, and terminal events through
  the top-level event log without delaying normal stream subscribers.
- Without configured persistence, `StartRequest` appends event rows to a
  temporary JSON store for the process lifetime and cleanup removes the temp
  directory on normal exit.
- A late observer can load prior request frames from SQLite through
  `internal/store.EventLogStore` and then subscribe to the live request stream
  for new events.
- Startup fails before accepting requests when the configured RDB or JSON store
  cannot be opened, migrated, or proven writable.
- With both JSON and RDB configured, RDB health/write failure prevents the
  system from treating secondary JSON success as durable, and replay after an
  in-memory miss reads from RDB.
- Event log rows are not duplicated by child stream stores.
- Persistence worker append failures are captured by logs and internal health
  state, not by `persistence.*` events or subscriber-visible stream failures.

Commit boundary: production code can record request streams through an
independent persistence participant, while stream subscribers remain unaware of
persistence and describe APIs still read their existing state sources.

### Phase 5: Move Runner Checkpoints Onto SQLite

Purpose: keep the append-only runner checkpoint contract while reducing the
split between durable JSONL runner logs and the shared persistence backend. This
remains runner control-plane persistence, not stream store persistence. The
process-lifetime temporary JSON checkpoint fallback remains available even when
no durable store is configured.

Change surface:

- Add SQLite tables and queries for `runner.RunnerRecord` append/load/delete.
- Implement a concrete SQLite `runner.RunnerStore` that preserves the existing
  init-first rule, monotonic `Seq`, timestamp stamping, delete behavior, and
  concurrent append safety.
- Keep `runner.JSONRunnerStore` available until application wiring and any
  migration path are deliberately changed.
- On startup, report corrupt or partially written checkpoint records with enough
  detail for the user to choose deletion or external repair instead of returning
  only a generic load error.

Test targets:

- Reuse the current `RunnerStore` behavior tests against the SQLite
  implementation: monotonic sequence assignment, first-record validation,
  double-init rejection, missing load/delete errors, delete, and concurrent
  append serialization.
- `Orchestrator.LoadRunnerRecords` works unchanged when configured with the
  SQLite runner store.
- Exchange markdown records still round-trip as `data_encoding=markdown` and
  raw `DataText`.
- Corrupt checkpoint logs identify the affected runner id and record sequence
  when that information can be recovered.

Commit boundary: SQLite can serve as the durable checkpoint log behind the same
interface, while the default JSON fallback remains lifetime-local.

### Phase 6: Add Snapshot Cursor

Purpose: avoid a separate state table. A live describer can keep its
current view in memory, and durable resume only needs to know the last stream
event sequence that has been materialized.

Change surface:

- Define the smallest cursor record needed by describe callers: request id,
  runner id when known, latest checkpoint sequence, and latest stream event
  sequence.
- Persist the cursor when a describer stops subscribing and when the runner
  writes a checkpoint, so a crash restart can resume from the latest certain
  checkpoint and replay only later event log rows.
- Rebuild the user-facing describe view from the latest checkpoint plus event
  log replay after the cursor. Do not add tables that duplicate request,
  runner, node, DAG, or artifact state unless a later query proves replay from
  the cursor is too expensive.
- Keep full exchange markdown and large artifacts in files or artifact storage;
  SQLite should store summaries, metadata, and stable references.

Test targets:

- A planned request produces a describe view containing the runner id, current
  phase/status, node graph, skill names, dependencies, and artifact references.
- Replaying from the stored cursor skips events already materialized and applies
  later event log rows in sequence-number order.
- A cursor written alongside a checkpoint lets describe rebuild from that
  checkpoint after reopening the store.
- A cursor written when a describer cancels lets a later describe call resume
  without replaying from the beginning of the request.
- Describe queries do not expose raw private skill implementation input or full
  unbounded LLM deltas.

Commit boundary: describe can resume from a durable cursor and the event log
without maintaining a separate current-state table.

### Phase 7: Wire Default Persistence And Migration Policy

Purpose: wire one consistent startup policy after the pieces are independently
tested: temporary JSON fallback by default, durable user-configured persistence
when provided, and clear recovery diagnostics for broken durable stores.

Change surface:

- Update CLI/server/datadir startup to create temporary JSON stores when the
  user does not configure persistence, keep them for the process lifetime, and
  remove them on normal exit.
- When the user configures durable persistence, open and configure the selected
  JSON and/or RDB stores for the event log, checkpoint log, registry/task/edge
  stores, and snapshot cursor consistently.
- When both JSON and RDB durable stores are configured, prioritize safe RDB
  writes and use RDB for replay after in-memory misses.
- Choose whether existing durable JSONL runner logs are imported lazily,
  imported by an explicit migration command, or left as legacy debug artifacts.
- Update user-facing docs only after the runtime wiring is real.

Test targets:

- End-to-end request tests verify that one configured SQLite database can serve
  request stream replay, `LoadRunnerRecords`, and describe views.
- Default-startup tests verify that temporary JSON stores are created, used for
  lifetime-local replay, and cleaned up on normal exit.
- Restart-style tests verify that recorded events, runner records, and snapshot
  cursors remain available after reopening user-configured durable stores.
- Startup-diagnostics tests cover corrupt JSON and RDB histories and assert that
  failures identify the affected store and recoverable location instead of
  silently discarding history or returning an opaque error.
- Dual-store tests verify that RDB write safety is prioritized over secondary
  JSON success and that replay after in-memory misses reads from RDB.
- Migration or legacy behavior tests cover whatever policy is selected for old
  JSONL runner logs.

Commit boundary: default startup has lifetime-local JSON persistence, while
user-configured durable persistence supports reopen, external query, and
diagnosable recovery decisions.

## Non-Goals

- Do not attach durable stores to every child stream by default.
- Do not make `StartRequest` wait for planning or runner creation.
- Do not turn conversation session persistence into a blanket event log.
- Do not treat runner checkpoint persistence as request stream persistence.
- Do not treat the default temporary JSON fallback as restart recovery.
- Do not silently discard corrupt configured persistence on startup.
- Do not add `persistence.*` stream event types unless a user-visible event-log
  lifecycle later needs observable state.
- Do not make describe APIs replay arbitrary raw stream history for normal
  reads; resume from the latest checkpoint and stored event sequence instead.
- Do not store full generated artifacts in SQLite when a stable file/resource
  reference is enough.

## Validation Matrix

Run the focused package tests after each phase, then broaden only when wiring
crosses package boundaries:

- Event log behavior: `go test ./internal/engine/stream`
- SQLite schema, sqlc, and event log adapters: `go test ./internal/store/sqlite`
- Runner record implementations: `go test ./internal/engine/runner`
- Orchestrator request wiring and describe integration: `go test ./internal/engine`
- Final integrated persistence path: `go test ./...`