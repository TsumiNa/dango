# Stream Merge Completion Fix Plan

## Status

The stream merge refactor has the bundle shape, hub mode, consumer expansion,
replay, persistence, and migrated runtime consumers in place. The focused and
full Go test suites pass, but the current implementation still misses two
completion criteria from `docs/stream-merge-alignment-plan.md`:

1. A downstream stream should behave as one switch hub for all merged upstreams.
2. Per-upstream FIFO order must be preserved across live delivery, bundle
   expansion, persistence, and replay.

This memo records the concrete repair plan for those gaps.

## Finding 1: Hub ownership is per merge, not per downstream

### Current behavior

`Stream.MergeWithConfig` creates a new `mergeHub` for every hub-mode merge call.
Production call sites then call `MergeWithConfig` once per upstream:

- Planner skill stream to request stream.
- Runner stream to request stream.
- Executor-owned stream to runner stream.

That means one downstream stream can have several independent hubs, each with
its own ticker, tick counter, lifecycle, and bundle emitter.

### Why this is wrong

The design goal says a downstream stream works like a switch hub for multiple
upstreams. Within one merge tick, the downstream should emit at most one bundle
containing all ready upstream deltas for that tick.

With one hub per upstream, two upstreams can emit two separate `merge.bundle`
events in the same wall-clock window. Each bundle can also start at `tick_id=1`,
which makes tick metadata local to the upstream merge rather than meaningful at
the downstream hub level.

### Expected behavior

For one downstream stream in hub mode:

- There is one hub per downstream merge window configuration.
- Each hub owns all registered upstream FIFOs for that downstream.
- One tick flush emits zero or one bundle to the downstream stream.
- If multiple upstreams have ready items in the same tick, those items appear in
  the same bundle.
- Stopping one `Merge` unregisters only that upstream. The shared hub stops only
  when all upstreams are gone, the downstream closes, or the hub context is
  canceled.

## Finding 2: FIFO join can skip an intervening event

### Current behavior

Hub enqueue tries to join every new event with the FIFO head whenever the FIFO
is non-empty.

For one upstream, use these join keys:

- `A`: same upstream, `llm.output.delta`, `running`.
- `B`: same upstream, `llm.reasoning.delta`, `running`.

If the upstream emits `A, B, A` before a tick flush, the current queue evolves as
follows:

```text
enqueue A1 -> [A1]
enqueue B1 -> [A1, B1]
enqueue A2 -> [A1+A2, B1]
```

The third event is joined into the head because it has the same join key as the
head. It skips over `B1`, which changes the logical event order and makes the
head event timestamp/logical time cover non-contiguous output.

### Why this is wrong

Join is only safe for adjacent deltas from the same upstream and the same join
key. Once the next queued event has a different join key, the current tick item
must stop joining and later events must remain queued behind that different-key
event.

### Expected behavior

The same `A, B, A` input should remain ordered:

```text
enqueue A1 -> [A1]
enqueue B1 -> [A1, B1]
enqueue A2 -> [A1, B1, A2]

tick 1 -> A1
tick 2 -> B1
tick 3 -> A2
```

Only adjacent same-key string deltas may join:

```text
enqueue A1 -> [A1]
enqueue A2 -> [A1, A2]
enqueue A3 -> [A1, A2, A3]

tick 1 -> A1+A2+A3
```

The joined event keeps the first event's timestamp, logical time, source, scope,
status, and metadata. That is acceptable only because the joined deltas are a
contiguous span in FIFO order.

## PR 11: Preserve same-upstream FIFO adjacency during joins

### Scope

Fix the per-upstream FIFO join algorithm without changing public merge APIs or
production merge call sites.

### Changes

1. Stop joining new events with the FIFO head during enqueue.
2. Enqueue events in arrival order until the FIFO depth limit is reached.
3. During tick flush, pop the FIFO head as the current tick item.
4. While the next queued event has the same join key and both deltas are JSON
   strings, pop and append it into the current tick item.
5. Stop joining as soon as the next queued event has a different join key or a
   non-joinable delta shape.
6. Keep `ErrBufferFull` behavior unchanged.

### Tests

1. `A, A, A` from one upstream joins into one nested event in one tick.
2. `A, B, A` from one upstream emits three ordered tick items, not `A+A, B`.
3. `A, object-A, A` does not join across the object payload.
4. Joined events retain the first event timestamp/logical time for a contiguous
   joined span.
5. Existing hub-mode direct, replay, persistence, and expansion tests still pass.

### Validation

- `go test ./internal/engine/stream`
- `go test ./...`

### Memo update

PR 11 implementation decision:

- Hub enqueue now only prepares and appends events to the upstream FIFO.
- Tick flush pops the FIFO head and joins only the immediately adjacent queued
   events that share the same join key and have JSON string deltas.
- Joining stops at the first different join key or non-string JSON payload.
- The joined event keeps the first event's timestamp, logical time, source,
   scope, status, and metadata. This is valid because the joined span is now
   guaranteed to be contiguous in that upstream FIFO.
- Buffer depth is checked before enqueue and join no longer bypasses the depth
   limit before a tick flush.

## PR 12: Share hub mode per downstream stream

**Status:** Implemented on `fix/stream-merge-downstream-hub`.

### Scope

Make hub mode match the switch-hub design for production `MergeWithConfig`
callers while preserving direct forwarding when `TickDuration == 0`.

### Changes

1. Add downstream-owned hub state to `Stream` for hub-mode merges.
2. Reuse a shared hub for compatible hub-mode `MergeWithConfig` calls on the
   same downstream stream.
3. Register each upstream subscription into that shared hub.
4. Keep `Merge.Stop` scoped to one upstream registration.
5. Stop the shared hub when all registered upstreams are closed/stopped, the
   downstream closes, or the hub context is canceled.
6. Keep `MergeFrom` and `MergeWithConfig` with `TickDuration == 0` on direct
   forwarding.
7. Keep raw bundle replay and `ReplayExpanded` behavior unchanged.

### Tests

1. Two hub-mode `MergeWithConfig` calls into the same downstream emit one
   bundle containing both upstreams when both are ready in the same tick.
2. The same scenario emits at most one downstream bundle per tick.
3. Tick IDs are downstream-hub scoped and increase across multi-upstream ticks.
4. Stopping one `Merge` removes only that upstream and does not stop other
   upstreams on the same downstream hub.
5. Closing the last upstream stops the shared hub and closes the relevant merge
   `Done` channels.
6. Default `MergeFrom` behavior remains direct forwarding.
7. Migrated production paths still expose the same expanded logical events.

### Validation

- `go test ./internal/engine/stream ./internal/engine/runner ./internal/engine ./internal/streamrender`
- `go test ./...`

### Memo update

Update `docs/pr-10-remaining-merge-consumers-memo.md` after this PR to record
that production hub-mode merges now share downstream-owned hubs, not one hub per
merge call.

PR 12 implementation decision:

- `Stream` now owns a hub registry keyed by normalized hub-mode merge window configuration.
- Compatible `MergeWithConfig` calls into the same downstream stream reuse the
   same `mergeHub`; `TickDuration == 0` still uses direct forwarding.
- A hub feeder keeps a pending registration until it sees the first upstream
   event, then registers the upstream identity derived from that event source.
- `Merge.Stop` cancels only that merge feeder. If the upstream was registered,
   its FIFO and subscription are removed from the shared hub while other upstreams
   continue using the hub.
- Natural upstream close still drains that upstream's queued events before the
   merge `Done` channel closes. The shared hub stops only when there are no
   pending registrations, active subscriptions, or queued FIFOs left.
- Downstream `Stream.Close` stops all downstream-owned shared hubs.
- Hub tick IDs are scoped to the downstream-owned hub, so multi-upstream bundles
   share one monotonically increasing tick sequence.

PR 12 validation:

- `go test ./internal/engine/stream ./internal/engine/runner ./internal/engine ./internal/streamrender`
- `go test ./...`

## Completion check after both PRs

After PR 11 and PR 12, review the original completion criteria again:

1. Each repair PR should contain colocated tests for the behavior it changes.
2. Direct forwarding should remain stable and independently tested.
3. Hub mode should surface overflow or emit errors through `Merge.Err` rather
   than dropping events silently.
4. Per-upstream FIFO order should hold for live delivery, bundle expansion,
   replay, and JSON store persistence.
5. The repair decisions should be recorded in this memo and the PR 10 memo.