# Stream-Merge Time-Alignment Plan

## Background

Today every `Stream.MergeFrom(upstream, ...)` runs an independent goroutine
that pulls events off one upstream `Subscription` and re-emits them into the
downstream stream. The downstream's `Emit` mutex serialises writes, but it
does **not** order them by source clock — the order is whichever goroutine
wins the lock race. Per-source FIFO survives (a single subscription channel
delivers in order), but cross-source order is non-deterministic.

When a runner has two skills streaming `llm.reasoning.delta` chunks
concurrently, their events interleave arbitrarily in the merged log. That
hurts:

- **Causal reading** — a child's `polish.started` can sometimes appear
  *after* the first reasoning chunk it logically precedes.
- **UI consolidation** — the renderer's per-source live-line and marquee
  buffers stay correct individually, but the cross-skill story you read top
  to bottom in the JSONL is whatever the goroutine scheduler decided.
- **Replay determinism** — re-running a saved JSONL through the renderer
  produces a different visual story than the original session because
  processing-time skew differs.

Per-source FIFO is already correct; this plan is about the **cross-source**
order.

## Survey of mainstream approaches

| Approach | Where used | Fit for us |
|---|---|---|
| Lamport timestamps | classic distributed systems | Partial-order only. Doesn't disambiguate concurrent multi-source. |
| Vector clocks | distributed databases | Per-source counter table per event. Detects causality but heavy and overkill in-process. |
| Monotonic clock + sort window | log mergers (`logmerge`, `lnav`) | Simple, in-process. Risks reordering events that were already in causal order. |
| **Watermarks** | Apache Flink, Beam, Kafka Streams, Materialize | Each source publishes "no events older than T". Merger releases events in time order once all sources have caught up. Bounded latency. |
| K-way merge with per-source heads | external sort, sstable merge | Pulls one event at a time per source. Requires sources to be drainable on demand (true for our channels). |
| Sequence-at-fork | causal broadcast in tree topologies | Lightweight; parent records its sequence at the fork point and the merger interleaves so child events fall between siblings correctly. |

Dango's merge graph is **tree-shaped** (request → planning + runner; runner →
executors → skills) and runs **in one process** with one wall clock. Watermarks
and sequence-at-fork both fit; they're complementary.

## Proposed plan (three phases)

### Phase 1 — process-wide logical time

Make every event in one request tree carry a single monotonically-increasing
`LogicalTime`, so the merger has a total order without trusting wall clocks
or per-source counters.

**Changes**

1. New field `Event.LogicalTime uint64` (in addition to `SequenceNumber`,
   which stays per-stream and identifies positions inside a single stream).
2. New type `stream.Clock` — `*atomic.Uint64`. The **root** stream of a
   request tree owns one. Every `Stream` constructed via `MergeFrom`
   inherits the parent's clock pointer instead of allocating its own.
3. `Stream.prepare` (or the Emit path) calls `clock.Add(1)` and stamps the
   event before assigning the per-stream `SequenceNumber`.
4. Backwards compat: a stream constructed via `stream.New(...)` without an
   inherited clock allocates its own `Clock`; standalone usage keeps working.

**What this buys**

- Every event in the tree has a unique `LogicalTime`. Sorting by it gives
  the canonical "what happened in what order" inside one request.
- JSONL replay can sort by `LogicalTime` and reconstruct that order even if
  the file was written with concurrent goroutine interleaving.
- Renderer keeps its current "first arrival wins" behaviour by default;
  alignment becomes opt-in via Phase 2.

**Cost** — one atomic add per emit, one extra `uint64` per event.

### Phase 2 — bounded watermark merge

Use the LogicalTime stamp to actually re-order events as they cross the
merge boundary, so live consumers (the renderer, JSONL writer) see a
consistent order without needing post-hoc sort.

**Mechanism**

Each `MergeFrom` runs a small goroutine pool of N upstream subscribers (N =
number of sources currently fanning into the same downstream). Instead of
each upstream goroutine independently re-emitting, they push into per-source
FIFO buffers held inside the merger. A single emitter goroutine watches all
buffers and applies this loop:

1. If every live source has a `head` event buffered → pick the head with the
   smallest `LogicalTime` and emit it. Replenish.
2. If at least one live source has no head → wait, but bounded by
   `MergeWindow` (default 50ms).
3. When the bound expires, release whatever heads exist sorted by
   `LogicalTime`. Sources that still have nothing get a "watermark" entry
   (`{LogicalTime: now, dummy: true}`) treated as "you can move on past
   me".
4. When an upstream closes, drop it from the live set.

**Properties**

- Cross-source order respects `LogicalTime` whenever sources keep up.
- Worst-case added latency = `MergeWindow` (capped, configurable). Lazy
  sources never block the whole tree forever.
- Per-source FIFO preserved (we always pull from the head of one source).
- Pure addition: existing single-source `MergeFrom` callers see no behaviour
  change because there's only one buffer to drain.

**Configuration**

```go
type MergeOptions struct {
    Window      time.Duration  // default 50ms; 0 disables alignment (current behaviour)
    BufferDepth int            // per-source FIFO cap; default 256
}
```

Defaulting `Window` to 0 keeps current behaviour as the explicit "no
alignment" mode while letting callers opt in.

### Phase 3 — replay & verification tooling

To make the alignment guarantee testable and reproducible:

1. New package `internal/engine/streamreplay`:
   - `Reader(io.Reader) → iterator over stored events, sorted by LogicalTime`.
   - `Player(Reader, time.Duration speed) → re-plays into a fresh Stream`.
2. Test helper `streamtest.AssertOrderedByLogicalTime(t, events)` that the
   merger's output is non-decreasing in `LogicalTime` whenever the merger
   was configured with `Window > 0`.
3. Honshu example test gains a "consistency" assertion that runs the
   recorded JSONL through `streamreplay` and verifies the sequence the
   renderer would consume matches the live session's order.

## Migration strategy

- Phase 1 is a pure additive schema change. Existing JSONL files without
  `LogicalTime` parse fine (zero value); new files carry the stamp. Renderer
  tests and existing consumers stay green.
- Phase 2 is gated by `MergeOptions.Window`. Until callers opt in, runtime
  behaviour is identical to today. Honshu can flip it on first to validate.
- Phase 3 builds on Phase 1 alone; it's useful even if Phase 2 is deferred.

## What I'd defer

- **Vector clocks** — we don't need true concurrency detection in-process;
  total order via `LogicalTime` is enough. Keep the schema simple.
- **Cross-process merge** — out of scope. If we ever distribute Dango
  workers, switch to watermark messages on the wire. The Phase-2 plumbing
  is the same; only the source of `LogicalTime` would change (per-worker
  monotonic + reconciler).
- **Reordering of past events for late arrivals** — the merger only delays
  release within `Window`; events older than the window arrive
  out-of-order. Acceptable for a UI, unacceptable for a database. We are
  the former.

## Open questions for review

1. Should `LogicalTime` live in `Scope` (so it's grouped with other
   correlation IDs) or as a top-level `Event` field? I lean top-level
   because it's not request-scoped — it's stream-tree-scoped.
2. `MergeWindow` default — 50ms is a guess. Should we tune from the Honshu
   run's typical inter-event gap (looks like 80–200ms during streaming)?
3. Do we want a "strict" mode that drops events whose `LogicalTime` is
   smaller than the last emitted (i.e. arrived later than `Window`)? My
   default is to emit them anyway with a metadata flag `late: true`, so
   nothing is silently dropped.
