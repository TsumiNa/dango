# Memo: fully hide the bundle wire-format from `stream`'s public surface (deferred)

Status: **deferred** — the larger half of the stream public-surface cleanup. The
merge-tuning surface was collapsed to a single `Stream.Merge` in the safe cleanup;
this memo tracks closing the remaining **bundle wire-format** leak, which is a
redesign of the persistence sequence model, not a mechanical change.

## What still leaks

These stay exported after the safe cleanup:
`EventBatch`, `EncodeEventBatch`, `DecodeEventBatch`, `FilterBundleEvent`,
`EventMergeBundle`, `ExpandBundleEvent`, plus the `WithRawStream` subscribe option.

Two reasons they can't simply be unexported today:

1. **dango's own tests fabricate bundles.** Tests in `runner`, `store/internal/sqlite`,
   `streamrender`, and `orchestrator` build `merge.bundle` events with
   `EncodeEventBatch`/`EventBatch`/`EventMergeBundle` to exercise bundle handling.
   Unexporting breaks them at compile time.
2. **`ExpandBundleEvent` is the persisted-history read path.** The orchestrator
   subscribes with `WithRawStream` and persists raw `merge.bundle` frames;
   `orchestrator/describe.go`, `orchestrator/request.go` (terminal detection), and
   `examples/honshu_groundwater` then call `ExpandBundleEvent` when reading the log.

## Why it's a redesign (the sequence-model blocker)

Persistence is keyed on the **request-stream bundle sequence**:
- `EventLogStore.LoadEvents` returns events `ORDER BY sequence_number ASC`;
  `AppendEvent` rejects `sequence_number == 0`.
- describe replay resumes from `cursor.EventSequence + 1` and advances
  `LatestEventSequence` using the request-stream sequence of each stored frame.

But `ExpandBundleEvent` keeps each nested event's **original upstream**
`SequenceNumber`/`LogicalTime` (it only patches `Scope`). Upstream sequences
(runner-stream, skill-stream) are independent counters — they collide and aren't
monotonic within the request scope. So you cannot just "persist expanded events":
the store's ordering, the `from` resume, and the describe cursor would all break.

## What closing it entails

1. **Re-sequence expanded events** with monotonic request-stream sequence numbers
   (decide where: at the merge hub when it bundles, or at the persistence
   subscriber) so expanded events order correctly and the describe cursor keys on
   individual events. Reconcile with the live request-stream sequence counter.
2. **Persist expanded events**: drop `WithRawStream` on the persistence
   subscription; update describe replay + terminal detection to consume plain
   events (no `ExpandBundleEvent`). Bundles then never escape the live merge layer.
3. **Rewrite the bundle-fabricating tests** in the four packages — drive the real
   merge to produce bundles, assert on expanded output, or add a `stream/streamtest`
   helper — so none of them need the public bundle codec.
4. **Unexport the bundle codec** (`EventBatch`, `EncodeEventBatch`,
   `DecodeEventBatch`, `FilterBundleEvent`, `EventMergeBundle`, `ExpandBundleEvent`)
   and `WithRawStream`. The bundle becomes a purely internal live-merge optimization.

## Risk / scope

Touches the durable sequence/cursor model — get replay fidelity and resume
correctness under test before/after. This is a focused redesign; do it on its own,
not bundled with unrelated work. The live consumer path (`Response.Stream`
subscribers) is already clean (non-raw subscriptions auto-expand), so this only
affects the persisted-history path and the internal wire-format surface.
