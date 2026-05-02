# Stream Refactor Memo

Last updated: 2026-05-02

This memo records the planned refactor for cross-layer output and event
delivery across orchestrator, runner, executor, skill, and conversation code.
Keep this file current while the branch evolves so parallel coding agents can
share the same design assumptions and migration status.

## Problem

The Honshu groundwater example can now complete, but the output is still hard
to inspect. The visible symptoms are formatting problems, but the root cause is
that information crosses layers through ad hoc channels:

- Orchestrator planning deltas use a request-specific callback.
- Runner updates expose large snapshots that are useful for persistence but
  too noisy for terminal consumers.
- Executor and skill progress is mostly hidden until a phase completes.
- Conversation streaming exposes token deltas, but those events are not part of
  the same event system as runner/executor progress.
- LLM provider streaming and the internal stream bus can be confused: provider
  streaming is only one possible input transport, while the engine stream is
  the normalized output channel every caller subscribes to.
- Persistence and replay are separate mechanisms instead of ordinary stream
  subscribers.

The result is that callers either see too little during long waits, or they see
raw internal structures with duplicated context and full user payloads.

## Goal

Build one independent, reusable stream system that lets every layer publish
structured event chunks outward and lets multiple subscribers consume the
events they care about.

The initial refactor target is output information transfer, not terminal UI
polish. Terminal rendering and saved artifact formatting should become simple
subscribers on top of the stream system.

## Design Principles

- Every outward-facing chunk is JSON unmarshalable.
- Every chunk is independently useful: it identifies event type, source,
  sequence number, status, and payload delta.
- Producers write to a stream writer instead of directly writing terminal
  output, log files, runner snapshots, or conversation sessions.
- Subscribers can attach and detach at any time.
- Subscribers can filter by event type, source, status, run/session IDs, or
  other stable metadata.
- Outer observability persistence is a subscriber, not a special side path.
- Conversation session persistence remains a lifecycle state log, not a full
  stream archive.
- Replay/catch-up is part of the stream design, so late subscribers can rebuild
  context without asking each layer for custom state.
- The system should handle high-volume token streams without forcing every
  consumer to receive full snapshots.

## Terminology

- Producer: any component that emits stream events. Examples: orchestrator,
  runner, executor, skill, conversation, tool dispatcher.
- Stream: append-only event bus for one logical task/run/session scope.
- Subscriber: a consumer that receives events from a stream. Examples:
  terminal renderer, NDJSON writer, database writer, debugging UI. A
  conversation session store is only a subscriber if it is restricted to
  conversation lifecycle state events.
- Replay: delivery of already-emitted events to a new subscriber from the
  stream buffer or backing store.
- Delta: incremental payload content. For LLM text this is a token/string
  fragment; for status updates it may be a concise message; for tool calls it
  may be structured JSON.

## Event Shape

All chunks must be valid JSON objects. The baseline event should be stable and
small:

```json
{
  "event_type": "llm.output.delta",
  "from": {
    "layer": "conversation",
    "id": "conv_123",
    "parent_id": "exec_456"
  },
  "sequence_number": 42,
  "status": "running",
  "delta": "partial text",
  "timestamp": "2026-05-02T00:00:00.000Z",
  "scope": {
    "request_id": "req_abc",
    "runner_id": "run_def",
    "node_id": "train_model",
    "session_id": "sess_xyz"
  },
  "metadata": {
    "model": "gpt-...",
    "tool_name": "bash"
  }
}
```

Required fields:

- `event_type`: machine-readable type string.
- `from`: producer identity. At minimum it must identify the layer.
- `sequence_number`: monotonically increasing within the stream.
- `status`: producer or operation status at the time of emission.
- `delta`: payload fragment. May be string, object, array, number, bool, or
  null, but must be JSON serializable.

Recommended fields:

- `timestamp`: wall-clock event time.
- `scope`: stable correlation IDs.
- `metadata`: compact structured context needed for filtering or display.

Avoid putting large snapshots, full prompts, full user payloads, or full file
contents in `delta` by default. Use artifact references or resource metadata
for large data.

## Event Types

Use dotted names so filters can match prefixes.

Initial event families:

- `status.started`
- `status.progress`
- `status.completed`
- `status.failed`
- `llm.reasoning.delta`
- `llm.output.delta`
- `llm.tool_call.started`
- `llm.tool_call.delta`
- `llm.tool_call.completed`
- `llm.tool_result.delta`
- `tool.execution.started`
- `tool.execution.completed`
- `tool.execution.failed`
- `runner.phase.changed`
- `runner.node.started`
- `runner.node.completed`
- `runner.node.failed`
- `executor.polish.started`
- `executor.polish.completed`
- `executor.execute.started`
- `executor.execute.completed`
- `executor.report.started`
- `executor.report.completed`
- `skill.memo.delta`
- `artifact.created`
- `persistence.appended`

These names are a starting point. Keep the set compact until real consumers
need more detail.

## Proposed Go API

Package location:
`internal/engine/stream`.

Core types:

```go
type Event struct {
    EventType      string         `json:"event_type"`
    From           Source         `json:"from"`
    SequenceNumber uint64         `json:"sequence_number"`
    Status         string         `json:"status"`
    Delta          json.RawMessage `json:"delta"`
    Timestamp      time.Time      `json:"timestamp,omitempty"`
    Scope          Scope          `json:"scope,omitempty"`
    Metadata       map[string]any `json:"metadata,omitempty"`
}

type Source struct {
    Layer    string `json:"layer"`
    ID       string `json:"id,omitempty"`
    ParentID string `json:"parent_id,omitempty"`
}

type Scope struct {
    RequestID string `json:"request_id,omitempty"`
    RunnerID  string `json:"runner_id,omitempty"`
    NodeID    string `json:"node_id,omitempty"`
    SessionID string `json:"session_id,omitempty"`
}
```

Producer API draft:

```go
type Stream struct { ... }

func New(scope Scope, opts ...Option) *Stream
func (s *Stream) Emit(ctx context.Context, event Event) error
func (s *Stream) Writer(source Source, status func() string) *Writer

type Writer struct { ... }
func (w *Writer) Emit(ctx context.Context, eventType string, delta any, metadata map[string]any) error
func (w *Writer) Status(ctx context.Context, status string, delta any) error
```

Subscriber API draft:

```go
type Filter struct {
    EventTypes []string
    Prefixes   []string
    Sources    []SourceSelector
    Statuses   []string
    Scope      Scope
}

type Subscription struct { ... }

func (s *Stream) Subscribe(filter Filter, opts ...SubscribeOption) (*Subscription, error)
func (sub *Subscription) Events() <-chan Event
func (sub *Subscription) Next(ctx context.Context) (Event, bool, error)
func (sub *Subscription) Cancel()
```

Replay options:

```go
func WithReplayFrom(sequence uint64) SubscribeOption
func WithReplayLast(n int) SubscribeOption
func WithNoReplay() SubscribeOption
```

Stream merge API:

```go
func (s *Stream) MergeFrom(ctx context.Context, upstream *Stream, filter Filter, opts ...SubscribeOption) (*Merge, error)

type Merge struct { ... }
func (m *Merge) Stop()
func (m *Merge) Done() <-chan struct{}
func (m *Merge) Err() error
```

`MergeFrom` subscribes to an upstream stream and re-emits matching events into
the downstream stream. The downstream stream assigns new sequence numbers while
preserving the original source and copying the upstream sequence number into
metadata. This lets runner-level streams combine parallel executor or skill
streams into one outward-facing stream.

Persistence API draft:

```go
type Store interface {
    Append(ctx context.Context, event Event) error
    Load(ctx context.Context, scope Scope, from uint64, filter Filter) ([]Event, error)
}
```

The stream should support an optional store. When configured, `Emit` appends
before publishing to subscribers so replay has a durable source.

## Stdio Model

The stream system is conceptually connecting producers and subscribers through
stdio-like JSON chunks:

- Producers write newline-delimited JSON events.
- Subscribers read and unmarshal each event independently.
- Terminal output becomes a renderer subscriber that formats selected events.
- Artifact logs become NDJSON subscribers.
- Conversation sessions keep storing lifecycle state needed to resume or trim a
  skill conversation. They should not store every outward stream event.

This does not require every internal component to literally use `os.Stdout`.
The important contract is that each event is a JSON chunk that can travel
through stdio, files, pipes, or in-memory channels.

## Filtering and Payload Decision

Filtering stays at subscription and merge boundaries for now; producers do not
inspect the active subscriber set before emitting. This keeps event semantics
stable for multi-subscriber delivery, replay, persistence, and late subscribers.
It also avoids ref-counted producer-side filters that would become complex once
streams can merge other streams.

Traffic reduction is handled in two places:

- `Subscribe` filters what a receiver consumes.
- `MergeFrom` filters what an upstream stream forwards into a downstream
  stream.

Large generated content should not be streamed as raw deltas by default. Code
blocks, patch text, file contents, model-generated scripts, CSVs, plots, and
other durable data should be written as artifacts and represented in stream
events by compact metadata or `artifact.created` references. Receivers can then
decide whether to load those artifacts.

## Cross-Layer Synchronization Through Stream

The stream system must become the **single mechanism** for any cross-layer
information transfer that needs more than a value return. Every notification,
progress update, lifecycle transition, and state change that another component
might observe should flow through the stream rather than through ad hoc
channels, sync.Cond patterns, or one-off signal channels.

### Why this is a core invariant

The 2026-05-02 audit found a latent deadlock in `Runner.waitForPhase`. The
runner had a `phaseSignal chan struct{}` separate from the stream, and three
phase-assignment sites (`StartPolish`, `RejectPolishedPlan`, `ReplanWith`) only
emitted the stream event but forgot to send to `phaseSignal`. That version
worked only because no waiter happened to be listening for those phases. Any
future caller of `waitForPhase(PhasePolishing)` would have blocked forever.

This is the canonical failure mode of parallel notification mechanisms: a
producer must remember to update every channel, and silently drops bugs are
indistinguishable from "no one was listening." A stream-first model removes the
class entirely — there is one place to publish, one place to subscribe, and
the mechanism takes care of fan-out, ordering, replay, and late attach.

### Required pattern

Every internal coordination point follows the same shape:

1. **Producer emits** a structured event with a stable `event_type`, current
   `status`, and the `delta` describing the change. The event must be self-
   contained: a subscriber receiving only this event must be able to decide
   what to do without consulting other side channels.
2. **Subscriber subscribes** with a filter and the appropriate replay option
   so it never misses a state it cares about, even if it attaches late.
3. **Subscriber analyzes** each event (event type, status, scope, metadata)
   and decides whether to act, ignore, or wait for more.
4. **Subscriber responds** by acting locally, optionally publishing a
   follow-up event of its own. It does not call back into the producer through
   a hidden channel.

This pattern collapses to "subscribe → analyze → respond" and naturally
supports both blocking consumers (`Subscription.Next(ctx)`) and non-blocking
consumers (select over `Subscription.Events()` with other channels).

### Race-avoidance properties

- A late subscriber receives replay from the stream buffer or store, so there
  is no "missed the signal" window.
- Sequence numbers are assigned at the producer; subscribers see a strictly
  ordered view even when the producer emits from multiple goroutines.
- Filters live at the subscription boundary, not at emission time, so adding
  a new subscriber cannot break existing ones.
- Subscriber buffers are bounded with explicit drop/error policy, so a slow
  consumer cannot deadlock the producer.
- The producer never inspects "is anyone listening?" before emitting, so
  there is no observer-presence race.

### Audit task: migrate ad hoc sync mechanisms onto the stream

Every parallel agent picking up this branch must, when touching code that
needs cross-goroutine or cross-layer coordination, prefer the stream. When
encountering an existing ad hoc channel or signal, evaluate whether it
should be replaced by stream subscription.

Initial audit list (non-exhaustive — extend as new instances are found):

- `Runner.phaseSignal` (`internal/engine/runner/runner.go`) ✓ removed.
  `waitForPhase` now subscribes to `runner.phase.changed` events on the
  runner's event stream, with a stable runner scope filter and replay so the
  waiter cannot miss a transition that fired before subscription.
- `Runner.Subscribe` low-level `RunnerEvent` fan-out
  (`internal/engine/runner/runner.go`) ✓ removed. The engine loop still uses
  `RunnerEvent` internally for persistence records, but no longer broadcasts it
  through a separate subscriber slice. Each internal event now refreshes the
  cached snapshot and emits the compact stream event directly. Managed lifecycle
  consumers such as `acceptAndComplete` subscribe to the runner stream.
- `Runner.Done()` settle channel ✓ migrated. The runner now starts a single
  settle-observer goroutine in `New` that subscribes to its own
  `runner.phase.changed` events and closes `r.done` exactly once when phase
  reaches `PhaseSettled`. `Abort` and `settle` no longer call a separate
  `markDone` — emitting the stream event is the only settle notification path,
  and `r.done` is now a derivative of the stream rather than a parallel
  mechanism. The external `Done()` / `Wait()` API shape is preserved.
- Conversation lifecycle hooks. As Conversation events are unified, any
  remaining hand-rolled callback (e.g. provider-replay state mutation
  notifications) should publish onto the stream and have its consumers
  subscribe rather than receive through a closure.
- Skill memo / progress / playground notifications. When skills want to
  signal mid-task progress to the runner or executor, they must publish a
  stream event rather than mutating shared state or calling back through a
  caller-supplied channel.
- Orchestrator planning callbacks ✓ retired. `StartRequestWithProgress`,
  `OrchestratorProgressFunc`, and `OrchestratorProgressEvent` were removed.
  Provider-side streaming during planning is now opted into by setting
  `Request.StreamPlanning = true`; live reasoning and text deltas are emitted
  to `Request.Stream` as `llm.reasoning.delta` and `llm.output.delta` events,
  and the only way to observe them is to subscribe to the stream.

When migrating, preserve backward semantics by:

1. First adding the stream event at the producer, with a sequence number and
   stable scope.
2. Then converting one consumer at a time to a subscription, verifying the
   replay window is large enough that late subscribers cannot miss the
   relevant transition.
3. Removing the ad hoc channel only after every consumer has migrated.
4. Adding a focused `*_test.go` case that fails if a new subscriber attaches
   after the relevant event has fired and does not see it via replay.

### Hard rules

- Do not introduce new `chan struct{}` notifications, `sync.Cond` waiters,
  or one-off callback fields for cross-component sync. Publish a stream
  event and have the consumer subscribe.
- Do not assume a subscriber is present at the moment of emission. Use
  replay or persistence so late subscribers see the same history.
- Do not bypass the sequence number / scope / event_type contract by
  smuggling state through `metadata` or shared maps. If a piece of state is
  load-bearing for a subscriber, it must appear in `delta` or be derivable
  from the event family.
- Do not let producers block on subscriber buffers. Bounded buffers with
  drop/error policy are mandatory.

## Persistence Boundaries

There are two different persistence needs and they should stay separate:

- Stream persistence is an outer observability concern. A CLI, debug UI,
  database sink, or test harness can subscribe to the request stream and store
  the event families it cares about.
- Conversation session persistence is a lifecycle state concern for one skill
  conversation. It exists so a skill can continue, restart, trim context, and
  replay its own model/tool state during its lifetime.

Conversation sessions should persist only state mutations required to rebuild
the conversation: init, user input, assistant text, reasoning state needed for
provider replay, tool calls, tool outputs, usage, trim/drop/replace events, and
similar lifecycle records. They should not persist terminal-renderer events,
runner/executor progress, raw token deltas already represented by committed
assistant turns, whole upstream request payloads, file contents, or large
artifact bodies.

If conversation persistence is later connected through the stream system, it
must subscribe only to a narrow state-event family or share the same
conversation mutation append path. It must not become a blanket subscriber to
the whole request stream.

## Layer Integration Plan

### Conversation

Current state:

- `ConversationConfig` can bind a stream, source, scope, and metadata to a
  conversation.
- `Conversation.Stream` emits text/reasoning deltas to both its legacy channel
  and the configured stream.
- `Conversation.Run` emits model-returned tool calls, tool execution
  start/completion/failure, tool results, and final text output when a stream
  is configured.
- Session persistence is separate from user-visible streaming and remains
  focused on conversation lifecycle state.

Target:

- Conversation emits stream events for reasoning deltas, output deltas, tool
  calls, tool execution, tool results, response completion, usage, and errors.
- Existing session persistence may share the same append event path for
  lifecycle state mutations, or subscribe only to a narrow state-event family if
  that family is introduced. It must not subscribe to all output/progress
  events.
- `Conversation.Stream` can either become a compatibility wrapper during the
  transition or be replaced directly if the branch does not need API
  compatibility.

### Skill

Current state:

- Runner-created skill conversations receive node/skill stream config through
  `ConversationConfig`.
- Skill output is still the final `Run` text after the model/tool loop
  completes, but the inner model/tool loop now emits compact stream events.

Target:

- Skill binds a stream writer with source `{layer:"skill", id: skill.Name}`.
- Skill forwards conversation events with skill/node scope attached.
- Skill can emit memo/progress events when it enters polish/execute/report.

### Executor

Current state:

- Executor returns exchange markdown per phase.
- It can hide long skill execution behind a single call.

Target:

- Executor emits phase start/completion/failure events.
- Executor attaches `node_id`, `skill_name`, and artifacts root to scope or
  metadata.
- Executor forwards skill/conversation stream events upward without converting
  them into runner snapshots.

### Runner

Current state:

- `RunnerUpdate`, `Runner.SubscribeUpdates`, and `Orchestrator.SubscribeRunner`
  have been removed. The snapshot-bearing update channel no longer exists.
- Runners emit compact stream events (`runner.phase.changed`,
  `runner.node.started`, `runner.node.completed`, `runner.node.failed`,
  `executor.*`) directly to the request-scoped event stream.
- Every runner has a stream. Orchestrator-owned runners use the request-scoped
  stream; standalone runners use a runner-owned stream created by `New`.
- Phase change notification for internal managed lifecycle (`waitForPhase`)
  subscribes to `runner.phase.changed` with replay and no longer uses a
  parallel `phaseSignal` channel.
- Snapshot caching (`Runner.View`, `Runner.GetSnapshot`) is kept for query
  use only.
- `Runner.SubscribeStream` and `Orchestrator.SubscribeRunnerStream` are the
  only external subscription APIs.
- `Runner.Done()` / `Runner.Wait()` are thin convenience wrappers over a
  single internal settle-observer goroutine that subscribes to
  `runner.phase.changed` and closes `r.done` when phase reaches Settled.
  `Abort` and `settle` only emit the stream event; there is no separate
  `markDone` path.

Target:

- Done. Stream is the single source of truth for every runner-level
  notification (phase, node lifecycle, settle). Snapshot/query path remains
  separate by design.

### Orchestrator

Current state:

- `StartRequest` is the only request entrypoint. It creates or accepts a
  request-scoped stream and emits planning status, text, and (when opted in)
  reasoning deltas as compact JSON events.
- `Request.StreamPlanning bool` opts into provider-side streaming during
  planning. When true, the planner uses `Conversation.Stream` and emits
  `llm.reasoning.delta` / `llm.output.delta` events. When false (default),
  the planner uses non-streaming `Skill.Run` and emits a single
  `llm.output.delta` plus `status.completed` once planning finishes.
- Runner creation, runner lifecycle, executor phases, skill events, and
  conversation deltas all flow into the same request stream. CLI examples
  subscribe once and render selected events.

Target:

- Done. There is no parallel callback or progress notification for
  orchestrator-owned planning; subscribing to `Request.Stream` is the only
  observation path.

## Migration Plan

### Phase 1: Stream Core

- Add `internal/engine/stream` package with event type, source/scope structs, stream,
- subscription, `Subscription.Next`, filters, stream merging, and in-memory
  replay buffer.
- Add tests for multi-subscriber delivery, cancellation, filtering, ordering,
- replay, merging, iterator-style reads, and JSON marshal/unmarshal.
- No existing production behavior changes yet.

### Phase 2: Conversation Events

- Add stream support to conversation config.
- Emit LLM reasoning/output/tool events.
- Keep existing tests passing while adding stream-specific tests.
- Keep conversation session persistence out of the general request stream
  unless a later state-only event family makes the subscription boundary
  explicit.

### Phase 3: Orchestrator Request Stream

- Add request-scoped stream creation.
- Let `Request` carry an optional request-scoped stream.
- Emit orchestrator planning status, reasoning deltas, text deltas, and runner
  creation events into that stream.
- Attach the same stream to the runner so runner lifecycle and node events can
  flow through the same subscription.
- Update Honshu example to subscribe once and render selected events.

### Phase 4: Executor and Skill Forwarding

- Thread stream scope through runner/executor/skill binding.
- Emit executor phase events.
- Forward skill and conversation events upward.
- Ensure artifacts and exchange markdown remain final outputs, while progress
  is streamed separately.

### Phase 5: Runner Update Replacement ✓

- `RunnerUpdate`, `Runner.SubscribeUpdates`, `Orchestrator.SubscribeRunner`
  removed. No compatibility wrappers added.
- `emitPhaseChangedEvent` and `emitNodeStreamEvent` replace the old
  `publishStreamUpdate(RunnerUpdate)` path. Stream event emission now reads
  runner state directly instead of going through a snapshot struct.
- `waitForPhase` no longer subscribes to the removed update channel. It now
  uses `runner.phase.changed` stream events as of Phase 7 unit 1.
- Query API (`Runner.View`, `Runner.GetSnapshot`) kept for snapshot access.
- All existing runner, stream, and orchestrator tests pass.

### Phase 6: Persistence and Replay

- Introduce stream store implementation.
- Keep conversation session persistence focused on lifecycle replay state. Only
  connect it to stream append/replay if the subscribed event family is
  intentionally limited to conversation state mutations.
- Add catch-up subscription tests.

### Phase 7: Ad Hoc Sync Audit and Migration

Goal: eliminate every parallel notification mechanism and let the stream be
the single coordination primitive for cross-goroutine and cross-layer state
transfer. See "Cross-Layer Synchronization Through Stream" above for the
motivating audit and the canonical pattern.

Concrete migration units (each a self-contained PR-sized step):

1. ✓ Replace `Runner.phaseSignal` with a stream subscription to
   `runner.phase.changed` inside `waitForPhase`. Verify with a test where the
   subscriber attaches after a phase event and still sees it through replay.
2. ✓ Migrate `forwardEngineEvents` and `acceptAndComplete` off `Runner.Subscribe`
   to a stream subscription scoped to the runner. Remove the low-level
   `Subscribe` channel slice once unused.
3. ✓ Convert `Runner.Done()` and `Runner.Wait()` into thin wrappers over a
   one-shot phase-change stream subscription. A single settle-observer
   goroutine started in `New` is the only thing that closes `r.done`,
   reacting to a `runner.phase.changed{phase: Settled}` stream event. `Abort`
   and `settle` no longer mark done explicitly. External `<-chan struct{}`
   shape preserved.
4. Audit `internal/llm` Conversation, Skill, and tool-dispatcher code for any
   remaining callback fields, signal channels, or sync.Cond patterns and
   convert each to producer-emit / subscriber-consume on the active stream.
5. ✓ Retire `StartRequestWithProgress` in favor of a request-scoped stream
   subscription created and consumed by the caller. Replaced with a
   `Request.StreamPlanning bool` opt-in for provider-side streaming during
   planning; deltas emit only to `Request.Stream`.

Acceptance signal for Phase 7:

- `grep` for `chan struct{}`, `sync.Cond`, and bespoke `Subscribe*` methods in
  `internal/engine` and `internal/llm` returns only stream-package internals
  and well-justified low-level primitives.
- Adding a new subscriber type requires no producer changes.
- A late subscriber attaching after a state transition can still observe and
  react to that transition through replay.

## Open Design Questions

- Should stream sequence numbers be per stream, per source, or both? Initial
  recommendation: one monotonically increasing stream sequence plus optional
  source-local sequence in metadata if needed.
- Should `delta` be `json.RawMessage` in the core event, or generic `any` with
  marshal validation at emit time? Initial recommendation: accept `any` in
  writer APIs but store `json.RawMessage` in `Event`.
- Should filters be exact match only at first, or support prefix matching in
  Phase 1? Initial recommendation: include exact event types and event type
  prefixes from the start.
- How much replay should the in-memory buffer keep by default? Initial
  recommendation: bounded by event count, configurable per stream.
- Should terminal formatting live in `cmd`, `internal/engine/stream`, or a small
  `internal/engine/stream/render` package? Initial recommendation: keep renderers out
  of the stream core.
- How should backpressure behave when a subscriber is slow? Initial
  recommendation: bounded subscriber buffers with drop/error policy explicit in
  subscribe options.

## Current Progress

- 2026-05-02: Memo created.
- 2026-05-02: Phase 1 stream core added under `internal/engine/stream` with event
  shape, source/scope structs, filters, multi-subscriber delivery,
  cancellation-safe subscriptions, bounded in-memory replay, writer helpers,
  optional store append hook, and focused tests.
- 2026-05-02: Request/runner stream integration started. `engine.Request` can
  now carry a request-scoped stream; orchestrator planning deltas and runner
  lifecycle/node events publish compact JSON events to it. The Honshu example
  now subscribes once to that stream and saves raw events to
  `artifacts/stream_events.ndjson` instead of separately consuming planner
  callbacks and full runner snapshots.
- 2026-05-02: Executor phase events added for polish, execute, and report
  stages. They are emitted by the runner with source layer `executor`, node and
  skill metadata, and compact status/error deltas. Skill/conversation internals
  still need a later pass so tool calls and skill-owned LLM loops stream
  directly instead of being inferred around executor calls.
- 2026-05-02: Stream merge and iterator APIs added. Parent streams can now
  `MergeFrom` child streams with a filter, and subscribers can use
  `Next(ctx)` for simple loop-style consumption. Filtering remains at
  subscription/merge boundaries; raw large content should be emitted as
  artifact references rather than stream deltas.
- 2026-05-02: Conversation/skill stream events started. `ConversationConfig`
  can now bind a stream, source, scope, and metadata. `Conversation.Run` emits
  tool call start/completion, tool execution start/completion/failure, tool
  result, and final output events; `Conversation.Stream` emits configured
  text/reasoning deltas. Runner node construction passes request-stream config
  into skill conversations, so skill-owned LLM/tool activity now appears in the
  same request stream.
- 2026-05-02: Conversation persistence boundary clarified. Session stores are
  lifecycle state logs for skill conversation resume/trim/replay, not blanket
  request-stream archives. Outer callers that need archival output should
  subscribe to the request stream themselves.
- 2026-05-02: Runner stream subscription layer started. `StartRequest` now
  creates a request stream when callers do not provide one, `Runner` exposes
  `SubscribeStream`, `Orchestrator` exposes `SubscribeRunnerStream`, and the
  orchestrate demo watches compact stream events instead of `RunnerUpdate`
  snapshots.
- 2026-05-02: Phase 5 complete. `RunnerUpdate`, `Runner.SubscribeUpdates`, and
  `Orchestrator.SubscribeRunner` removed with no compatibility wrappers.
  `publishStreamUpdate(RunnerUpdate)` replaced by `emitPhaseChangedEvent()` and
  `emitNodeStreamEvent()` which read runner state directly. `waitForPhase` now
  uses `runner.phase.changed` stream events instead of subscribing to the
  removed update channel. Query path (`Runner.View`, `Runner.GetSnapshot`) kept
  for snapshot access. Stream and query paths are now fully separated.
- 2026-05-02: Latent deadlock found and fixed in `Runner.waitForPhase`.
  `StartPolish`, `RejectPolishedPlan`, and `ReplanWith` set `r.phase` directly
  while emitting the stream event but were not signaling the parallel
  `phaseSignal` channel. Worked only because no waiter happened to be listening
  for those phases. This became the canonical motivation for Phase 7: the stream
  system must become the single sync primitive so this class of "forgot to
  notify the side channel" bug stops being possible. Audit list and migration
  steps recorded under "Cross-Layer Synchronization Through Stream" and
  Phase 7.
- 2026-05-02: LLM provider streaming decoupled from the internal engine stream.
  A request-scoped stream no longer forces orchestrator planning to call the
  provider streaming API. Normal `StartRequest` uses the non-streaming skill
  run and emits the final planner text/status into the engine stream;
  `StartRequestWithProgress` remains the temporary legacy path that opts into
  provider streaming for live progress callbacks.
- 2026-05-02: Phase 7 unit 1 complete. `Runner.phaseSignal` and
  `notifyPhaseChanged()` removed. `Runner.New` now creates a runner-owned
  stream when no request stream is supplied, `waitForPhase` subscribes to
  replayable `runner.phase.changed` events, and `runEngine` emits the
  `PhaseExecuting` stream event after the engine state enters running.
- 2026-05-02: Phase 7 unit 2 complete. `Runner.Subscribe`, the low-level
  subscriber slice, and `forwardEngineEvents` were removed. The engine loop now
  updates the cached snapshot and emits compact stream events from the same
  internal event append path that persists `RunnerEvent` records.
  `acceptAndComplete` now subscribes to `EventStatusProgress` /
  `EventRunnerNodeFailed` stream events instead of reading a bespoke
  `RunnerEvent` channel. Runner tests were updated to observe
  `Runner.SubscribeStream`.
- 2026-05-02: Phase 7 unit 3 complete. `Runner.Done()` and `Runner.Wait()` no
  longer have a parallel `markDone` notification path. A single settle-observer
  goroutine started in `Runner.New` subscribes to `runner.phase.changed`
  events on the runner's own stream and closes `r.done` once when phase
  reaches `PhaseSettled`. `Abort` and `settle` only emit the stream event —
  the observer is the sole closer of the Done channel. The
  external `Done()` / `Wait()` API shape is preserved; tests in
  `stream_test.go` cover the contract.
- Current Honshu example output problem is accepted as the first driver for
  the stream refactor.
- Prior branch work already reduced `main.go` to skill-directory registration
  and moved domain work back into skills. That should remain a constraint for
  this refactor.

## Working Rules For Parallel Agents

- Update this memo when a design decision changes.
- Prefer replacing in-branch APIs directly instead of adding compatibility
  wrappers unless explicitly needed.
- Keep the stream core independent of orchestrator, runner, executor, skill,
  and LLM packages to avoid import cycles.
- Do not add terminal formatting logic to the core event bus.
- Add focused tests next to each changed source file.
- Keep event payloads compact by default; link to artifacts or persisted
  resources for large data.
- **Stream is the only sync primitive for cross-component coordination.** Do
  not add new `chan struct{}` signals, sync.Cond waiters, or callback fields
  for state notifications. Publish a stream event and have consumers
  subscribe. See "Cross-Layer Synchronization Through Stream" for the full
  rationale and the audit list. When you find an existing ad hoc mechanism in
  code you are touching, prefer to migrate it onto the stream rather than
  paper over it with another channel.
- Every cross-component notification must follow "subscribe → analyze →
  respond." The producer never inspects observer presence; the subscriber
  decides blocking vs non-blocking via `Subscription.Next` or
  `Subscription.Events`.
