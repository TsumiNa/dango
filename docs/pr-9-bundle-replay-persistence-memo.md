# Stream Bundle Replay and Persistence - PR 9 Memo

## Completed: PR 9 - Add replay and persistence handling for bundles

**PR**: https://github.com/TsumiNa/dango/pull/35

### Replay Modes

Streams now expose two explicit replay snapshot helpers:

```go
events, err := stream.Replay(filter, opts...)
events, err := stream.ReplayExpanded(filter, opts...)
```

`Replay` returns buffered or stored events exactly as they were emitted. Bundle
events stay as top-level `merge.bundle` events, which keeps raw replay useful
for debugging stored stream traffic.

`ReplayExpanded` loads the raw replay range, expands any `merge.bundle` events,
and then applies the caller's filter to the resulting logical events. Non-bundle
events pass through the same filter. Expanded nested events inherit missing
outer bundle scope fields through the existing `ExpandBundleEvent` behavior.

### Persistence

The existing `Store.Append` path persists bundle events without special casing.
`JSONStore` writes the top-level bundle event as one JSONL event whose delta is
the encoded `BundlePayload`. Reopened stores load the raw bundle event back with
nested events intact.

### Callers

- Use `Replay` for raw stream debugging and stored bundle inspection.
- Use `ReplayExpanded` when a caller wants replay in logical nested event order.
- Existing live subscribers still receive raw stream events and should call
  `ExpandBundleEvent` or `FilterBundleEvent` when reading migrated hub paths.

### Validation

- `TestStreamReplayExpandedExpandsStoredBundles`
- `TestJSONStorePersistsBundleEventsWithNestedEvents`
- `go test ./internal/engine/stream`

## Next: PR 10 - Migrate remaining merge consumers

Move the remaining production merge paths to hub mode, and make their consumers
expand bundled events before matching nested event types.