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

- Runner publishes `RunnerUpdate` with full snapshots.
- Subscribers can receive too much implementation detail.

Target:

- Runner emits compact lifecycle events.
- Full snapshots become query/persistence data, not the default stream payload.
- Runner update API can be replaced or implemented as a subscriber/adapter
  over stream events during migration.

### Orchestrator

Current state:

- Request planning has a special `StartRequestWithProgress` callback.
- After runner creation, callers subscribe separately to runner updates.

Target:

- Orchestrator creates or accepts a request-scoped stream.
- Planning, runner creation, runner lifecycle, executor phases, skill events,
  and conversation deltas all flow into that stream.
- CLI examples subscribe once and render selected events.

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

### Phase 5: Runner Update Replacement

- Convert `RunnerUpdate` consumers to stream subscribers.
- Keep query APIs for current snapshots.
- Move raw snapshot persistence behind explicit store/query paths.

### Phase 6: Persistence and Replay

- Introduce stream store implementation.
- Keep conversation session persistence focused on lifecycle replay state. Only
  connect it to stream append/replay if the subscribed event family is
  intentionally limited to conversation state mutations.
- Add catch-up subscription tests.

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
