# Stream Persistence Deployment Memo

Last updated: 2026-05-03

This memo tracks a future PR decision: how, when, and where request streams
should be archived outside the stream core.

## Current Position

The stream package already supports durable replay plumbing:

- `Stream` can append emitted events to an optional `Store`.
- Subscribers can replay from memory and fall back to `Store.Load` when the
  requested range is older than the in-memory buffer.
- `internal/engine/stream.JSONStore` provides a simple JSONL-backed store.

That is enough for the current refactor. The remaining work is deployment
policy, not core stream behavior.

## Why This Is Separate

Default persistence is not obviously correct because streams can merge upstream
streams. If every layer enables its own store by default, one logical request
can produce overlapping archives at several levels. If each layer exposes its
own persistence configuration, top-level callers such as terminal renderers or
examples must understand too many storage decisions.

Storage location is also policy-heavy:

- Disposable replay support can live under a temp directory and disappear when
  the process exits.
- Debug archives should live in a user-accessible artifacts directory.
- Long-lived archives may eventually belong in SQLite or another database sink.

Mixing these choices into the stream refactor would create configuration noise
before the product shape is clear.

## Future PR Questions

- Which component owns stream archive configuration for a full request?
- Should archive stores attach only at the top-level request stream?
- Should debug mode select a user-visible artifacts directory by default?
- Should production archive sinks be stream subscribers instead of `Store`
  implementations attached to producers?
- Does SQLite need a stream-specific archive schema, or should archives stay as
  JSONL artifacts until there is a real query use case?

## Non-Goals For The Current Branch

- Do not enable stream persistence by default.
- Do not add cross-layer persistence configuration.
- Do not add database/archive sinks.
- Do not reintroduce `persistence.*` stream event types until a concrete archive
  sink needs observable lifecycle events.