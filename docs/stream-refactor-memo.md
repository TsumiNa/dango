# Stream Refactor Memo

Last updated: 2026-05-03

This memo records the current design and remaining work for the stream refactor
across orchestrator, runner, executor, skill, and conversation code. It is kept
as a coordination note for this branch rather than a full changelog.

## Purpose

The Honshu groundwater example made the original handoff problem visible: long
engine runs were hard to inspect because progress, lifecycle state, token
deltas, runner snapshots, and persistence hooks traveled through separate
callbacks and channels.

The refactor introduces one normalized output stream. Producers publish small
JSON events, and callers attach subscribers for terminal rendering, artifact
logs, debug views, persistence, or tests. Provider streaming remains only an
LLM transport detail; the engine stream is the outward observation channel.

## Design Invariants

- Every outward event is a small JSON object with stable `event_type`, `from`,
  `sequence_number`, `status`, and `delta` fields.
- Producers emit without inspecting whether anyone is subscribed.
- Subscribers attach through filters and replay options, so late consumers can
  rebuild the relevant context.
- Stream subscribers are bounded and use explicit overflow behavior so a slow
  consumer cannot block a producer.
- Cross-component notifications use stream events rather than parallel
  callbacks, `sync.Cond`, or one-off signal channels.
- Conversation session persistence remains a lifecycle state log for resume and
  trim behavior. It must not become a blanket archive of the outward request
  stream.
- Large generated outputs travel as artifacts or resource references. Stream
  payloads should describe those artifacts, not inline full files or snapshots.

## Event Contract

The core event shape lives in `internal/engine/stream`:

```go
type Event struct {
    EventType      string          `json:"event_type"`
    From           Source          `json:"from"`
    SequenceNumber uint64          `json:"sequence_number"`
    Status         string          `json:"status"`
    Delta          json.RawMessage `json:"delta"`
    Timestamp      time.Time       `json:"timestamp,omitempty"`
    Scope          Scope           `json:"scope,omitempty"`
    Metadata       map[string]any  `json:"metadata,omitempty"`
}
```

Important event families today:

- `status.*` for request and high-level lifecycle status.
- `llm.*` for reasoning, output, tool calls, and tool results.
- `status.completed` / `status.failed` from conversations for response
  completion, usage snapshots, and terminal LLM errors.
- `tool.execution.*` for local tool execution lifecycle.
- `runner.phase.changed` and `runner.node.*` for runner progress.
- `executor.polish.*`, `executor.execute.*`, and `executor.report.*` for
  executor phase progress.
- `artifact.created` for executor-declared exchange resources.
- `skill.memo.delta` for memo sections declared in skill exchange documents.

## Current Architecture

### Stream Core

`internal/engine/stream` provides append-only streams, filtering, iterator-style
subscriptions, in-memory replay, stream merging, writer helpers, optional store
append hooks, and bounded subscriber overflow policies.

`MergeFrom` lets a downstream stream re-emit filtered upstream events while
assigning downstream sequence numbers. The original upstream sequence is kept
as metadata.

### Orchestrator

`StartRequest` is the only request entrypoint. It creates the request-scoped
stream itself and returns `StartRequestResponse{Stream, RunnerID}`. `Request` is
a pure input DTO and no longer owns stream configuration.

Orchestrator planning uses a non-streaming skill run, then emits compact
planning output and status events to the returned stream. There is no parallel
planning callback path.

### Runner

Runner update snapshots are no longer broadcast through `RunnerUpdate` or
`SubscribeUpdates`. Runners emit compact stream events for phase changes, node
lifecycle, executor phases, and settle state.

`Runner.Done()` and `Runner.Wait()` are derived from a single internal
settle-observer subscription to `runner.phase.changed`. Snapshot/query APIs such
as `Runner.View` and `Runner.GetSnapshot` remain separate by design.

### Executor And Skill

Executor phases emit stream events for polish, execute, and report lifecycle.
Runner-created skill conversations receive stream configuration with node and
skill scope, so model output, tool calls, tool execution, and tool results from
skill-owned LLM loops flow into the same request stream.

Skill exchange documents emit compact memo events from their `Memo` sections.

### Conversation

`ConversationConfig` can bind a stream, source, scope, and metadata to a
conversation. Configured conversations emit reasoning/output deltas, model tool
calls, local tool execution events, tool results, response completion with
usage, and terminal LLM failure events.

`Conversation.Stream` remains a provider transport API for incremental response
delivery. It is not the outward observation mechanism for engine progress, and
it should stay distinct from the request-scoped engine stream.

Conversation session persistence remains separate from outward stream
observability.

## Completed Milestones

- Stream core package added with event shape, filters, subscriptions, replay,
  merge, writer helpers, optional store append hook, and tests.
- Provider streaming and engine output streaming were separated.
- Request stream ownership moved from `Request` to `StartRequest` /
  `StartRequestResponse`.
- Orchestrator planning callbacks and `StartRequestWithProgress` were removed.
- Runner snapshot broadcast APIs were removed in favor of compact stream events.
- Runner phase waiting, managed completion, and done/wait signaling now derive
  from replayable stream events.
- Executor phase events and skill conversation stream configuration were wired
  into the request stream.
- Runner node completion now emits `artifact.created` for resources declared in
  normalized exchange outputs.
- Skill exchange memo sections now emit `skill.memo.delta` from polish,
  execute, and report outputs.
- Conversation response completion, usage snapshots, send failures, and run
  terminal failures now flow through the stream.
- Subscriber overflow handling was made explicit so slow consumers do not block
  producers.
- Stream replay now falls back to `Store.Load` when subscribers ask for events
  older than the current in-memory buffer window, and `internal/engine/stream`
  now includes a concrete JSON-backed durable store.
- The current `internal/llm` tree has been audited for ad hoc sync. No
  remaining cross-component callback or signal path was found there; remaining
  transport and lifecycle channels are intentional.

## Remaining Planned Work

### Durable Stream Replay

The stream now falls back to `Store.Load` when a subscriber asks for replay
outside the current in-memory buffer window, and the stream package now ships a
concrete JSON-backed durable store.

The remaining question here is deployment choice rather than missing plumbing:
decide whether outer observability should keep using the file-backed store, grow
a SQLite-backed stream archive, or only instantiate durable stores in specific
CLI/debug flows.

### Sync Audit Follow-Through

The current audit pass is clean:

- `sync.Cond` usage is absent in `internal/engine` and `internal/llm`.
- Bespoke subscription APIs are limited to `Runner.SubscribeStream` and
  `Orchestrator.SubscribeRunnerStream`, both of which delegate to the stream
  package.
- Remaining `chan struct{}` uses are confined to stream internals, provider
  transport behavior, queue/lifecycle primitives, and tests.

This is now a maintenance rule rather than a blocking migration item: newly
introduced cross-component notifications should continue to publish stream
events instead of adding side channels.

### Subscriber Implementations

Keep renderers and archives outside the stream core. Terminal formatting,
JSONL artifact logs, database sinks, and debug UIs should subscribe to the
request stream rather than changing producers.

## Working Rules For This Branch

- Update this memo only when a design invariant, major milestone, or remaining
  work item changes.
- Keep `internal/engine/stream` independent of orchestrator, runner, executor,
  skill, and LLM packages.
- Do not add terminal formatting logic to the stream core.
- Do not add new cross-component callback fields, `chan struct{}` signals, or
  `sync.Cond` waiters. Publish a stream event and subscribe to it.
- Add focused tests next to the changed source file for each behavior change.
- Keep stream payloads compact by default and reference artifacts for large
  content.
