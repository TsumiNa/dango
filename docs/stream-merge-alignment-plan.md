# Stream Merge Refactor PR Plan

## Goal

Refactor stream merging so a downstream stream works like a switch hub for
multiple upstream streams. Within each merge tick, the hub emits one bundle that
contains the upstream deltas ready for that tick. Downstream consumers parse the
bundle and select the nested stream events they care about.

## Design rules

1. Keep the existing `Stream.Emit`, `Subscribe`, replay, store, scope, and
   filtering behavior stable unless a PR explicitly changes it.
2. Preserve per-upstream FIFO order.
3. A merge tick emits at most one bundle into the downstream stream.
4. Within one tick, one upstream may contribute multiple deltas only when they
   have the same join key.
5. The join key is the upstream identity plus `EventType` and `Status`.
6. Join only append-safe delta payloads. Start with JSON strings and leave other
   payload shapes as separate FIFO entries.
7. If the next delta from the same upstream does not match the join key, keep it
   in that upstream FIFO and emit it in a later tick.
8. Do not introduce an external reactive-stream framework dependency. Use the
   standard library and small existing dependencies only when they directly
   simplify lifecycle or tests.

## PR 1: Add merge bundle event shape

**Scope**

Define the data shape used by later PRs without changing merge behavior.

**Changes**

1. Add a stream event type for merge bundles.
2. Add a bundle payload type that contains tick window metadata and nested stream
   events.
3. Add helper code to encode and decode bundle payloads.
4. Keep helper code near the stream behavior that emits or consumes bundles.

**Tests**

1. Bundle payload marshals and unmarshals without losing nested event fields.
2. Empty bundles are rejected or omitted according to the helper contract.
3. Existing stream tests continue to observe unchanged single-event merge
   behavior.

**Memo update**

Record the final bundle JSON shape and exact event type name.

## PR 2: Add process-local logical time

**Scope**

Add stable ordering metadata that bundles, replay, and debugging can use. This
PR must not change live merge delivery order.

**Changes**

1. Add `Event.LogicalTime uint64`.
2. Add a stream-owned logical clock.
3. Stamp each emitted event before assigning the per-stream sequence number.
4. Ensure merged child streams can share the parent clock when later hub PRs
   connect them.
5. Keep standalone streams working with their own clock.

**Tests**

1. A single stream emits increasing logical times.
2. Events preserve per-stream sequence numbers independently of logical time.
3. Existing JSON without `logical_time` still unmarshals.

**Memo update**

Record where `LogicalTime` lives and how standalone streams initialize it.

## PR 3: Extract upstream identity and join-key helpers

**Scope**

Introduce the minimal private helpers needed to identify one upstream FIFO and
decide whether two events can be joined. This PR must not change runtime merge
behavior.

**Changes**

1. Define the private upstream identity used by the hub.
2. Define the private join key from upstream identity, `EventType`, and
   `Status`.
3. Add a private append-safe delta join helper for JSON string payloads.
4. Leave non-string JSON payloads unjoined.

**Tests**

1. Events from the same upstream with the same type and status share a join key.
2. Different upstream, type, or status values produce different join keys.
3. JSON string deltas join in FIFO order.
4. Object, array, number, boolean, and null deltas do not join.

**Memo update**

Record the exact upstream identity fields and supported join payload shapes.

## PR 4: Add per-upstream FIFO buffer

**Scope**

Add the internal buffer used by the hub, independent of `MergeFrom`.

**Changes**

1. Add a private FIFO structure for events from one upstream.
2. Support enqueue, peek, pop, and same-tick join attempts.
3. When a queued event cannot join the current tick head, leave it queued for
   the next tick.
4. Keep buffer overflow behavior explicit and testable.

**Tests**

1. FIFO preserves per-upstream event order.
2. Same-key string deltas join into the current tick item.
3. Different-key events remain queued for a later tick.
4. Buffer depth limits return an error instead of silently dropping events.

**Memo update**

Record buffer overflow behavior and default buffer depth.

## PR 5: Add merge hub with tick flush

**Scope**

Implement the switch-hub core behind tests, but do not wire it into production
`MergeFrom` yet.

**Changes**

1. Add a private merge hub that owns multiple upstream FIFOs.
2. Add a `MergeWindow` tick duration.
3. On each tick, collect one ready item per upstream FIFO.
4. Emit one bundle payload for the tick.
5. Keep non-joinable extra items queued for later ticks.
6. Stop cleanly when all upstream subscriptions close or context is canceled.

**Tests**

1. One tick emits one bundle containing ready upstream events.
2. Multiple upstreams contribute to the same tick bundle.
3. A fast upstream with multiple joinable string deltas contributes one joined
   event.
4. A fast upstream with different event types contributes only the first event
   in the current tick and delays the rest.
5. Closing an upstream removes it without blocking other upstreams.

**Memo update**

Record tick flush rules and stop conditions.

## PR 6: Wire hub mode into merge configuration

**Scope**

Expose the hub as an opt-in merge mode while preserving current `MergeFrom`
behavior by default.

**Changes**

1. Add merge configuration for window duration and per-upstream buffer depth.
2. Keep zero window as the current direct-forwarding behavior.
3. Add an opt-in path that routes upstream subscriptions through the merge hub.
4. Keep the public API minimal and avoid compatibility wrappers for in-branch
   code.

**Tests**

1. Default `MergeFrom` behavior is unchanged.
2. Hub mode emits bundle events instead of direct child events.
3. Hub mode respects filters applied to upstream subscriptions.
4. Context cancellation and `Merge.Stop` stop hub goroutines.

**Memo update**

Record the public configuration shape and default values.

## PR 7: Add bundle-aware downstream consumption helpers

**Scope**

Make it easy for subscribers to read nested events from bundle payloads without
duplicating parsing logic in every consumer.

**Changes**

1. Add a helper that expands a bundle event into nested events.
2. Add a helper that applies an existing stream `Filter` to nested bundle
   events.
3. Keep normal non-bundle events pass-through for consumers that read both
   direct and bundled streams during migration.

**Tests**

1. Bundle expansion returns nested events in bundle order.
2. Existing filters select matching nested events.
3. Non-bundle events pass through unchanged.
4. Malformed bundle payloads return an explicit error.

**Memo update**

Record the consumer helper names and migration pattern.

## PR 8: Migrate one real caller to hub mode

**Scope**

Enable the new hub behavior in one narrow runtime path to validate the design
without changing every stream merge at once.

**Changes**

1. Pick the runner-to-parent or executor-to-runner merge path.
2. Enable hub mode only for that path.
3. Update the direct consumers in that path to expand bundle events.
4. Leave other merge paths on direct-forwarding mode.

**Tests**

1. The migrated path receives bundle events.
2. The migrated path still exposes expected nested LLM, tool, and status events
   to its consumer.
3. Existing unmigrated paths keep direct-event behavior.

**Memo update**

Record the migrated path, observed bundle shape, and any follow-up migration
targets.

## PR 9: Add replay and persistence handling for bundles

**Scope**

Ensure stored and replayed streams preserve the same bundle semantics as live
streams.

**Changes**

1. Confirm bundle events persist through the existing store path.
2. Add replay helpers that expand bundle events when callers request nested
   event replay.
3. Keep raw replay available for debugging stored bundle events.

**Tests**

1. Stored bundle events load back with nested events intact.
2. Expanded replay returns the same nested event order as live expansion.
3. Raw replay still returns the bundle event itself.

**Memo update**

Record the replay modes and which callers use each mode.

## PR 10: Migrate remaining merge consumers

**Scope**

Move remaining runtime merge paths to hub mode after the first caller proves the
model.

**Changes**

1. Enable hub mode for each remaining multi-upstream merge path.
2. Update consumers to expand bundles where needed.
3. Remove temporary migration-only pass-through handling if it is no longer
   needed.

**Tests**

1. All migrated paths expose the same logical nested events as before.
2. Multi-upstream output is bundled by tick.
3. Direct-forwarding mode remains available only if still needed by tests or
   explicit callers.

**Memo update**

Record which paths are migrated and whether direct-forwarding mode remains.

## Completion criteria

1. Each PR changes one narrow behavior and has colocated tests for that behavior.
2. No PR relies on a later PR to make its tests meaningful.
3. Hub mode never drops events silently.
4. Per-upstream FIFO order is preserved across live delivery, bundle expansion,
   persistence, and replay.
5. The memo is updated after each PR with only the decisions needed by the next
   PR.
