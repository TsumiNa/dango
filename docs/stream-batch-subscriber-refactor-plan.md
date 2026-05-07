# Stream Batch Subscriber Refactor Plan

## Status

The stream merge refactor introduced hub-mode tick flushing, per-upstream FIFO
ordering, and `merge.bundle` events. The current implementation preserves the
logical data needed by downstream consumers, but the public stream surface still
exposes the batching transport shape directly.

In debug output this appears as `merge.bundle` events whose `delta` contains a
`nested_events` array. Even single-upstream ticks are represented as a bundle
with one nested event. Callers that want logical stream events must know about
the bundle event type and call expansion helpers before filtering, rendering,
waiting for lifecycle updates, or replaying persisted streams.

This memo breaks the cleanup into small PRs. The intended end state is that
batching remains available as an internal/raw transport frame, while ordinary
subscribers consume logical `Event` values without needing to know whether a
tick produced one event or several.

## Goals

- Keep hub-mode batching, tick IDs, and per-upstream FIFO ordering.
- Represent single-upstream and multi-upstream tick output with one consistent
  `[]Event` batch shape.
- Rename `nested_events` to `events` in the batch payload shape so the debug/raw
  representation describes what it contains directly.
- Make ordinary subscribe and replay APIs deliver logical events by default.
- Keep explicit raw/debug APIs for subscribers, persistence tests, and tools that
  need to inspect tick frames.
- Reduce duplicated bundle expansion logic in renderer, examples, runner, and
  request-waiting consumers.

## Non-Goals

- Do not remove hub-mode batching or revert to direct forwarding for merged
  streams.
- Do not change executor, runner, or orchestrator lifecycle semantics except
  where they currently compensate for raw bundle exposure.
- Do not redesign stream persistence storage in the same PR as subscriber API
  cleanup unless a small compatibility shim requires it.
- Do not solve exchange document payload-boundary issues here; those belong to
  the exchange system redesign.

## Target Model

Conceptually, a downstream stream receives delivery frames. A frame contains one
or more logical `Event` values:

```go
type EventBatch struct {
    TickID uint64  `json:"tick_id"` // omitempty planned for a later cleanup
    Events []Event `json:"events"`
}
```

Direct forwarding is a frame with one event and no meaningful tick ID. Hub-mode
merge ticks are frames with a tick ID and one or more events. Public logical
subscribers should not have to care which transport path produced the frame.

Raw/debug subscribers may still observe stored frames, including tick metadata,
for diagnostics and persistence verification.

## PR 1: Rename Batch Payload Vocabulary

**Status:** Implemented.

### Scope

Keep the current public behavior, but make the raw payload vocabulary clearer.

### Changes

1. Rename the payload type from bundle-oriented naming to batch-oriented naming,
   for example `BundlePayload` to `EventBatch` or `BatchPayload`.
2. Rename the JSON field from `nested_events` to `events`.
3. Keep backward-compatible decoding for old persisted/debug payloads that still
   contain `nested_events`.
4. Keep `merge.bundle` emission and existing expansion helpers in place for this
   PR.
5. Update tests and docs that assert the raw payload field name.

### Tests

- Encoding uses `events` for new payloads.
- Decoding accepts both `events` and legacy `nested_events`.
- Existing expansion/filter tests continue to pass with both payload names.

## PR 2: Introduce Internal Delivery Frames

### Scope

Separate the internal frame concept from the externally visible `Event` shape,
without changing default subscriber behavior yet.

### Changes

1. Add a small internal frame representation that carries `[]Event` plus optional
   tick metadata.
2. Make direct forwarding and hub-mode flushing both pass through the same frame
   normalization path before emission/storage.
3. Preserve current raw stream records so existing subscribers and persistence do
   not break in this PR.
4. Keep per-upstream FIFO joining and hub lifecycle behavior unchanged.

### Tests

- Direct forwarding normalizes to a one-event frame.
- Hub flushing normalizes to a frame containing one event for single-upstream
  ticks and multiple events for multi-upstream ticks.
- Frame normalization preserves event source, scope, status, logical time,
  timestamps, metadata, and upstream sequence metadata.

## PR 3: Add Logical Subscribe And Replay Paths

### Scope

Move bundle/batch expansion into the stream package so consumers can opt into
logical events without open-coding expansion.

### Changes

1. Add explicit logical replay and subscription paths if needed, or evolve the
   existing `Subscribe`/`Replay` implementation behind a compatibility layer.
2. Apply filters to logical events inside a batch, not only to the raw carrier.
3. Preserve raw replay/subscription helpers for debug and persistence inspection,
   such as `SubscribeRaw`, `ReplayRaw`, or equivalent local naming.
4. Keep ordering deterministic: raw stream order first, batch event order within
   each frame second.

### Tests

- A logical subscriber receives one event for a single-event batch and multiple
  events for a multi-event batch.
- Filters select matching events inside a batch and drop non-matching events.
- Replay returns logical events in the same order as live subscription.
- Raw replay/subscription still exposes the batch frame for debug consumers.

## PR 4: Make Ordinary Subscribers Logical By Default

### Scope

Flip the ordinary consumer-facing APIs to logical-event delivery and migrate
runtime callers off manual expansion.

### Changes

1. Make `Subscribe` and ordinary replay APIs return logical events by default,
   if PR 3 introduced temporary parallel logical APIs.
2. Move raw/debug consumers to the explicit raw APIs.
3. Remove manual calls to batch expansion from examples, stream renderer,
   request waiting helpers, runner consumers, and tests that are not validating
   raw persistence.
4. Ensure subscriber buffer semantics are documented for logical events, because
   one raw batch may expand into more than one delivered event.

### Tests

- Existing runtime consumers pass without knowing about `merge.bundle`.
- Renderer subscription tests observe logical events and still preserve raw debug
  logging through the raw observation path.
- Request and runner waiting helpers no longer need to expand bundle events.
- Subscriber buffer and overflow behavior is clear when one raw batch expands to
  multiple logical events.

## PR 5: Clean Up Bundle Compatibility Surface

### Scope

Remove or narrow the old bundle-specific API after consumers have migrated.

### Changes

1. Rename or deprecate bundle-specific helpers that now describe raw batch
   behavior poorly, such as `ExpandBundleEvent` and `FilterBundleEvent`.
2. Keep legacy decoders only where persisted data compatibility requires them.
3. Update docs and debug output examples to use `events` instead of
   `nested_events`.
4. Revisit whether the raw event type should remain `merge.bundle` or move to a
   clearer raw/internal name in a later API cleanup.

### Tests

- No production consumer depends on `merge.bundle` in ordinary subscribe/replay
  paths.
- Legacy persisted/debug batch payloads with `nested_events` still decode if the
  repository needs to read old stream archives.
- New debug output uses the `events` field.

## Validation Plan

Each PR should run the focused stream package tests plus the touched consumer
packages. The final PR should run:

- `go test ./internal/engine/stream`
- `go test ./internal/streamrender`
- `go test ./internal/engine ./internal/engine/runner`
- `go test ./...`

The Honshu groundwater example should be rerun after PR 4 or PR 5 to confirm
that ordinary progress rendering no longer exposes `merge.bundle` mechanics,
while debug/raw output still makes tick frames inspectable.

## Open Questions

- Should raw frame subscribers receive `EventBatch` values directly, or keep a
  raw `Event` carrier for persistence compatibility?
- Should `tick_id` be part of logical event metadata after expansion, or remain
  visible only in raw/debug frames?
- Should subscriber buffer limits count raw frames, logical events, or both?
- How long should legacy `nested_events` decoding remain supported?
- Should direct non-merge emissions be stored as raw one-event frames eventually,
  or is the frame abstraction only needed at merge boundaries?